// pkg/dqmp/node.go
package dqmp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	crand "crypto/rand"

	"github.com/bradanzie/dqmp/pkg/api"
	"github.com/bradanzie/dqmp/pkg/config"
	"github.com/bradanzie/dqmp/pkg/data"
	"github.com/bradanzie/dqmp/pkg/energy"
	"github.com/bradanzie/dqmp/pkg/peers"     // Importer peers
	"github.com/bradanzie/dqmp/pkg/transport" //"github.com/bradanzie/dqmp/pkg/api"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
)

const NodeVersion = "0.1.0-dev" // Définir une version simple
const (
	DQMPAddrProtocolID     = protocol.ID("/dqmp/addr-exchange/1.0.0") // Sur libp2p transport
	DQMPIdentifyProtocolID = protocol.ID("/dqmp/identify/1.0.0")      // Sur DQMP transport
)
const MaxReplicas = 3 // Nombre maximum de pairs vers qui répliquer

const (
	pingTimeout           = 5 * time.Second
	pingStreamMsg         = "PING"
	pongStreamMsg         = "PONG"
	controlStreamProto    = "dqmp/control/ping/1.0" // Exemple de proto pour stream de contrôle
	replicateStreamPrefix = "REPLICATE"
)

const (
	gossipInterval       = 1 * time.Minute // Intervalle d'envoi du gossip
	gossipStreamPrefix   = "GOSSIP"
	gossipEndMarker      = "ENDGOSSIP"
	maxGossipPeersPerMsg = 50 // Limiter la taille des messages gossip
)

// Node représente une instance DQMP
type Node struct {
	config        *config.Config
	listener      *quic.Listener
	peerManager   *peers.Manager // Ajouter le peer manager
	apiServer     *api.Server
	dataManager   *data.Manager
	energyWatcher energy.Watcher
	startTime     time.Time
	p2pHost       host.Host    // Le Host libp2p
	kadDHT        *dht.IpfsDHT // Le DHT Kademlia
	mdnsService   *discovery.Discovery
	// discovery   *drouting.RoutingDiscovery // Helper pour l'annonce/découverte DHT
	stopChan chan struct{}
	wg       sync.WaitGroup // Pour attendre les goroutines
}

// loadOrCreateIdentity charge une clé privée libp2p ou en crée une nouvelle.
func loadOrCreateIdentity(idPath string) (crypto.PrivKey, error) {
	if _, err := os.Stat(idPath); err == nil {
		// Le fichier existe, le lire
		privBytes, err := ioutil.ReadFile(idPath)
		if err != nil {
			return nil, fmt.Errorf("erreur lecture fichier identité '%s': %w", idPath, err)
		}
		privKey, err := crypto.UnmarshalPrivateKey(privBytes)
		if err != nil {
			return nil, fmt.Errorf("erreur unmarshal clé privée depuis '%s': %w", idPath, err)
		}
		log.Printf("DISCOVERY: Identité libp2p chargée depuis %s\n", idPath)
		return privKey, nil
	} else if errors.Is(err, os.ErrNotExist) {
		// Le fichier n'existe pas, en créer un nouveau
		log.Printf("DISCOVERY: Aucune identité trouvée à '%s', génération d'une nouvelle...\n", idPath)
		// Utilise Ed25519 par défaut (bon choix)
		privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
		if err != nil {
			return nil, fmt.Errorf("erreur génération clé privée: %w", err)
		}

		// Sauvegarder la nouvelle clé
		privBytes, err := crypto.MarshalPrivateKey(privKey)
		if err != nil {
			return nil, fmt.Errorf("erreur marshal clé privée: %w", err)
		}
		if err := ioutil.WriteFile(idPath, privBytes, 0600); err != nil { // 0600 = read/write pour user seulement
			return nil, fmt.Errorf("erreur écriture fichier identité '%s': %w", idPath, err)
		}
		log.Printf("DISCOVERY: Nouvelle identité libp2p sauvegardée dans %s\n", idPath)
		return privKey, nil
	} else {
		// Autre erreur lors de Stat
		return nil, fmt.Errorf("erreur vérification fichier identité '%s': %w", idPath, err)
	}
}

// NewNode crée une nouvelle instance de nœud DQMP
func NewNode(cfg *config.Config) (*Node, error) {
	startTime := time.Now()

	// --- Initialisation Libp2p ---
	privKey, err := loadOrCreateIdentity(cfg.Discovery.IdentityPath)
	if err != nil {
		return nil, fmt.Errorf("échec gestion identité libp2p: %w", err)
	}

	// --- Préparer les relais statiques à partir des bootstrappers par défaut ---
	// 1. Obtenir les multiadresses des bootstrappers par défaut
	bootstrapPeersMA := dht.DefaultBootstrapPeers
	if len(cfg.Discovery.BootstrapPeers) > 0 { // Si l'utilisateur a défini ses propres bootstrappers
		log.Println("DISCOVERY: Utilisation des bootstrap peers définis par l'utilisateur comme relais statiques potentiels.")
		// Convertir les strings de la config en multiaddr
		customPeersMA := make([]ma.Multiaddr, 0, len(cfg.Discovery.BootstrapPeers))
		for _, addrStr := range cfg.Discovery.BootstrapPeers {
			maddr, errM := ma.NewMultiaddr(addrStr)
			if errM != nil {
				log.Printf("WARN: Ignorer l'adresse bootstrap invalide '%s' pour les relais statiques: %v", addrStr, errM)
				continue
			}
			customPeersMA = append(customPeersMA, maddr)
		}
		if len(customPeersMA) > 0 {
			bootstrapPeersMA = customPeersMA // Utiliser ceux de la config s'ils sont valides
		} else {
			log.Println("WARN: Aucun bootstrap peer valide trouvé dans la config, retour aux relais par défaut.")
			// Garder dht.DefaultBootstrapPeers
		}
	}

	// 2. Convertir les multiadresses en peer.AddrInfo
	defaultStaticRelays := make([]peer.AddrInfo, 0, len(bootstrapPeersMA))
	for _, maddr := range bootstrapPeersMA {
		pi, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			log.Printf("WARN: Impossible de convertir le bootstrap multiaddr '%s' en AddrInfo pour relais: %v", maddr, err)
			continue
		}
		defaultStaticRelays = append(defaultStaticRelays, *pi)
	}
	log.Printf("DISCOVERY: Utilisation de %d relais statiques potentiels (basés sur les bootstrap peers).\n", len(defaultStaticRelays))
	if len(defaultStaticRelays) == 0 {
		log.Println("WARN: Aucune information de relais statique utilisable trouvée!")
		// AutoRelay pourrait ne pas fonctionner sans relais statiques ou découverts via PeerSource.
	}
	// ---------------------------------------------------------------------

	// Créer le Host libp2p
	log.Println("DISCOVERY: Création du Host libp2p...")
	p2pHost, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(cfg.Discovery.ListenAddrs...),
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr { return addrs }),
		libp2p.EnableRelay(),

		// --- CORRECTION FINALE AUTORELAY ---
		// Passer la liste des AddrInfo des bootstrap peers comme relais statiques.
		libp2p.EnableAutoRelayWithStaticRelays(defaultStaticRelays),
		// -----------------------------------

		libp2p.EnableHolePunching(),
		libp2p.DefaultSecurity,
		libp2p.DefaultTransports,
		libp2p.DefaultMuxers,
		libp2p.EnableRelay(),
		libp2p.EnableNATService(),
		// libp2p.EnableMDNS(),
	)
	if err != nil {
		return nil, fmt.Errorf("échec création Host libp2p: %w", err)
	}
	log.Printf("DISCOVERY: Host libp2p créé avec ID: %s\n", p2pHost.ID().String())
	log.Println("DISCOVERY: Écoute libp2p sur:")
	for _, addr := range p2pHost.Addrs() {
		log.Printf("  - %s\n", addr.String())
	}
	// -----------------------------

	// Initialiser PeerManager avec l'ID du host local
	pm := peers.NewManager(p2pHost.ID())

	// Initialiser DataManager
	dm, err := data.NewManager(cfg.Data.Directory)
	if err != nil {
		p2pHost.Close() // Cleanup libp2p host
		return nil, fmt.Errorf("échec initialisation DataManager: %w", err)
	}

	// --- Initialiser EnergyWatcher ---
	// Pour l'instant, utiliser toujours le Mock. Plus tard, on pourrait choisir
	// en fonction de l'OS ou de la config.
	var watcher energy.Watcher
	// var errWatcher error

	if runtime.GOOS == "linux" { // Tenter le watcher Linux
		log.Println("ENERGY: Détection OS Linux, tentative d'initialisation du Watcher sysfs...")
		// Utiliser un intervalle de mise à jour raisonnable
		linuxWatcher, errLw := energy.NewLinuxWatcher(15 * time.Second)
		if errLw != nil {
			// Erreur fatale lors de l'init Linux? Pour l'instant, on loggue et on fallback sur Mock.
			log.Printf("ERROR: Échec initialisation LinuxWatcher: %v. Fallback sur MockWatcher.", errLw)
			watcher = energy.NewMockWatcher() // Fallback
		} else if linuxWatcher == nil {
			// Pas d'erreur fatale, mais sysfs non utilisable/incompatible
			log.Println("ENERGY: Watcher sysfs Linux non disponible ou incompatible, fallback sur MockWatcher.")
			watcher = energy.NewMockWatcher() // Fallback
		} else {
			// Succès !
			log.Println("ENERGY: Utilisation du LinuxWatcher basé sur sysfs.")
			watcher = linuxWatcher
		}
	} else {
		// OS non Linux, utiliser le Mock par défaut
		log.Println("ENERGY: OS non Linux détecté, utilisation du MockWatcher.")
		watcher = energy.NewMockWatcher()
	}
	// Aucune erreur fatale retournée par les watchers pour l'instant
	// errWatcher reste nil

	// Créer l'instance Node *avant* l'API Server pour pouvoir la passer
	node := &Node{
		config:        cfg,
		peerManager:   pm,
		dataManager:   dm,
		energyWatcher: watcher,
		p2pHost:       p2pHost,
		startTime:     startTime,
		stopChan:      make(chan struct{}),
		// apiServer sera défini après
	}

	// Initialiser l'API Server en passant 'node' (qui implémente api.NodeReplicator)
	node.apiServer = api.NewServer(&cfg.API, pm, dm, watcher, node, startTime, NodeVersion)

	// Définir le handler pour l'échange d'adresse DQMP via libp2p
	node.p2pHost.SetStreamHandler(DQMPAddrProtocolID, node.handleDQMPAddrRequest)

	// Initialiser le DHT Kademlia
	// Passer le contexte global pour l'initialisation, mais utiliser un contexte
	// lié à la durée de vie du nœud pour les opérations continues.
	if err := node.initDHT(context.Background()); err != nil {
		// Cleanup
		if node.apiServer != nil {
			node.apiServer.Stop(context.Background())
		} // Arrêt immédiat
		if node.p2pHost != nil {
			node.p2pHost.Close()
		}
		if node.dataManager != nil {
			node.dataManager.Close()
		}
		return nil, fmt.Errorf("échec initialisation DHT: %w", err)
	}

	// --- Initialiser et Démarrer mDNS via NewMdnsService ---
	// Le nœud 'node' lui-même implémente l'interface mdns.Notifee
	// car il a la méthode HandlePeerFound.
	// Utiliser un nom de service spécifique à DQMP pour éviter les conflits
	// et ne découvrir que d'autres nœuds DQMP via mDNS.
	mdnsServiceName := "_dqmp-discovery._udp" // Exemple de nom spécifique
	// Lancer NewMdnsService, qui démarre le service en arrière-plan.
	// On ne stocke pas forcément le pointeur retourné si on ne l'arrête pas séparément.
	mdnsService := mdns.NewMdnsService(
		node.p2pHost,    // Le Host libp2p
		mdnsServiceName, // Nom unique pour ce service
		node,            // L'objet qui recevra les notifications (implémente Notifee)
	)
	// Pour démarrer explicitement (bien que NewMdnsService le fasse souvent) :
	if err := mdnsService.Start(); err != nil {
		log.Printf("WARN: Échec du démarrage explicite du service mDNS: %v", err)
		// Continuer même si mDNS échoue ? Probablement oui.
	} else {
		log.Println("DISCOVERY: Service mDNS démarré avec le nom:", mdnsServiceName)
	}
	// node.mdnsService = mdnsService // Stocker si besoin d'appeler Close() plus tard

	return node, nil
}

// handleDQMPAddrRequest est appelé quand un pair nous contacte via libp2p sur notre protocole.
func (n *Node) handleDQMPAddrRequest(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	log.Printf("ADDR_EXCHANGE: Requête d'adresse DQMP reçue de %s\n", remotePeer.ShortString())
	defer stream.Close() // Fermer le stream à la fin

	// Récupérer notre propre adresse d'écoute QUIC DQMP
	if n.listener == nil {
		log.Println("ADDR_EXCHANGE: Listener DQMP non prêt, impossible de répondre.")
		// Envoyer une erreur ou fermer ?
		stream.Reset() // Indique une erreur
		return
	}
	dqmpAddr := n.listener.Addr().String()

	// Envoyer l'adresse au pair distant
	// Utiliser un format simple (juste l'adresse en string + newline)
	_, err := stream.Write([]byte(dqmpAddr + "\n"))
	if err != nil {
		log.Printf("ADDR_EXCHANGE: Erreur envoi adresse DQMP à %s: %v\n", remotePeer.ShortString(), err)
		stream.Reset()
		return
	}
	log.Printf("ADDR_EXCHANGE: Adresse DQMP (%s) envoyée à %s\n", dqmpAddr, remotePeer.ShortString())
}

// initDHT initialise le DHT Kademlia
func (n *Node) initDHT(ctx context.Context) error {
	log.Println("DISCOVERY: Initialisation du DHT Kademlia...")

	// Options pour le DHT (mode serveur par défaut)
	dhtOptions := []dht.Option{
		dht.Mode(dht.ModeServer), // Important pour participer au réseau
		// dht.BootstrapPeers(n.libp2pBootstrapPeers()...), // On fera le bootstrap séparément
	}

	// Créer une instance DHT
	var err error
	n.kadDHT, err = dht.New(ctx, n.p2pHost, dhtOptions...)
	if err != nil {
		return fmt.Errorf("échec création instance DHT: %w", err)
	}

	// Lancer le bootstrap du DHT (connexion aux pairs initiaux)
	log.Println("DISCOVERY: Lancement du bootstrap DHT...")
	if err = n.kadDHT.Bootstrap(ctx); err != nil {
		n.kadDHT.Close() // Fermer le DHT si bootstrap échoue
		return fmt.Errorf("échec bootstrap DHT: %w", err)
	}

	// Connexion explicite aux bootstrap peers de la config
	// (Bootstrap peut être lent, on force la connexion)
	bootstrapPeers := n.libp2pBootstrapPeers()
	if len(bootstrapPeers) > 0 {
		log.Printf("DISCOVERY: Connexion explicite à %d bootstrap peers libp2p...\n", len(bootstrapPeers))
		var wgBoot sync.WaitGroup
		for _, pinfo := range bootstrapPeers {
			wgBoot.Add(1)
			go func(pi peer.AddrInfo) {
				defer wgBoot.Done()
				ctxBoot, cancel := context.WithTimeout(ctx, 15*time.Second) // Timeout connexion bootstrap
				defer cancel()
				if err := n.p2pHost.Connect(ctxBoot, pi); err != nil {
					log.Printf("DISCOVERY: Échec connexion bootstrap %s: %v\n", pi.ID, err)
				} else {
					log.Printf("DISCOVERY: Connecté au bootstrap peer %s\n", pi.ID)
				}
			}(pinfo)
		}
		wgBoot.Wait()
		log.Println("DISCOVERY: Connexions bootstrap terminées.")
	} else {
		log.Println("WARN: Aucun bootstrap peer libp2p configuré.")
	}

	log.Println("DISCOVERY: DHT Kademlia initialisé.")
	return nil
}

// libp2pBootstrapPeers convertit les strings multiaddr de la config en AddrInfo.
func (n *Node) libp2pBootstrapPeers() []peer.AddrInfo {
	peers := make([]peer.AddrInfo, 0, len(n.config.Discovery.BootstrapPeers))
	for _, addrStr := range n.config.Discovery.BootstrapPeers {
		if addrStr == "" {
			continue
		}
		addr, err := ma.NewMultiaddr(addrStr)
		if err != nil {
			log.Printf("WARN: Adresse bootstrap libp2p invalide '%s': %v\n", addrStr, err)
			continue
		}
		pi, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			log.Printf("WARN: Impossible d'extraire AddrInfo de '%s': %v\n", addrStr, err)
			continue
		}
		peers = append(peers, *pi)
	}
	return peers
}

// Start démarre le nœud DQMP
func (n *Node) Start() error {
	log.Printf("INFO: Démarrage du nœud DQMP v%s sur %s...\n", NodeVersion, n.config.Network.Listen)
	n.startTime = time.Now()

	listener, err := transport.Listen(n.config.Network.Listen)
	if err != nil {
		log.Printf("ERROR: Échec du démarrage du listener QUIC: %v\n", err)
		return err
	}
	n.listener = listener
	log.Printf("INFO: Écoute QUIC démarrée sur %s\n", n.listener.Addr().String())

	// Démarrer le serveur API si configuré
	if n.apiServer != nil {
		if err := n.apiServer.Start(); err != nil {
			log.Printf("ERROR: Échec du démarrage du serveur API: %v\n", err)
			// Décider si c'est une erreur fatale ou non
			// return err // Arrêter le nœud si l'API ne démarre pas ? Ou juste logguer ?
			// Pour l'instant, on loggue mais on continue.
		}
	}

	// --- Démarrer EnergyWatcher ---
	if n.energyWatcher != nil {
		if err := n.energyWatcher.Start(); err != nil {
			// Erreur non fatale pour le mock, mais pourrait l'être pour un vrai watcher
			log.Printf("WARN: Échec démarrage EnergyWatcher: %v", err)
		}
	}

	n.wg.Add(1) // Pour la boucle acceptConnections
	go n.acceptConnections()

	n.wg.Add(1)
	go n.runDiscovery()

	n.wg.Add(1)
	go n.runGossip()
	log.Println("INFO: Nœud DQMP démarré avec succès.")
	return nil
}

// runGossip gère l'envoi périodique d'informations sur les pairs aux voisins connectés.
func (n *Node) runGossip() {
	defer n.wg.Done()
	ticker := time.NewTicker(gossipInterval)
	defer ticker.Stop()

	log.Println("GOSSIP: Démarrage du gossip de pairs...")

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-n.stopChan; cancel() }()
	defer cancel()

	for {
		select {
		case <-ticker.C:
			n.performGossip(ctx)
		case <-n.stopChan:
			log.Println("GOSSIP: Arrêt du gossip.")
			return
		case <-ctx.Done():
			log.Println("GOSSIP: Contexte annulé, arrêt du gossip.")
			return
		}
	}
}

// performGossip envoie la liste des pairs connus à un sous-ensemble de voisins connectés.
func (n *Node) performGossip(ctx context.Context) {
	connectedPeers := n.peerManager.GetConnectedPeers()
	if len(connectedPeers) == 0 {
		return // Personne à qui parler
	}

	// Préparer le message de gossip (nos pairs connus)
	// On n'envoie que ceux qui ont un ID et potentiellement une adresse DQMP
	knownPeers := n.peerManager.GetAllPeers()
	gossipMessage := new(bytes.Buffer)
	gossipMessage.WriteString(gossipStreamPrefix + "\n")
	count := 0

	// --- Inclure nos propres informations énergétiques ---
	myStatus, err := n.energyWatcher.GetCurrentStatus()
	myEcoScore := float32(0.5) // Score par défaut en cas d'erreur
	if err == nil {
		myEcoScore = myStatus.EcoScore
	} else {
		log.Printf("WARN(GOSSIP): Impossible d'obtenir notre statut énergétique: %v", err)
	}
	myDQMPAddrStr := ""
	if n.listener != nil {
		myDQMPAddrStr = n.listener.Addr().String()
	} // Notre adresse d'écoute
	// Format: ID AddrDQMP EcoScore
	fmt.Fprintf(gossipMessage, "%s %s %.3f\n", n.p2pHost.ID().String(), myDQMPAddrStr, myEcoScore)
	count++

	for _, p := range knownPeers {
		// Ne pas s'envoyer soi-même ou des pairs sans ID
		if p == nil || p.ID == "" || p.ID == n.p2pHost.ID() {
			continue
		}
		// N'envoyer que ceux qui sont potentiellement joignables ou découverts récemment ?
		// Pour l'instant, envoyons ceux qui ont un ID.
		dqmpAddrStr := ""
		if p.DQMPAddr != nil {
			dqmpAddrStr = p.DQMPAddr.String()
		}
		// Format: ID AddrDQMP (AddrDQMP peut être vide)
		fmt.Fprintf(gossipMessage, "%s %s\n", p.ID.String(), dqmpAddrStr)
		count++
		if count >= maxGossipPeersPerMsg {
			break // Limiter la taille
		}
	}
	gossipMessage.WriteString(gossipEndMarker + "\n")

	if count == 0 {
		return // Rien à envoyer
	}

	messageBytes := gossipMessage.Bytes()

	// Envoyer à quelques pairs connectés (ex: 2 aléatoirement)
	// TODO: Stratégie de sélection plus intelligente
	numTargets := 2
	if len(connectedPeers) < numTargets {
		numTargets = len(connectedPeers)
	}
	// Sélection aléatoire simple (pas idéal mais suffisant pour démo)
	rand.Shuffle(len(connectedPeers), func(i, j int) {
		connectedPeers[i], connectedPeers[j] = connectedPeers[j], connectedPeers[i]
	})

	targetPeers := connectedPeers[:numTargets]

	log.Printf("GOSSIP: Envoi de %d entrée(s) de pairs à %d voisin(s)...\n", count, len(targetPeers))

	for _, targetPeer := range targetPeers {
		n.wg.Add(1) // Ajouter au waitgroup pour la goroutine d'envoi
		go func(p *peers.Peer, msg []byte) {
			defer n.wg.Done()
			logID := p.ID.ShortString()
			if logID == "" {
				logID = p.DQMPAddr.String()
			}

			if p.Connection == nil || p.Connection.Context().Err() != nil {
				// log.Printf("GOSSIP: Connexion à %s invalide, envoi annulé.", logID)
				return
			}
			conn := p.Connection

			gossipCtx, gossipCancel := context.WithTimeout(ctx, 10*time.Second)
			defer gossipCancel()

			stream, err := conn.OpenStreamSync(gossipCtx)
			if err != nil {
				log.Printf("GOSSIP: Échec ouverture stream vers %s: %v\n", logID, err)
				return
			}
			defer stream.Close()

			stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, err = stream.Write(msg)
			stream.SetWriteDeadline(time.Time{})

			if err != nil {
				log.Printf("GOSSIP: Échec écriture vers %s: %v\n", logID, err)
				stream.CancelWrite(1)
				stream.CancelRead(1) // Assurer fermeture propre
			} else {
				// log.Printf("GOSSIP: Message envoyé à %s.\n", logID)
			}
		}(targetPeer, messageBytes)
	}
}

// runDiscovery gère l'annonce et la recherche périodique de pairs DQMP via le DHT libp2p.
func (n *Node) runDiscovery() {
	defer n.wg.Done()

	// Créer le helper pour la découverte basée sur le routage DHT
	routingDiscovery := routing.NewRoutingDiscovery(n.kadDHT)

	// Tickers pour l'annonce et la découverte périodiques
	// Annoncer un peu plus souvent que chercher peut aider à maintenir la présence
	announceInterval := n.config.Discovery.DiscoveryInterval / 2
	if announceInterval < 15*time.Second { // Minimum raisonnable
		announceInterval = 15 * time.Second
	}
	announceTicker := time.NewTicker(announceInterval)
	defer announceTicker.Stop()

	discoveryInterval := n.config.Discovery.DiscoveryInterval
	if discoveryInterval < 30*time.Second { // Minimum raisonnable
		discoveryInterval = 30 * time.Second
	}
	discoveryTicker := time.NewTicker(discoveryInterval)
	defer discoveryTicker.Stop()

	log.Printf("DISCOVERY: Lancement de l'annonce et de la découverte DHT (Rendezvous: %s, Announce Interval: %v, Discovery Interval: %v)\n",
		n.config.Discovery.Rendezvous, announceInterval, discoveryInterval)

	// Contexte lié à la durée de vie de cette goroutine/du nœud
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-n.stopChan // Attend le signal d'arrêt du nœud
		cancel()     // Annule le contexte de découverte
	}()
	defer cancel() // S'assurer que cancel est appelé à la fin de runDiscovery

	// Annonce initiale
	n.performDHTAdvertise(ctx, routingDiscovery)

	// Délai initial pour laisser le temps au DHT et à AutoRelay de se stabiliser un peu
	initialDelay := 10 * time.Second // Augmenté un peu
	log.Printf("DISCOVERY: Attente initiale de %v pour stabilisation DHT/Relais...\n", initialDelay)
	select {
	case <-time.After(initialDelay):
		// Continuer après le délai
	case <-n.stopChan:
		log.Println("DISCOVERY: Arrêt pendant l'attente initiale.")
		return
	case <-ctx.Done(): // Contexte annulé pendant l'attente
		log.Println("DISCOVERY: Contexte annulé pendant l'attente initiale.")
		return
	}
	log.Println("DISCOVERY: Fin attente initiale, lancement première recherche.")

	// Découverte initiale
	n.performDHTFindPeers(ctx, routingDiscovery)

	// Boucle principale des tickers
	for {
		select {
		case <-announceTicker.C:
			n.performDHTAdvertise(ctx, routingDiscovery)
		case <-discoveryTicker.C:
			n.performDHTFindPeers(ctx, routingDiscovery)
		case <-n.stopChan:
			log.Println("DISCOVERY: Arrêt de la découverte DHT.")
			return
		case <-ctx.Done(): // Si le contexte est annulé pour une autre raison
			log.Println("DISCOVERY: Contexte de découverte annulé, arrêt.")
			return
		}
	}
}

// performDHTAdvertise annonce notre présence dans le DHT.
func (n *Node) performDHTAdvertise(ctx context.Context, routingDiscovery *routing.RoutingDiscovery) {
	log.Printf("DISCOVERY: Annonce de notre présence pour '%s'...\n", n.config.Discovery.Rendezvous)
	// Annoncer notre adresse QUIC DQMP? Pour l'instant, on annonce juste notre PeerID.
	// Les autres devront nous trouver et ensuite potentiellement nous demander notre adresse DQMP
	// via un protocole libp2p dédié, ou on pourrait la stocker dans le DHT (plus complexe).
	// ttl, err := dutil.Advertise(ctx, routingDiscovery, n.config.Discovery.Rendezvous) // Annonce le Host courant
	ttl, err := routingDiscovery.Advertise(ctx, n.config.Discovery.Rendezvous) // Annonce le Host courant

	if err != nil {
		log.Printf("DISCOVERY: Erreur lors de l'annonce DHT: %v\n", err)
	} else {
		log.Printf("DISCOVERY: Annonce réussie (TTL estimé: %v)\n", ttl)
	}
}

// performDHTFindPeers recherche d'autres pairs DQMP dans le DHT.
func (n *Node) performDHTFindPeers(ctx context.Context, routingDiscovery *routing.RoutingDiscovery) {
	log.Printf("DISCOVERY: Recherche de pairs pour '%s'...\n", n.config.Discovery.Rendezvous)

	// Utiliser le contexte de la goroutine runDiscovery qui sera annulé à l'arrêt
	findCtx, cancel := context.WithCancel(ctx)
	defer cancel() // Annuler ce contexte spécifique si la fonction se termine prématurément

	peerChan, err := routingDiscovery.FindPeers(findCtx, n.config.Discovery.Rendezvous)
	if err != nil {
		// Vérifier si l'erreur est due à l'annulation du contexte parent
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("DISCOVERY: Recherche DHT annulée ou timeout: %v\n", err)
		} else {
			log.Printf("DISCOVERY: Erreur lors du lancement de la recherche DHT: %v\n", err)
		}
		return
	}

	foundCount := 0      // Compte les pairs avec adresses traités pour connexion
	discoveredCount := 0 // Compte tous les pairs découverts
	processedPeers := make(map[peer.ID]bool)

	for peerInfo := range peerChan { // Boucle jusqu'à ce que le canal soit fermé ou le contexte annulé
		// Vérifier si le contexte a été annulé pendant l'itération
		if findCtx.Err() != nil {
			log.Printf("DISCOVERY: Contexte annulé pendant la réception des pairs trouvés (%v).\n", findCtx.Err())
			break // Sortir de la boucle for
		}

		// Ignorer soi-même
		if peerInfo.ID == n.p2pHost.ID() {
			continue
		}

		// Éviter de traiter le même pair plusieurs fois par recherche
		if processedPeers[peerInfo.ID] {
			continue
		}
		processedPeers[peerInfo.ID] = true
		discoveredCount++

		// Mettre à jour/Ajouter le pair dans le manager (même s'il n'a pas d'adresse pour l'instant)
		n.peerManager.AddOrUpdateDiscoveredPeer(peerInfo)

		// Logguer la découverte initiale
		log.Printf("DISCOVERY: Pair trouvé via DHT: %s (%d addrs initialement)\n", peerInfo.ID.ShortString(), len(peerInfo.Addrs))
		// for _, addr := range peerInfo.Addrs { log.Printf("  - Addr: %s", addr) } // Debug

		// Vérifier l'état actuel avant de tenter la connexion
		p, exists := n.peerManager.GetPeer(peerInfo.ID)
		// Tenter connexion si découvert ou échoué précédemment, et si une connexion n'est pas déjà en cours/active
		if exists && (p.State == peers.StateDiscovered || p.State == peers.StateFailed) {
			log.Printf("DISCOVERY: Tentative d'obtention d'adresse DQMP et connexion vers %s (état: %s)\n", p.ID.ShortString(), p.State)
			foundCount++ // On le compte comme traité pour connexion
			n.wg.Add(1)
			// Passer le contexte de runDiscovery (ctx) qui gère l'arrêt du nœud
			go n.requestDQMPAddrAndConnect(ctx, peerInfo) // Lancer la tentative même si 0 addrs initialement
		} else if exists {
			// log.Printf("DISCOVERY: Connexion vers %s non tentée (état: %s)\n", p.ID.ShortString(), p.State)
		} else {
			log.Printf("WARN: Pair %s non trouvé dans le manager juste après AddOrUpdateDiscoveredPeer?\n", peerInfo.ID.ShortString())
		}
	} // Fin de la boucle for range peerChan

	// Le canal est fermé ou le contexte est annulé
	if findCtx.Err() == nil { // Si on n'est pas sorti à cause d'une annulation
		if discoveredCount == 0 {
			log.Printf("DISCOVERY: Aucun pair trouvé pour '%s'.\n", n.config.Discovery.Rendezvous)
		} else if foundCount == 0 {
			log.Printf("DISCOVERY: Recherche terminée, %d pair(s) découvert(s), mais aucun n'était dans un état approprié pour tenter une connexion.\n", discoveredCount)
		} else {
			log.Printf("DISCOVERY: Recherche terminée, %d pair(s) découvert(s), tentative de connexion lancée pour %d.\n", discoveredCount, foundCount)
		}
	}
}

// --- requestDQMPAddrAndConnect (Version Finale avec Fallback Local) ---
func (n *Node) requestDQMPAddrAndConnect(ctx context.Context, peerInfo peer.AddrInfo) {
	// S'assurer que le WaitGroup est décrémenté à la fin de la goroutine
	defer n.wg.Done()
	remotePeerID := peerInfo.ID

	// Marquer le pair comme 'Connecting' dans le PeerManager avant toute tentative.
	// On ignore l'erreur potentielle ici, car le pair sera mis à jour plus tard.
	p, _ := n.peerManager.SetPeerConnecting(remotePeerID, nil)
	if p == nil { // Vérification paranoïaque
		log.Printf("ERROR: SetPeerConnecting a retourné nil pour %s! Impossible de continuer.", remotePeerID.ShortString())
		return
	}

	log.Printf("ADDR_EXCHANGE/CONN: Début tentative vers %s\n", remotePeerID.ShortString())

	// --- Tentative 1 : Échange d'adresse via Libp2p Stream ---
	var dqmpAddrStr string
	var streamErr error

	// Contexte avec timeout court pour l'ouverture du stream libp2p
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	// Essayer d'ouvrir le stream pour le protocole d'échange d'adresse
	stream, err := n.p2pHost.NewStream(reqCtx, remotePeerID, DQMPAddrProtocolID)
	cancel() // Annuler le contexte de la requête NewStream immédiatement après l'appel

	if err == nil {
		// Le stream libp2p a été ouvert avec succès !
		defer stream.Close() // Fermer le stream libp2p à la fin de cette tentative
		log.Printf("ADDR_EXCHANGE: Stream libp2p ouvert avec succès vers %s, lecture adresse DQMP...\n", remotePeerID.ShortString())

		// Lire la réponse (adresse DQMP) avec un timeout
		reader := bufio.NewReader(stream)
		readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second) // Timeout pour la lecture
		readErrChan := make(chan error, 1)
		go func() {
			var readErr error
			// Lire jusqu'au newline
			dqmpAddrStr, readErr = reader.ReadString('\n')
			readErrChan <- readErr
		}()

		select {
		case streamErr = <-readErrChan: // Lecture terminée (avec ou sans erreur)
		case <-readCtx.Done(): // Timeout de lecture
			streamErr = readCtx.Err()
		}
		readCancel() // Annuler le contexte de lecture

		// Analyser le résultat de la lecture
		if streamErr == nil {
			dqmpAddrStr = strings.TrimSpace(dqmpAddrStr)
			if dqmpAddrStr == "" {
				streamErr = errors.New("adresse DQMP vide reçue via stream")
			}
		}
	} else {
		// L'ouverture du stream libp2p a échoué
		streamErr = err // Conserver l'erreur originale de NewStream
	}

	// --- Analyse du résultat de la Tentative 1 et Fallback éventuel ---
	isNoAddressError := false // Flag pour savoir si l'erreur est liée à la non-joignabilité
	if streamErr != nil {
		errMsg := streamErr.Error()
		isNoAddressError = strings.Contains(errMsg, "no addresses") ||
			strings.Contains(errMsg, "no route to host") ||
			strings.Contains(errMsg, "cannot connect to peer") ||
			strings.Contains(errMsg, "dial backoff") ||
			strings.Contains(errMsg, "context deadline exceeded") // Peut souvent masquer "no addresses"
		log.Printf("ADDR_EXCHANGE: Échec tentative via libp2p pour %s: %v (IsNoAddress=%t)\n", remotePeerID.ShortString(), streamErr, isNoAddressError)
	}

	// --- Tentative 2 : Fallback Local (Si Tentative 1 échoue pour cause de non-joignabilité ET qu'on a vu des adresses locales via mDNS) ---
	// On vérifie si dqmpAddrStr est toujours vide ET si l'erreur est de type non-joignabilité ET si peerInfo contient des adresses locales
	if dqmpAddrStr == "" && isNoAddressError && n.hasLocalAddr(peerInfo.Addrs) {
		log.Printf("CONN(Fallback): Échec AddrExchange, tentative de connexion DQMP directe (hack local) vers %s\n", remotePeerID.ShortString())

		// Essayer de deviner le port DQMP de l'autre nœud basé sur le nôtre (4242 <-> 4243)
		// Ceci est très spécifique à notre configuration de test local à 2 nœuds.
		var targetPort string
		// Extraire notre port d'écoute DQMP
		listenParts := strings.Split(n.config.Network.Listen, ":")
		myPort := ""
		if len(listenParts) == 2 {
			myPort = listenParts[1]
		} else {
			// Format d'écoute inattendu, impossible de deviner
			log.Printf("CONN(Fallback): Format d'écoute (%s) inattendu, impossible de deviner le port pour %s\n", n.config.Network.Listen, remotePeerID.ShortString())
			n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("échec addr-exchange et fallback local (format port): %w", streamErr))
			return // Abandonner
		}

		// Deviner le port cible
		if myPort == "4242" {
			targetPort = "4243"
		} else if myPort == "4243" {
			targetPort = "4242"
		} else {
			log.Printf("CONN(Fallback): Port local (%s) non standard, impossible de deviner le port DQMP pour %s\n", myPort, remotePeerID.ShortString())
			n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("échec addr-exchange et fallback local (port non standard): %w", streamErr))
			return // Abandonner
		}
		dqmpAddrStr = "127.0.0.1:" + targetPort // Toujours utiliser loopback pour le fallback local
		log.Printf("CONN(Fallback): Adresse DQMP devinée pour %s: %s\n", remotePeerID.ShortString(), dqmpAddrStr)
		// Laisser la suite du code utiliser cette adresse devinée
	} else if dqmpAddrStr == "" {
		// Si dqmpAddrStr est toujours vide mais que l'erreur n'était PAS "no address"
		// OU si le fallback local n'a pas été activé/a échoué, alors abandonner définitivement.
		log.Printf("CONN: Échec final pour obtenir l'adresse DQMP de %s après toutes les tentatives.\n", remotePeerID.ShortString())
		// Utiliser streamErr s'il existe, sinon créer une erreur générique
		finalErr := streamErr
		if finalErr == nil {
			finalErr = errors.New("impossible d'obtenir l'adresse DQMP")
		}
		n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("échec final addr-exchange: %w", finalErr))
		return // Abandonner cette tentative de connexion
	}

	// --- Suite : Résolution de l'Adresse DQMP et Connexion QUIC ---
	log.Printf("CONN: Utilisation de l'adresse DQMP '%s' pour la connexion à %s\n", dqmpAddrStr, remotePeerID.ShortString())

	// Résoudre l'adresse UDP finale
	udpAddr, err := net.ResolveUDPAddr("udp", dqmpAddrStr)
	if err != nil {
		log.Printf("CONN: Impossible de résoudre l'adresse DQMP '%s' pour %s: %v\n", dqmpAddrStr, remotePeerID.ShortString(), err)
		n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("adresse DQMP invalide '%s': %w", dqmpAddrStr, err))
		return
	}

	// Mettre à jour l'état Connecting avec l'adresse DQMP résolue
	_, err = n.peerManager.SetPeerConnecting(remotePeerID, udpAddr)
	if err != nil {
		log.Printf("CONN: Erreur lors de la mise à jour de l'adresse DQMP pour %s: %v\n", remotePeerID.ShortString(), err)
		// Continuer quand même, car l'adresse est résolue
	}

	// Vérifier si on essaie de se connecter à soi-même
	if n.isSelf(dqmpAddrStr) {
		log.Printf("CONN: Adresse DQMP '%s' correspond à nous-mêmes, connexion annulée.\n", dqmpAddrStr)
		n.peerManager.SetPeerDisconnected(remotePeerID, errors.New("tentative connexion à soi-même"))
		return
	}

	// Vérifier si une connexion existe déjà pour cette adresse DQMP
	if existingPeer, exists := n.peerManager.GetPeerByDQMPAddr(udpAddr.String()); exists {
		// Si c'est le même PeerID que celui qu'on vise
		if existingPeer.ID == remotePeerID {
			// Et si la connexion est déjà établie
			if existingPeer.State == peers.StateConnected {
				log.Printf("CONN: Connexion DQMP déjà établie avec %s (%s).\n", remotePeerID.ShortString(), udpAddr.String())
				// Mettre à jour LastSeen pour rafraîchir mais ne pas recréer la connexion
				n.peerManager.SetPeerConnected(remotePeerID, existingPeer.Connection)
				return
			}
			// Si l'état est Connecting, une autre tentative est peut-être en cours. Laisser celle-ci continuer.
			log.Printf("CONN: Tentative de connexion DQMP vers %s (%s) déjà en cours.\n", remotePeerID.ShortString(), udpAddr.String())
		} else {
			// Conflit : L'adresse est utilisée par un autre PeerID !
			log.Printf("WARN: Conflit d'adresse DQMP ! %s est déjà utilisée par %s (tentative de connexion pour %s). Connexion annulée.",
				udpAddr.String(), existingPeer.ID.ShortString(), remotePeerID.ShortString())
			n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("conflit adresse DQMP avec %s", existingPeer.ID.ShortString()))
			return
		}
	}

	// Tout est bon, tenter la connexion QUIC DQMP
	log.Printf("CONN: Tentative de connexion QUIC DQMP vers %s (%s)\n", remotePeerID.ShortString(), dqmpAddrStr)
	// Utiliser le contexte principal ctx pour Dial (annulé à l'arrêt du nœud) mais avec un timeout spécifique
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := transport.Dial(dialCtx, dqmpAddrStr)
	dialCancel() // Libérer le contexte de timeout de Dial

	if err != nil {
		// La connexion QUIC a échoué
		log.Printf("CONN: Échec connexion QUIC DQMP vers %s (%s): %v\n", remotePeerID.ShortString(), dqmpAddrStr, err)
		n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("échec connexion QUIC DQMP: %w", err))
		return
	}

	// Connexion QUIC réussie !
	log.Printf("CONN: Connexion QUIC DQMP établie avec %s (%s)\n", remotePeerID.ShortString(), dqmpAddrStr)

	// Mettre à jour le PeerManager pour marquer comme connecté
	_, err = n.peerManager.SetPeerConnected(remotePeerID, conn)
	if err != nil {
		// Erreur lors de la mise à jour du manager (ex: conflit détecté au dernier moment?)
		log.Printf("ERROR: Échec mise à jour PeerManager après connexion DQMP vers %s: %v\n", remotePeerID.ShortString(), err)
		conn.CloseWithError(1, "Erreur interne PeerManager") // Fermer la connexion établie
		n.peerManager.SetPeerDisconnected(remotePeerID, err) // Marquer comme échoué
		return
	}

	// Lancer le handler de connexion pour gérer l'identification et les streams applicatifs
	n.wg.Add(1)
	go n.handleConnection(conn, false, remotePeerID) // false = sortant, passer l'ID attendu
}

// --- Helper hasLocalAddr (à ajouter dans node.go) ---
// Vérifie si la liste de Multiaddrs contient au moins une adresse loopback ou privée.
func (n *Node) hasLocalAddr(addrs []ma.Multiaddr) bool {
	for _, addr := range addrs {
		// Utiliser manet pour vérifier si c'est loopback ou privé
		isLoopback := manet.IsIPLoopback(addr) // Ignore l'erreur potentielle de IsIPLoopback
		isPrivate := manet.IsPrivateAddr(addr) // Ignore l'erreur potentielle de IsPrivateAddr
		if isLoopback || isPrivate {
			return true
		}
	}
	return false
}

// isSelf vérifie si une adresse correspond à l'adresse d'écoute locale.
// Simpliste, ne gère pas toutes les interfaces.
func (n *Node) isSelf(addr string) bool {
	if n.listener == nil {
		return false // Ne peut pas vérifier si on n'écoute pas encore
	}
	// listenAddr := n.listener.Addr().String()

	// Résoudre l'adresse cible
	targetUDPAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return false // Ne peut pas résoudre
	}

	listenUDPAddr, ok := n.listener.Addr().(*net.UDPAddr)
	if !ok {
		return false // Adresse d'écoute n'est pas UDP?
	}

	// Comparaison simple d'IP et Port
	// Attention: peut être trompeur avec 0.0.0.0 ou [::]
	// Une vérification plus robuste impliquerait de lister les IP locales.
	if listenUDPAddr.Port == targetUDPAddr.Port {
		if listenUDPAddr.IP.IsUnspecified() || targetUDPAddr.IP.Equal(listenUDPAddr.IP) {
			// Potentiellement soi-même, surtout si l'IP d'écoute est unspecified
			// Ou si les IP correspondent explicitement
			// Pourrait être affiné en vérifiant les IP locales
			return true
		}
		// TODO: Comparer targetUDPAddr.IP avec toutes les IP locales
	}

	return false // Par défaut, considérer que ce n'est pas soi-même
}

// acceptConnections boucle pour accepter les nouvelles connexions QUIC
func (n *Node) acceptConnections() {
	defer n.wg.Done() // Signaler la fin de cette goroutine
	log.Println("INFO: Prêt à accepter les connexions QUIC entrantes...")
	for {
		select {
		case <-n.stopChan:
			log.Println("INFO: Arrêt de la boucle d'acceptation.")
			return
		default:
		}

		acceptCtx := context.Background() // Le listener.Accept gère son propre timeout interne ou blocage
		conn, err := n.listener.Accept(acceptCtx)

		if err != nil {
			// Vérifier si l'erreur est due à la fermeture du listener pendant l'arrêt
			select {
			case <-n.stopChan:
				log.Println("INFO: Listener DQMP fermé pendant l'arrêt, fin de l'acceptation.")
				return // Sortir proprement
			default:
				// Si stopChan n'est pas fermé, l'erreur n'est pas (directement) due à l'arrêt global.
				// Vérifier les erreurs réseau courantes indiquant une fermeture.
				if errors.Is(err, net.ErrClosed) || errors.Is(err, quic.ErrServerClosed) {
					// Le listener a été fermé pour une raison quelconque (peut-être pendant l'arrêt, mais détecté ici).
					log.Println("INFO: Listener DQMP fermé (net.ErrClosed/quic.ErrServerClosed), fin de l'acceptation.")
					return // Sortir de la goroutine
				}

				// Si ce n'est pas une erreur de fermeture attendue, c'est une autre erreur.
				log.Printf("WARN: Erreur lors de l'acceptation d'une connexion QUIC DQMP: %v\n", err)
				// Attendre un court instant avant de réessayer pour éviter de saturer le CPU
				// si l'erreur est persistante (ex: problème de descripteur de fichier).
				time.Sleep(100 * time.Millisecond)
				continue // Revenir au début de la boucle
			}
		}

		// Si nous arrivons ici, une connexion a été acceptée avec succès.
		log.Printf("INFO: Nouvelle connexion QUIC DQMP acceptée de %s\n", conn.RemoteAddr().String())

		// Lancer une goroutine pour gérer cette connexion
		n.wg.Add(1)                           // Ajouter au waitgroup principal
		go n.handleConnection(conn, true, "") // true car c'est une connexion entrante
	}
}

// handleConnection gère une connexion QUIC (entrante ou sortante)
func (n *Node) handleConnection(conn quic.Connection, isIncoming bool, expectedID peer.ID) {
	defer n.wg.Done()
	remoteAddr := conn.RemoteAddr()
	addrStr := remoteAddr.String()
	// remotePeerID sera déterminé par l'identification
	var remotePeerID peer.ID

	// Utiliser l'adresse comme ID temporaire pour les logs
	logID := addrStr
	defer func() { // Mettre à jour logID si on trouve le PeerID
		finalLogID := addrStr
		if remotePeerID != "" {
			finalLogID = remotePeerID.ShortString()
		}
		log.Printf("CONN: Goroutine pour %s (%s) terminée.\n", finalLogID, addrStr)
	}()

	ctx := conn.Context() // Contexte de la connexion QUIC

	log.Printf("CONN: Gestion de la connexion %s (%s) - ID attendu: %s\n",
		addrStr, map[bool]string{true: "entrante", false: "sortante"}[isIncoming], expectedID.ShortString())

	// --- Lancer l'identification immédiatement ---
	// Utiliser un contexte séparé pour l'identification avec un timeout court
	idCtx, idCancel := context.WithTimeout(ctx, 10*time.Second) // Timeout de 10s pour l'échange d'ID
	identifiedID, err := n.performIdentification(idCtx, conn, isIncoming)
	idCancel() // Libérer le contexte de timeout

	if err != nil {
		log.Printf("CONN: Échec identification avec %s: %v. Fermeture connexion.\n", addrStr, err)
		conn.CloseWithError(3, "Identification échouée") // Code d'erreur applicatif
		// Si on avait un ID attendu, marquer comme échoué ?
		if expectedID != "" {
			n.peerManager.SetPeerDisconnected(expectedID, fmt.Errorf("échec identification: %w", err))
		}
		return // Arrêter le handler si l'identification échoue
	}

	// Identification réussie !
	remotePeerID = identifiedID
	logID = remotePeerID.ShortString() // Mettre à jour l'ID pour les logs suivants
	log.Printf("CONN: Identification réussie avec %s (%s)\n", logID, addrStr)

	// Vérifier si l'ID identifié correspond à l'ID attendu (pour les connexions sortantes)
	if expectedID != "" && expectedID != remotePeerID {
		log.Printf("ERROR: ID identifié (%s) ne correspond pas à l'ID attendu (%s) pour %s! Fermeture.\n",
			logID, expectedID.ShortString(), addrStr)
		conn.CloseWithError(4, "Mismatch PeerID attendu/identifié")
		n.peerManager.SetPeerDisconnected(remotePeerID, errors.New("mismatch PeerID attendu/identifié"))
		// Marquer aussi l'ID attendu comme échoué
		n.peerManager.SetPeerDisconnected(expectedID, errors.New("mismatch PeerID attendu/identifié"))
		return
	}

	// Mettre à jour le PeerManager avec l'ID correct et la connexion active
	_, err = n.peerManager.SetPeerConnected(remotePeerID, conn)
	if err != nil {
		// Normalement, le seul cas d'erreur ici est un conflit d'adresse majeur géré dans SetPeerConnected
		log.Printf("ERROR: Échec mise à jour PeerManager après identification de %s: %v. Fermeture.\n", logID, err)
		conn.CloseWithError(5, "Erreur interne PeerManager post-identification")
		n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("erreur post-identification: %w", err))
		return
	}
	// ------------------------------------------

	// Boucle pour accepter les streams applicatifs (PING, REPLICATE, etc.)
	log.Printf("CONN: Prêt à accepter les streams applicatifs de %s\n", logID)
	for {
		select {
		case <-ctx.Done(): // La connexion est fermée (par nous ou l'autre)
			log.Printf("CONN: Connexion %s (%s) fermée (%v).\n", logID, addrStr, ctx.Err())
			// Mettre à jour le PeerManager pour indiquer la déconnexion
			// Utiliser ctx.Err() comme raison si remotePeerID est connu
			if remotePeerID != "" {
				n.peerManager.SetPeerDisconnected(remotePeerID, ctx.Err())
			}
			return
		case <-n.stopChan: // Le nœud s'arrête
			log.Printf("CONN: Arrêt du nœud demandé, fermeture de la connexion %s.\n", logID)
			conn.CloseWithError(0, "Node shutting down")
			if remotePeerID != "" {
				n.peerManager.SetPeerDisconnected(remotePeerID, errors.New("node shutting down"))
			}
			return
		default:
			// Accepter le prochain stream applicatif
			// Utiliser le contexte de la connexion pour AcceptStream
			stream, err := conn.AcceptStream(ctx)
			if err != nil {
				// Gérer les erreurs d'acceptation de stream (similaire à acceptConnections)
				select {
				case <-ctx.Done(): // Connexion fermée pendant l'attente
					log.Printf("CONN: AcceptStream terminé pour %s car la connexion est fermée.\n", logID)
					return
				default:
					if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) || err == quic.ErrServerClosed {
						log.Printf("CONN: AcceptStream terminé pour %s car la connexion est fermée (err: %v).\n", logID, err)
						return // La connexion a été fermée pendant l'attente
					}
					log.Printf("CONN: Erreur AcceptStream pour %s: %v\n", logID, err)
					// Peut-être juste retourner si l'erreur est grave ?
					// Pour l'instant, on attend un peu et on continue.
					time.Sleep(100 * time.Millisecond)
					continue
				}
			}

			// Vérifier si c'est le stream d'identification (ne devrait plus arriver ici)
			// On pourrait utiliser stream.Protocol() si on le définissait.
			// if stream.Protocol() == DQMPIdentifyProtocolID { stream.Reset(6); continue }

			log.Printf("CONN: Nouveau stream applicatif [%d] accepté de %s\n", stream.StreamID(), logID)
			// Gérer le stream applicatif dans une nouvelle goroutine
			n.wg.Add(1)
			go n.handleStream(conn, stream, remotePeerID) // Passer l'ID identifié
		}
	}
}

// performIdentification effectue l'échange d'ID sur la connexion QUIC DQMP.
// performIdentification effectue l'échange d'ID ET le challenge/response
func (n *Node) performIdentification(ctx context.Context, conn quic.Connection, isIncoming bool) (peer.ID, error) {
	var stream quic.Stream
	var err error
	logPrefix := fmt.Sprintf("IDENTIFY(Challenge): [%s]", conn.RemoteAddr()) // Préfixe pour logs

	// --- 1. Ouvrir/Accepter le Stream ---
	if isIncoming {
		log.Printf("%s Attente du stream d'identification...\n", logPrefix)
		stream, err = conn.AcceptStream(ctx)
		if err != nil {
			return "", fmt.Errorf("échec acceptation stream identification: %w", err)
		}
		log.Printf("%s Stream [%d] accepté.\n", logPrefix, stream.StreamID())
	} else {
		log.Printf("%s Ouverture du stream d'identification...\n", logPrefix)
		stream, err = conn.OpenStreamSync(ctx) // Utiliser ctx ici, pas idCtx
		if err != nil {
			return "", fmt.Errorf("échec ouverture stream identification: %w", err)
		}
		log.Printf("%s Stream [%d] ouvert.\n", logPrefix, stream.StreamID())
	}
	defer stream.Close() // Fermer le stream à la fin

	// --- 2. Échange Initial des PeerIDs (Parallèle) ---
	var remotePID peer.ID
	var wgIdentify sync.WaitGroup
	var sendErr, recvErr error
	idDoneChan := make(chan struct{})

	wgIdentify.Add(2)
	// Goroutine envoi notre ID
	go func() {
		defer wgIdentify.Done()
		// log.Printf("%s Envoi de notre ID (%s)...\n", logPrefix, n.p2pHost.ID().ShortString()) // Log moins verbeux
		encoder := gob.NewEncoder(stream)
		stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err := encoder.Encode(n.p2pHost.ID())
		stream.SetWriteDeadline(time.Time{})
		if err != nil {
			sendErr = fmt.Errorf("échec envoi ID: %w", err)
			stream.CancelWrite(1)
		}
	}()
	// Goroutine réception ID distant
	go func() {
		defer wgIdentify.Done()
		// log.Printf("%s Attente de l'ID distant...\n", logPrefix)
		decoder := gob.NewDecoder(stream)
		stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		err := decoder.Decode(&remotePID)
		stream.SetReadDeadline(time.Time{})
		if err != nil {
			recvErr = fmt.Errorf("échec réception ID: %w", err)
		}
	}()

	// Attendre fin échange ID ou timeout
	go func() { wgIdentify.Wait(); close(idDoneChan) }()
	select {
	case <-idDoneChan:
		if sendErr != nil {
			return "", sendErr
		}
		if recvErr != nil {
			return "", recvErr
		}
		if remotePID == "" {
			return "", errors.New("ID distant reçu est vide")
		}
		if remotePID == n.p2pHost.ID() {
			return "", errors.New("connexion à soi-même détectée")
		}
		log.Printf("%s Échange PeerID terminé. Distant: %s\n", logPrefix, remotePID.ShortString())
	case <-ctx.Done(): // Utiliser le contexte global passé à performIdentification
		log.Printf("%s Timeout/Annulation pendant échange ID.\n", logPrefix)
		stream.CancelRead(7)
		stream.CancelWrite(7)
		return "", fmt.Errorf("timeout/annulation pendant échange ID: %w", ctx.Err())
	}

	// --- 3. Challenge/Response (Authentification Mutuelle) ---
	// On effectue deux passes : A challenge B, puis B challenge A.

	// 3a. Obtenir notre clé privée et la clé publique distante
	ourPrivKey := n.p2pHost.Peerstore().PrivKey(n.p2pHost.ID())
	if ourPrivKey == nil {
		return "", errors.New("impossible de récupérer notre clé privée locale")
	}
	remotePubKey := n.p2pHost.Peerstore().PubKey(remotePID)
	if remotePubKey == nil {
		// On n'a pas encore la clé publique de l'autre (libp2p l'obtient pendant la connexion ou via DHT)
		// Essayons de l'obtenir via le Host (peut bloquer brièvement)
		log.Printf("%s Clé publique pour %s non trouvée localement, tentative d'obtention...", logPrefix, remotePID.ShortString())
		// Utiliser un sous-contexte pour cette opération potentiellement bloquante
		_, pkCancel := context.WithTimeout(ctx, 5*time.Second)
		remotePubKey = n.p2pHost.Peerstore().PubKey(remotePID) // Réessayer après délai? Non, Connect() ou NewStream devrait l'avoir ajoutée.
		if remotePubKey == nil {
			// Tenter une connexion explicite pour forcer l'échange de clés? Risqué.
			// Pour l'instant, si la clé publique n'est pas là, on échoue.
			pkCancel()
			return "", fmt.Errorf("impossible de récupérer la clé publique pour le pair distant %s", remotePID.ShortString())
		}
		pkCancel()
		log.Printf("%s Clé publique pour %s obtenue.\n", logPrefix, remotePID.ShortString())
	}

	// 3b. Challenge -> Signature -> Vérification (2 fois)
	if !isIncoming {
		// Nous (sortant) challengeons l'entrant
		log.Printf("%s [Phase 1/2] Challenge envoyé à %s...\n", logPrefix, remotePID.ShortString())
		err = n.performChallengeResponse(ctx, stream, ourPrivKey, remotePubKey, false) // false = nous challengeons
		if err != nil {
			return "", fmt.Errorf("[Phase 1/2] Échec challenge vers %s: %w", remotePID.ShortString(), err)
		}
		log.Printf("%s [Phase 1/2] Vérification de %s réussie.\n", logPrefix, remotePID.ShortString())

		// Nous (sortant) répondons au challenge de l'entrant
		log.Printf("%s [Phase 2/2] Attente challenge de %s...\n", logPrefix, remotePID.ShortString())
		err = n.performChallengeResponse(ctx, stream, ourPrivKey, remotePubKey, true) // true = nous répondons
		if err != nil {
			return "", fmt.Errorf("[Phase 2/2] Échec réponse challenge de %s: %w", remotePID.ShortString(), err)
		}
		log.Printf("%s [Phase 2/2] Réponse signée envoyée à %s.\n", logPrefix, remotePID.ShortString())
	} else {
		// Nous (entrant) répondons au challenge du sortant
		log.Printf("%s [Phase 1/2] Attente challenge de %s...\n", logPrefix, remotePID.ShortString())
		err = n.performChallengeResponse(ctx, stream, ourPrivKey, remotePubKey, true) // true = nous répondons
		if err != nil {
			return "", fmt.Errorf("[Phase 1/2] Échec réponse challenge de %s: %w", remotePID.ShortString(), err)
		}
		log.Printf("%s [Phase 1/2] Réponse signée envoyée à %s.\n", logPrefix, remotePID.ShortString())

		// Nous (entrant) challengeons le sortant
		log.Printf("%s [Phase 2/2] Challenge envoyé à %s...\n", logPrefix, remotePID.ShortString())
		err = n.performChallengeResponse(ctx, stream, ourPrivKey, remotePubKey, false) // false = nous challengeons
		if err != nil {
			return "", fmt.Errorf("[Phase 2/2] Échec challenge vers %s: %w", remotePID.ShortString(), err)
		}
		log.Printf("%s [Phase 2/2] Vérification de %s réussie.\n", logPrefix, remotePID.ShortString())
	}

	// Si on arrive ici, l'identification complète (ID + Challenge) a réussi
	log.Printf("%s Identification mutuelle avec %s terminée avec succès.\n", logPrefix, remotePID.ShortString())
	return remotePID, nil
}

// performChallengeResponse gère une passe de challenge-réponse-vérification.
// 'respondMode=true' signifie que nous recevons un challenge et envoyons une signature.
// 'respondMode=false' signifie que nous envoyons un challenge et vérifions une signature.
func (n *Node) performChallengeResponse(ctx context.Context, stream quic.Stream,
	ourPrivKey crypto.PrivKey, remotePubKey crypto.PubKey, respondMode bool) error {

	challengeSize := 32
	challenge := make([]byte, challengeSize)
	var signature []byte
	var err error

	// Utiliser gob pour envoyer/recevoir challenge et signature facilement
	encoder := gob.NewEncoder(stream)
	decoder := gob.NewDecoder(stream)

	if respondMode {
		// --- Mode Réponse ---
		// 1. Recevoir le challenge
		stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		err = decoder.Decode(&challenge)
		stream.SetReadDeadline(time.Time{})
		if err != nil {
			return fmt.Errorf("échec réception challenge: %w", err)
		}
		if len(challenge) != challengeSize {
			return fmt.Errorf("taille challenge reçu invalide: %d", len(challenge))
		}

		// 2. Signer le challenge avec notre clé privée
		signature, err = ourPrivKey.Sign(challenge)
		if err != nil {
			return fmt.Errorf("échec signature challenge: %w", err)
		}

		// 3. Envoyer la signature
		stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err = encoder.Encode(signature)
		stream.SetWriteDeadline(time.Time{})
		if err != nil {
			stream.CancelWrite(1)
			return fmt.Errorf("échec envoi signature: %w", err)
		}

	} else {
		// --- Mode Challenge ---
		// 1. Générer un challenge aléatoire
		if _, err := crand.Read(challenge); err != nil {
			return fmt.Errorf("échec génération challenge: %w", err)
		}

		// 2. Envoyer le challenge
		stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err = encoder.Encode(challenge)
		stream.SetWriteDeadline(time.Time{})
		if err != nil {
			stream.CancelWrite(1)
			return fmt.Errorf("échec envoi challenge: %w", err)
		}

		// 3. Recevoir la signature
		stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		err = decoder.Decode(&signature)
		stream.SetReadDeadline(time.Time{})
		if err != nil {
			return fmt.Errorf("échec réception signature: %w", err)
		}
		if len(signature) == 0 {
			return errors.New("signature reçue vide")
		}

		// 4. Vérifier la signature avec la clé publique distante
		ok, err := remotePubKey.Verify(challenge, signature)
		if err != nil {
			return fmt.Errorf("erreur lors de la vérification signature: %w", err)
		}
		if !ok {
			return errors.New("vérification de la signature échouée")
		}
	}

	return nil // Succès pour cette passe
}

// handleStream gère un stream QUIC individuel
func (n *Node) handleStream(conn quic.Connection, stream quic.Stream, remotePeerID peer.ID) {
	defer n.wg.Done()
	defer stream.Close()
	logID := conn.RemoteAddr().String()
	if remotePeerID != "" {
		logID = remotePeerID.ShortString()
	}

	log.Printf("STREAM: Gestion du stream [%d] de %s\n", stream.StreamID(), logID)

	// Lire la première ligne pour déterminer le type de message
	reader := bufio.NewReader(stream)
	// Augmenter légèrement la taille du buffer si les clés/valeurs peuvent être longues
	// reader := bufio.NewReaderSize(stream, 1024)

	// Lire la première ligne (commande ou message)
	// Mettre un timeout de lecture pour éviter de bloquer indéfiniment sur un stream inactif
	stream.SetReadDeadline(time.Now().Add(30 * time.Second)) // Timeout de 30s pour lire la commande

	firstLine, err := reader.ReadString('\n')
	// Réinitialiser le deadline après avoir lu la commande
	stream.SetReadDeadline(time.Time{}) // Pas de deadline pour le reste (ou une autre valeur)

	if err != nil {
		// Gérer les erreurs de lecture (EOF est ok si le stream est fermé proprement)
		if err != io.EOF && !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) {
			// Vérifier si c'est un timeout
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				log.Printf("STREAM: Timeout en attendant la commande sur stream [%d] de %s\n", stream.StreamID(), logID)
			} else {
				log.Printf("STREAM: Erreur lecture commande sur stream [%d] de %s: %v\n", stream.StreamID(), logID, err)
			}
		} else if err == io.EOF {
			log.Printf("STREAM: Stream [%d] fermé par %s avant d'envoyer une commande.\n", stream.StreamID(), logID)
		}
		return
	}
	firstLine = strings.TrimSpace(firstLine)

	// Dispatcher en fonction de la première ligne
	switch {
	case firstLine == pingStreamMsg:
		n.handlePingStream(conn, stream, remotePeerID, logID)
	case strings.HasPrefix(firstLine, replicateStreamPrefix):
		n.handleReplicateStream(conn, stream, remotePeerID, logID, firstLine, reader)
	case firstLine == gossipStreamPrefix:
		n.handleGossipStream(conn, stream, remotePeerID, logID, reader)
	default:
		log.Printf("STREAM: Commande inconnue '%s' reçue sur stream [%d] de %s, fermeture.\n", firstLine, stream.StreamID(), logID)
		// Envoyer une erreur ? Pour l'instant, on ferme juste.
	}
}

// handlePingStream gère un stream PING entrant
func (n *Node) handlePingStream(conn quic.Connection, stream quic.Stream, remotePeerID peer.ID, logID string) {
	log.Printf("STREAM: Réception PING sur stream [%d] de %s, envoi PONG\n", stream.StreamID(), logID)
	// Ajouter un deadline d'écriture
	stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := stream.Write([]byte(pongStreamMsg + "\n"))
	stream.SetWriteDeadline(time.Time{}) // Reset deadline

	if err != nil {
		log.Printf("STREAM: Erreur d'écriture PONG sur stream [%d] de %s: %v\n", stream.StreamID(), logID, err)
		// stream.CancelWrite(1) // Optionnel: annuler l'écriture
		return
	}
	log.Printf("STREAM: PONG envoyé sur stream [%d] de %s\n", stream.StreamID(), logID)
	// Le stream sera fermé par le defer dans handleStream
}

// handleReplicateStream gère un stream de réplication entrant
func (n *Node) handleReplicateStream(conn quic.Connection, stream quic.Stream, remotePeerID peer.ID, logID string, commandLine string, reader *bufio.Reader) {
	log.Printf("STREAM: Réception REPLICATE sur stream [%d] de %s\n", stream.StreamID(), logID)

	// Parser la ligne de commande: REPLICATE <key_len> <key> <value_len>
	// Note: Le format actuel est un peu fragile. Protobuf serait mieux.
	parts := strings.Fields(commandLine)
	if len(parts) != 4 {
		log.Printf("STREAM: Format REPLICATE invalide reçu de %s: '%s'\n", logID, commandLine)
		stream.CancelRead(1)  // NOUVEAU
		stream.CancelWrite(1) // NOUVEAU
		return
	}

	keyLenStr, valueLenStr := parts[1], parts[3]
	key := parts[2] // Récupérer la clé directement

	keyLen, errK := strconv.Atoi(keyLenStr)
	valueLen, errV := strconv.Atoi(valueLenStr)

	if errK != nil || errV != nil || keyLen < 0 || valueLen < 0 || keyLen != len(key) {
		log.Printf("STREAM: Longueurs key/value invalides reçues de %s: keyLen=%s, valueLen=%s, key='%s'\n", logID, keyLenStr, valueLenStr, key)
		stream.CancelRead(1)  // NOUVEAU
		stream.CancelWrite(1) // NOUVEAU
		return
	}

	// Lire la valeur depuis le reste du stream
	valueBytes := make([]byte, valueLen)
	// Définir un deadline de lecture pour la valeur
	stream.SetReadDeadline(time.Now().Add(30 * time.Second)) // Timeout pour lire la valeur
	_, err := io.ReadFull(reader, valueBytes)                // Lire exactement valueLen octets
	stream.SetReadDeadline(time.Time{})                      // Reset deadline

	if err != nil {
		if err == io.ErrUnexpectedEOF {
			log.Printf("STREAM: Fin de stream inattendue en lisant la valeur de réplication de %s (attendu %d octets)\n", logID, valueLen)
		} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			log.Printf("STREAM: Timeout en lisant la valeur de réplication (%d octets) de %s\n", valueLen, logID)
		} else {
			log.Printf("STREAM: Erreur lecture valeur de réplication de %s: %v\n", logID, err)
		}
		stream.CancelRead(1)  // NOUVEAU
		stream.CancelWrite(1) // NOUVEAU
		return
	}

	log.Printf("REPLICATION: Donnée reçue de %s pour clé '%s' (%d octets). Stockage local...\n", logID, key, valueLen)

	// Stocker localement via DataManager
	// Important: Utiliser un contexte séparé ou limiter le temps ici
	// pour ne pas bloquer indéfiniment si BoltDB est lent.
	_, putCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer putCancel()
	// Utiliser Put avec contexte si DataManager le supporte, sinon appel direct.
	err = n.dataManager.Put(key, valueBytes) // Ajouter contexte si possible

	if err != nil {
		log.Printf("REPLICATION: Échec stockage local de la donnée répliquée ('%s') de %s: %v\n", key, logID, err)
		// Envoyer un NACK ? Pour l'instant, on ne fait rien de plus.
		// stream.Reset(2) // Autre code d'erreur ?
		return
	}

	log.Printf("REPLICATION: Donnée répliquée ('%s') de %s stockée localement avec succès.\n", key, logID)

	// Pas de réponse ACK pour l'instant. Le stream sera fermé.
}

// replicateData envoie une paire clé-valeur à un sous-ensemble de pairs connectés.
func (n *Node) ReplicateData(ctx context.Context, key string, value []byte) {
	// 1. Récupérer les pairs connectés éligibles, déjà triés par EcoScore décroissant
	eligiblePeers := n.peerManager.GetPeersByEcoScore()

	if len(eligiblePeers) == 0 {
		log.Println("REPLICATION: Aucun pair connecté éligible trouvé pour la réplication.")
		return
	}

	// 2. Sélectionner les cibles (max MaxReplicas) avec tentative de diversité
	numTargets := 0
	targets := make([]*peers.Peer, 0, MaxReplicas)
	hasHighEcoReplica := false // Flag pour savoir si on a déjà choisi un pair "fiable" (AC/Batterie haute)

	log.Printf("REPLICATION: Sélection des cibles pour '%s' parmi %d pairs éligibles (Max: %d)...\n", key, len(eligiblePeers), MaxReplicas)

	// Parcourir les pairs triés par EcoScore
	for _, p := range eligiblePeers {
		// Limite MaxReplicas atteinte
		if numTargets >= MaxReplicas {
			break
		}

		// Éviter soi-même (ne devrait pas être dans la liste mais par sécurité)
		if p.ID == n.p2pHost.ID() {
			continue
		}

		// Logique de sélection améliorée (simple diversité) :
		// On essaie de prendre au moins un pair avec un score élevé (>0.8 jugé fiable/AC/Batterie haute)
		// Puis on remplit avec les suivants, quel que soit leur score, pour atteindre MaxReplicas.
		isHighEco := p.EcoScore >= 0.80 // Seuil pour considérer comme "fiable"

		if !hasHighEcoReplica && isHighEco {
			// C'est le premier pair "fiable" trouvé, on le prend.
			targets = append(targets, p)
			numTargets++
			hasHighEcoReplica = true
			log.Printf("  - Cible %d (Priorité Haute Fiabilité): %s (EcoScore: %.3f)\n", numTargets, p.ID.ShortString(), p.EcoScore)
		} else if numTargets < MaxReplicas {
			// Si on n'a pas encore atteint MaxReplicas, et que ce n'est pas le premier "fiable"
			// (soit on a déjà un fiable, soit celui-ci n'est pas considéré comme fiable),
			// on le prend pour remplir les slots restants, en suivant l'ordre de score.
			// On pourrait ajouter une logique ici pour éviter de prendre *que* des scores très bas si possible.
			targets = append(targets, p)
			numTargets++
			log.Printf("  - Cible %d (Remplissage par Score): %s (EcoScore: %.3f)\n", numTargets, p.ID.ShortString(), p.EcoScore)
		}
		// Si on a déjà un fiable ET qu'on a atteint MaxReplicas, la boucle s'arrêtera.
		// Si celui-ci n'est pas fiable et qu'on a déjà MaxReplicas, on ne le prend pas.
	}

	// Vérifier si des cibles ont été sélectionnées
	if len(targets) == 0 {
		log.Printf("REPLICATION: Aucune cible valide sélectionnée pour '%s' après filtrage.\n", key)
		return
	}

	log.Printf("REPLICATION: Réplication finale de '%s' vers %d pair(s) initiée.\n", key, len(targets))

	// 3. Envoyer les données aux cibles sélectionnées (en parallèle)
	var wgReplication sync.WaitGroup
	for _, targetPeer := range targets {
		// Lancer une goroutine pour chaque envoi
		wgReplication.Add(1)
		go func(p *peers.Peer, k string, v []byte) {
			defer wgReplication.Done() // S'assurer que le WaitGroup est décrémenté

			logID := p.ID.ShortString()
			if logID == "" && p.DQMPAddr != nil {
				logID = p.DQMPAddr.String()
			}
			if logID == "" {
				logID = "(Inconnu)"
			} // Fallback ultime

			// Vérifier si la connexion est toujours valide juste avant l'envoi
			// (elle aurait pu être fermée entre GetPeersByEcoScore et maintenant)
			if p.Connection == nil || p.Connection.Context().Err() != nil {
				log.Printf("REPLICATION: Connexion à %s (%s) invalide au moment de l'envoi, réplication annulée.\n", logID, k)
				// Mettre à jour l'état ? Non, la fermeture sera détectée par handleConnection.
				return
			}
			conn := p.Connection // Utiliser la connexion stockée

			log.Printf("REPLICATION: Envoi de '%s' vers %s...\n", k, logID)

			// Contexte avec timeout pour cette opération de réplication spécifique
			repCtx, repCancel := context.WithTimeout(ctx, 20*time.Second) // Timeout un peu plus long pour ouverture + écriture
			defer repCancel()

			// Ouvrir un nouveau stream pour cette réplication
			stream, err := conn.OpenStreamSync(repCtx)
			if err != nil {
				log.Printf("REPLICATION: Échec ouverture stream vers %s pour clé '%s': %v\n", logID, k, err)
				// Connexion probablement morte. handleConnection s'en chargera.
				return
			}
			// Fermer le stream proprement à la fin de la goroutine
			// stream.Close() ferme l'écriture et lit jusqu'à EOF. Bon pour signaler la fin.
			// S'il y a une erreur avant, on utilise Cancel.
			defer stream.Close()

			// Construire le message de commande + données
			// Format: REPLICATE <key_len> <key> <value_len>\n<value_bytes>
			command := fmt.Sprintf("%s %d %s %d\n", replicateStreamPrefix, len(k), k, len(v))

			// Écrire la commande puis la valeur
			// Appliquer un deadline d'écriture pour éviter blocage infini
			stream.SetWriteDeadline(time.Now().Add(15 * time.Second))
			_, errCmd := stream.Write([]byte(command))
			var errVal error
			if errCmd == nil {
				_, errVal = stream.Write(v)
			}
			stream.SetWriteDeadline(time.Time{}) // Reset deadline

			// Vérifier les erreurs d'écriture
			if errCmd != nil || errVal != nil {
				err = errCmd // Priorité à l'erreur de commande
				if err == nil {
					err = errVal
				}
				log.Printf("REPLICATION: Échec écriture vers %s pour clé '%s': %v\n", logID, k, err)
				// Annuler explicitement en cas d'erreur pour informer l'autre pair
				stream.CancelWrite(1) // Code d'erreur applicatif 1
				stream.CancelRead(1)  // Annuler aussi la lecture
				return
			}

			log.Printf("REPLICATION: Donnée '%s' envoyée avec succès à %s.\n", k, logID)
			// Pas d'attente de ACK pour l'instant. La fermeture du stream par le defer
			// signale la fin de l'envoi à l'autre pair.

		}(targetPeer, key, value) // Passer les bonnes variables à la goroutine
	}

	// Optionnel: Attendre que toutes les goroutines d'envoi se terminent
	// Cela bloquerait la fonction ReplicateData, ce qui n'est peut-être pas souhaitable
	// si elle est appelée depuis une goroutine API qui doit répondre rapidement.
	// Pour un 'fire and forget', on ne met pas Wait().
	// wgReplication.Wait()
}

// handleGossipStream traite un message gossip entrant.
func (n *Node) handleGossipStream(conn quic.Connection, stream quic.Stream, remotePeerID peer.ID, logID string, reader *bufio.Reader) {
	log.Printf("GOSSIP: Message reçu de %s sur stream [%d]\n", logID, stream.StreamID())
	receivedCount := 0
	for {
		// Appliquer un deadline pour chaque ligne lue
		stream.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := reader.ReadString('\n')
		stream.SetReadDeadline(time.Time{}) // Reset après lecture

		if err != nil {
			if err != io.EOF {
				log.Printf("GOSSIP: Erreur lecture ligne gossip de %s: %v\n", logID, err)
			} else {
				log.Printf("GOSSIP: Fin de stream pendant lecture gossip de %s.\n", logID)
			}
			// Si on n'a pas vu ENDGOSSIP, c'est suspect
			if !strings.Contains(line, gossipEndMarker) { // Vérifier même en cas d'erreur
				log.Printf("WARN: Marqueur ENDGOSSIP manquant dans le message de %s.\n", logID)
			}
			break // Sortir de la boucle en cas d'erreur ou EOF
		}

		line = strings.TrimSpace(line)
		if line == gossipEndMarker {
			log.Printf("GOSSIP: Fin du message de %s, %d entrée(s) traitée(s).\n", logID, receivedCount)
			break // Fin normale
		}
		if line == "" {
			continue
		} // Ignorer lignes vides

		// Parser la ligne: PeerID AddrDQMP
		parts := strings.Fields(line)
		if len(parts) < 1 || len(parts) > 3 {
			log.Printf("GOSSIP: Ligne de pair invalide reçue de %s: '%s'\n", logID, line)
			continue
		}

		receivedIDStr := parts[0]
		receivedDQMPAddrStr := ""
		receivedEcoScoreStr := ""

		if len(parts) >= 2 {
			// Si la 2e partie ressemble à un score, c'est probablement le format "ID Score"
			// Sinon, c'est "ID Addr" ou "ID Addr Score"
			if _, err := strconv.ParseFloat(parts[1], 32); err == nil && len(parts) == 2 {
				receivedEcoScoreStr = parts[1]
			} else {
				receivedDQMPAddrStr = parts[1]
				if len(parts) == 3 {
					receivedEcoScoreStr = parts[2]
				}
			}
		}

		receivedPID, err := peer.Decode(receivedIDStr)
		if err != nil {
			log.Printf("GOSSIP: PeerID invalide reçu de %s: '%s' (%v)\n", logID, receivedIDStr, err)
			continue
		}

		// Traiter l'information reçue
		// Si l'EcoScore est présent, il concerne FORCEMENT l'expéditeur (remotePeerID)
		// car on a décidé que seul un nœud annonce son propre score.
		// MAIS le message peut lister d'autres pairs (sans score).
		var ecoScore float32 = -1.0 // Valeur indiquant "non fourni"
		if receivedPID == remotePeerID && receivedEcoScoreStr != "" {
			score, err := strconv.ParseFloat(receivedEcoScoreStr, 32)
			if err == nil {
				ecoScore = float32(score)
			} else {
				log.Printf("GOSSIP: EcoScore invalide '%s' reçu pour l'expéditeur %s\n", receivedEcoScoreStr, logID)
			}
		}

		// Appeler processGossipInfo avec les infos parsées
		n.processGossipInfo(receivedPID, receivedDQMPAddrStr, ecoScore, remotePeerID) // Passer l'ID de l'expéditeur
		receivedCount++

	}
	// Stream fermé par defer
}

// processGossipInfo traite l'information sur un pair reçue via gossip.
func (n *Node) processGossipInfo(pid peer.ID, dqmpAddrStr string, ecoScore float32, gossipSourceID peer.ID) {
	// Vérifier si on connaît déjà ce pair
	p, exists := n.peerManager.GetPeer(pid)
	if !exists {
		log.Printf("GOSSIP: Découverte nouveau pair %s via gossip (Addr DQMP: %s)\n", pid.ShortString(), dqmpAddrStr)
		// Ajouter comme découvert. On n'a pas ses Multiaddrs libp2p.
		newPeerInfo := peer.AddrInfo{ID: pid} // Adresses vides pour l'instant
		p = n.peerManager.AddOrUpdateDiscoveredPeer(newPeerInfo)
		if p == nil {
			return
		} // C'était nous-même

		// Mettre à jour l'adresse DQMP si fournie et valide
		if dqmpAddrStr != "" {
			addr, err := net.ResolveUDPAddr("udp", dqmpAddrStr)
			if err == nil && p != nil { // Vérifier p != nil car AddOrUpdate peut retourner nil si self
				n.peerManager.SetPeerConnecting(pid, addr) // Marquer comme connectable avec cette adresse
			}
		}

		// Mettre à jour l'EcoScore si c'est l'info de la source elle-même
		if pid == gossipSourceID && ecoScore >= 0.0 {
			n.peerManager.UpdatePeerEcoScore(pid, ecoScore)
		}

	} else {
		if pid == gossipSourceID && ecoScore >= 0.0 {
			n.peerManager.UpdatePeerEcoScore(pid, ecoScore)
		}
		// Pair déjà connu. Mettre à jour l'adresse DQMP si fournie et si on n'a pas déjà une connexion active ?
		if dqmpAddrStr != "" && (p.State == peers.StateDiscovered || p.State == peers.StateFailed) {
			addr, err := net.ResolveUDPAddr("udp", dqmpAddrStr)
			if err == nil {
				// Mettre à jour l'adresse potentielle si on n'est pas connecté/connecting
				if p.DQMPAddr == nil || p.DQMPAddr.String() != addr.String() {
					log.Printf("GOSSIP: Mise à jour addr DQMP pour %s -> %s\n", pid.ShortString(), addr.String())
					n.peerManager.SetPeerConnecting(pid, addr) // Mettre à jour l'adresse connue
				}
			}
		}
		// Mettre à jour LastSeen ?
		// p.LastSeen = time.Now() // Nécessite accès en écriture au PeerManager (mutex)
		// Mettre à jour LastSeen quand on reçoit une info de la source elle-même?
		// if pid == gossipSourceID {
		// 	n.peerManager.UpdatePeerLastSeen(pid) // Méthode à créer, nécessite Lock
		// }
	}
}

// SendPing envoie un PING à un pair et attend un PONG.
// C'est une fonction utilitaire qui pourrait être appelée par le CLI ou d'autres logiques.
func (n *Node) SendPing(ctx context.Context, targetAddr string) error {
	log.Printf("PING: Tentative de PING vers %s\n", targetAddr)

	// Utiliser le PeerManager pour voir si on a déjà une connexion ?
	// Pour ce test simple, on établit une nouvelle connexion à chaque fois.
	// Plus tard, on réutilisera les connexions existantes via PeerManager.

	dialCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	conn, err := transport.Dial(dialCtx, targetAddr)
	if err != nil {
		return fmt.Errorf("échec de connexion pour PING: %w", err)
	}
	// Fermer la connexion à la fin du PING pour ce test simple
	// En pratique, on voudrait garder la connexion ouverte
	defer conn.CloseWithError(0, "Ping completed")

	// Ouvrir un nouveau stream bidirectionnel pour le PING
	streamCtx, streamCancel := context.WithTimeout(ctx, pingTimeout)
	defer streamCancel()
	stream, err := conn.OpenStreamSync(streamCtx) // OpenStreamSync attend que le pair accepte
	if err != nil {
		return fmt.Errorf("échec d'ouverture du stream PING: %w", err)
	}
	defer stream.Close() // Ferme l'écriture, signale la fin à l'autre pair

	log.Printf("PING: Envoi '%s' vers %s sur stream [%d]\n", pingStreamMsg, targetAddr, stream.StreamID())
	_, err = stream.Write([]byte(pingStreamMsg + "\n"))
	if err != nil {
		stream.CancelWrite(1)
		return fmt.Errorf("échec d'écriture PING: %w", err)
	}

	// Lire la réponse PONG
	reader := bufio.NewReader(stream)
	// Définir un deadline de lecture sur le stream pour le timeout global
	if deadline, ok := dialCtx.Deadline(); ok {
		stream.SetReadDeadline(time.Now().Add(pingTimeout - time.Since(deadline))) // Ajuster le délai restant
	}

	response, err := reader.ReadString('\n')
	if err != nil {
		stream.CancelRead(1)
		return fmt.Errorf("échec de lecture PONG: %w", err)
	}

	response = strings.TrimSpace(response)
	log.Printf("PING: Reçu '%s' de %s sur stream [%d]\n", response, targetAddr, stream.StreamID())

	if response != pongStreamMsg {
		return fmt.Errorf("réponse inattendue reçue: '%s', attendu '%s'", response, pongStreamMsg)
	}

	log.Printf("PING: PING vers %s réussi!\n", targetAddr)
	return nil
}

// --- Implémentation de l'interface mdns.Notifee ---
// La fonction HandlePeerFound existe déjà et est correcte.
// Elle sera appelée par le mdnsService lorsqu'un pair est trouvé.
func (n *Node) HandlePeerFound(pi peer.AddrInfo) {
	// Ignorer soi-même
	if pi.ID == n.p2pHost.ID() {
		return
	}
	log.Printf("DISCOVERY(mDNS): Pair trouvé localement: %s (%d addrs)\n", pi.ID.ShortString(), len(pi.Addrs))

	// Ajouter/Mettre à jour le pair découvert dans le manager
	n.peerManager.AddOrUpdateDiscoveredPeer(pi)

	// Tenter la connexion si nécessaire
	p, exists := n.peerManager.GetPeer(pi.ID)
	if exists && (p.State == peers.StateDiscovered || p.State == peers.StateFailed) {
		log.Printf("DISCOVERY(mDNS): Tentative de connexion vers %s (état: %s)\n", p.ID.ShortString(), p.State)
		n.wg.Add(1)
		go n.requestDQMPAddrAndConnect(context.Background(), pi) // Utiliser contexte de fond
	}
}

// Stop arrête proprement le nœud DQMP
func (n *Node) Stop() error {
	log.Println("INFO: Arrêt du nœud DQMP...")

	// 1. Signaler aux goroutines de s'arrêter
	close(n.stopChan)

	// 2. Arrêter le serveur API en premier (pour qu'il ne traite plus de requêtes)
	if n.apiServer != nil {
		// Utiliser un contexte avec timeout pour l'arrêt gracieux
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := n.apiServer.Stop(shutdownCtx); err != nil {
			log.Printf("WARN: Problème lors de l'arrêt du serveur API: %v", err)
		}
	}

	// 3. Fermer DataManager (important de le faire avant la fin)
	if n.dataManager != nil {
		if err := n.dataManager.Close(); err != nil {
			log.Printf("WARN: Erreur lors de la fermeture du DataManager: %v", err)
		}
	}

	// --- Arrêter EnergyWatcher ---
	if n.energyWatcher != nil {
		log.Println("ENERGY: Arrêt du EnergyWatcher...")
		if err := n.energyWatcher.Stop(); err != nil {
			log.Printf("WARN: Erreur lors de l'arrêt du EnergyWatcher: %v", err)
		}
	}

	// 4. Fermer le listener en premier pour ne plus accepter de nouvelles connexions
	if n.listener != nil {
		log.Println("INFO: Fermeture du listener QUIC...")
		err := n.listener.Close()
		if err != nil {
			log.Printf("WARN: Erreur lors de la fermeture du listener QUIC: %v\n", err)
		} else {
			log.Println("INFO: Listener QUIC fermé.")
		}
	}

	// 5. Fermer le DHT Kademlia
	if n.kadDHT != nil {
		log.Println("DISCOVERY: Fermeture du DHT Kademlia...")
		if err := n.kadDHT.Close(); err != nil {
			log.Printf("WARN: Erreur lors de la fermeture du DHT: %v", err)
		} else {
			log.Println("DISCOVERY: DHT fermé.")
		}
	}

	// 6. Fermer le Host libp2p (ferme aussi ses connexions)
	if n.p2pHost != nil {
		log.Println("DISCOVERY: Fermeture du Host libp2p...")
		if err := n.p2pHost.Close(); err != nil {
			log.Printf("WARN: Erreur lors de la fermeture du Host libp2p: %v", err)
		} else {
			log.Println("DISCOVERY: Host libp2p fermé.")
		}
	}

	// 7. Fermer explicitement toutes les connexions actives gérées par le peerManager
	// Note: handleConnection devrait aussi gérer la fermeture via stopChan/ctx.Done()
	// Ceci est une ceinture et bretelles.
	activeConns := n.peerManager.GetActiveConnections()
	log.Printf("INFO: Fermeture de %d connexion(s) active(s)...\n", len(activeConns))
	for _, conn := range activeConns {
		// Le code 0 indique une fermeture normale par l'application
		conn.CloseWithError(0, "Node shutting down")
	}

	// 8. Attendre que toutes les goroutines (accept, bootstrap, handleConnection, handleStream) se terminent
	log.Println("INFO: Attente de la fin des goroutines...")
	// Mettre un timeout sur l'attente pour éviter de bloquer indéfiniment
	waitTimeout := 10 * time.Second
	waitChan := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(waitChan)
	}()
	select {
	case <-waitChan:
		log.Println("INFO: Toutes les goroutines se sont terminées.")
	case <-time.After(waitTimeout):
		log.Printf("WARN: Timeout en attendant les goroutines après %v.\n", waitTimeout)
	}

	log.Println("INFO: Nœud DQMP arrêté.")
	return nil
}

// Run (inchangé)
func (n *Node) Run() error {
	// ...
	if err := n.Start(); err != nil {
		return err
	}
	// ... attendre signal ...
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	receivedSignal := <-sigChan
	log.Printf("INFO: Signal reçu (%v), lancement de l'arrêt...\n", receivedSignal)
	// ...
	return n.Stop()
}
