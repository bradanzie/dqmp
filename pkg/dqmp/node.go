// pkg/dqmp/node.go
package dqmp

import (
	"bufio"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"strconv"

	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bradanzie/dqmp/pkg/api"
	"github.com/bradanzie/dqmp/pkg/config"
	"github.com/bradanzie/dqmp/pkg/data"
	"github.com/bradanzie/dqmp/pkg/peers"     // Importer peers
	"github.com/bradanzie/dqmp/pkg/transport" //"github.com/bradanzie/dqmp/pkg/api"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	discovery "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	ma "github.com/multiformats/go-multiaddr"
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

// Node représente une instance DQMP
type Node struct {
	config      *config.Config
	listener    *quic.Listener
	peerManager *peers.Manager // Ajouter le peer manager
	apiServer   *api.Server
	dataManager *data.Manager
	startTime   time.Time
	p2pHost     host.Host    // Le Host libp2p
	kadDHT      *dht.IpfsDHT // Le DHT Kademlia
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

	dm, err := data.NewManager(cfg.Data.Directory)
	if err != nil {
		return nil, fmt.Errorf("échec initialisation DataManager: %w", err)
	}

	// --- Initialisation Libp2p ---
	privKey, err := loadOrCreateIdentity(cfg.Discovery.IdentityPath)
	if err != nil {
		return nil, fmt.Errorf("échec gestion identité libp2p: %w", err)
	}

	// Créer le Host libp2p
	// On configure les adresses d'écoute depuis la config
	// On pourrait ajouter des options (NAT traversal, etc.)
	p2pHost, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(cfg.Discovery.ListenAddrs...),
		libp2p.EnableRelay(),        // Optionnel: pour aider avec NAT
		libp2p.EnableHolePunching(), // Activer le service de Hole Punching (UDP et potentiellement TCP)
		// libp2p.EnableNATService(), // Optionnel
	)
	if err != nil {
		// Fermer le DataManager si l'init libp2p échoue
		dm.Close()
		return nil, fmt.Errorf("échec création Host libp2p: %w", err)
	}
	// Initialiser PeerManager AVEC l'ID du host libp2p local
	pm := peers.NewManager(p2pHost.ID())

	log.Printf("DISCOVERY: Host libp2p créé avec ID: %s\n", p2pHost.ID().String())
	log.Println("DISCOVERY: Écoute libp2p sur:")
	for _, addr := range p2pHost.Addrs() {
		log.Printf("  - %s\n", addr.String())
	}
	// -----------------------------

	node := &Node{
		config:      cfg,
		peerManager: pm,
		dataManager: dm,
		p2pHost:     p2pHost,
		startTime:   startTime,
		stopChan:    make(chan struct{}),
		// apiServer sera défini après
	}

	// Initialiser l'API Server en passant 'node' (qui implémente api.NodeReplicator)
	// où une api.NodeReplicator est attendue.
	node.apiServer = api.NewServer(&cfg.API, pm, dm, node, startTime, NodeVersion)

	// Définir le handler pour l'échange d'adresse DQMP
	node.p2pHost.SetStreamHandler(DQMPAddrProtocolID, node.handleDQMPAddrRequest)

	// Initialiser le DHT Kademlia APRES la création du nœud
	// car le DHT a besoin du Host

	if err := node.initDHT(context.Background()); err != nil {
		// ... (cleanup: node.apiServer.Stop? p2pHost.Close, dm.Close) ...
		// Attention à l'ordre et aux nil checks si l'init échoue à mi-chemin
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

	n.wg.Add(1) // Pour la boucle acceptConnections
	go n.acceptConnections()

	n.wg.Add(1)
	go n.runDiscovery()

	log.Println("INFO: Nœud DQMP démarré avec succès.")
	return nil
}

// runDiscovery gère l'annonce et la recherche périodique de pairs DQMP via le DHT libp2p.
func (n *Node) runDiscovery() {
	defer n.wg.Done()
	routingDiscovery := discovery.NewRoutingDiscovery(n.kadDHT)

	// Annoncer notre présence sous le nom de rendez-vous
	// Il faut relancer l'annonce périodiquement car les annonces expirent dans le DHT
	announceTicker := time.NewTicker(n.config.Discovery.DiscoveryInterval / 2) // Annoncer plus souvent que chercher
	defer announceTicker.Stop()

	// Boucle principale de découverte
	discoveryTicker := time.NewTicker(n.config.Discovery.DiscoveryInterval)
	defer discoveryTicker.Stop()

	log.Printf("DISCOVERY: Lancement de l'annonce et de la découverte DHT (Rendezvous: %s, Intervalle: %v)\n",
		n.config.Discovery.Rendezvous, n.config.Discovery.DiscoveryInterval)

	// Contexte pour les opérations DHT, annulé à l'arrêt
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-n.stopChan
		cancel()
	}()

	// Annonce et découverte initiales
	n.performDHTAdvertise(ctx, routingDiscovery)
	n.performDHTFindPeers(ctx, routingDiscovery)

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
func (n *Node) performDHTAdvertise(ctx context.Context, routingDiscovery *discovery.RoutingDiscovery) {
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
func (n *Node) performDHTFindPeers(ctx context.Context, routingDiscovery *discovery.RoutingDiscovery) {
	log.Printf("DISCOVERY: Recherche de pairs pour '%s'...\n", n.config.Discovery.Rendezvous)
	// peerChan, err := dutil.FindPeers(ctx, routingDiscovery, n.config.Discovery.Rendezvous)
	peerChan, err := routingDiscovery.FindPeers(ctx, n.config.Discovery.Rendezvous)
	if err != nil {
		log.Printf("DISCOVERY: Erreur lors de la recherche DHT: %v\n", err)
		return
	}

	foundCount := 0
	processedPeers := make(map[peer.ID]bool) // Éviter de traiter le même pair plusieurs fois par recherche

	for peerInfo := range peerChan {
		if peerInfo.ID == n.p2pHost.ID() {
			continue
		}
		if processedPeers[peerInfo.ID] {
			continue
		}
		processedPeers[peerInfo.ID] = true

		foundCount++
		log.Printf("DISCOVERY: Pair trouvé via DHT: %s (%d addrs)\n", peerInfo.ID.ShortString(), len(peerInfo.Addrs))

		// Ajouter/Mettre à jour le pair découvert dans le manager
		n.peerManager.AddOrUpdateDiscoveredPeer(peerInfo)

		// Vérifier si on doit tenter une connexion DQMP
		// (On pourrait ajouter une logique pour ne pas retenter immédiatement si StateFailed)
		p, _ := n.peerManager.GetPeer(peerInfo.ID)
		if p != nil && (p.State == peers.StateDiscovered || p.State == peers.StateFailed) {
			// Tentative d'obtention de l'adresse DQMP et connexion
			n.wg.Add(1)
			go n.requestDQMPAddrAndConnect(ctx, peerInfo)
		} else if p != nil {
			// log.Printf("DISCOVERY: Pair %s déjà dans l'état %s, connexion DQMP non tentée.\n", peerInfo.ID.ShortString(), p.State)
		}
	}
	if foundCount == 0 {
		log.Printf("DISCOVERY: Aucun nouveau pair trouvé pour '%s'.\n", n.config.Discovery.Rendezvous)
	}
}

// requestDQMPAddrAndConnect contacte un pair libp2p pour obtenir son adresse DQMP
// et tente ensuite de s'y connecter avec notre stack QUIC DQMP.
func (n *Node) requestDQMPAddrAndConnect(ctx context.Context, peerInfo peer.AddrInfo) {
	defer n.wg.Done() // Assurer que le waitgroup est décrémenté

	remotePeerID := peerInfo.ID
	// Marquer comme Connecting AVANT d'ouvrir le stream libp2p
	// On n'a pas encore l'adresse DQMP, on la mettra à jour plus tard
	_, err := n.peerManager.SetPeerConnecting(remotePeerID, nil)
	if err != nil {
		log.Printf("ADDR_EXCHANGE: Erreur lors du marquage de %s comme Connecting: %v\n", remotePeerID.ShortString(), err)
		// Que faire ? Continuer quand même ? Probablement oui.
	}

	log.Printf("ADDR_EXCHANGE: Tentative d'obtention de l'adresse DQMP de %s\n", remotePeerID.ShortString())
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	stream, err := n.p2pHost.NewStream(reqCtx, remotePeerID, DQMPAddrProtocolID)
	if err != nil {
		log.Printf("ADDR_EXCHANGE: Échec ouverture stream vers %s: %v\n", remotePeerID.ShortString(), err)
		n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("échec ouverture stream addr-exchange: %w", err))
		return
	}
	defer stream.Close()

	// Lire la réponse (l'adresse DQMP)
	reader := bufio.NewReader(stream)
	dqmpAddrStr, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("ADDR_EXCHANGE: Échec lecture réponse de %s: %v\n", remotePeerID.ShortString(), err)
		n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("échec lecture stream addr-exchange: %w", err))
		return
	}

	dqmpAddrStr = strings.TrimSpace(dqmpAddrStr)
	if dqmpAddrStr == "" {
		log.Printf("ADDR_EXCHANGE: Réponse vide reçue de %s.\n", remotePeerID.ShortString())
		return
	}

	log.Printf("ADDR_EXCHANGE: Adresse DQMP reçue de %s: %s\n", remotePeerID.ShortString(), dqmpAddrStr)

	// Résoudre l'adresse DQMP reçue en net.Addr
	// Important pour utiliser comme clé dans l'index secondaire peersByAddr
	udpAddr, err := net.ResolveUDPAddr("udp", dqmpAddrStr)
	if err != nil {
		log.Printf("CONN: Impossible de résoudre l'adresse DQMP '%s' reçue de %s: %v\n", dqmpAddrStr, remotePeerID.ShortString(), err)
		n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("adresse DQMP invalide reçue: %s", dqmpAddrStr))
		return
	}

	// Marquer à nouveau comme Connecting, mais cette fois AVEC l'adresse DQMP
	_, err = n.peerManager.SetPeerConnecting(remotePeerID, udpAddr)
	if err != nil {
		log.Printf("CONN: Erreur lors de la mise à jour de l'adresse DQMP pour %s: %v\n", remotePeerID.ShortString(), err)
		// Continuer quand même ?
	}

	// Maintenant, tenter la connexion QUIC DQMP vers cette adresse
	if n.isSelf(dqmpAddrStr) {
		log.Printf("CONN: Adresse DQMP reçue (%s) est la nôtre, connexion ignorée.\n", dqmpAddrStr)
		return
	}

	// Vérifier si déjà connecté ou en cours de connexion via PeerManager
	if existingPeer, exists := n.peerManager.GetPeerByDQMPAddr(udpAddr.String()); exists {
		if existingPeer.ID == remotePeerID {
			if existingPeer.State == peers.StateConnected {
				log.Printf("CONN: Déjà connecté à %s (%s), connexion DQMP ignorée.\n", remotePeerID.ShortString(), udpAddr.String())
				// Ne pas marquer comme déconnecté ici, la connexion est bonne
				n.peerManager.SetPeerConnected(remotePeerID, existingPeer.Connection) // Juste pour rafraîchir LastSeen/State?
				return
			}
			// Si Connecting, on laisse la tentative actuelle continuer (ou on l'annule?)
		} else {
			// Conflit géré dans SetPeerConnecting/SetPeerConnected
			log.Printf("WARN: L'adresse DQMP %s est déjà associée à %s (différent de %s).", udpAddr.String(), existingPeer.ID.ShortString(), remotePeerID.ShortString())
		}
	}

	log.Printf("CONN: Tentative de connexion DQMP vers %s (%s)\n", remotePeerID.ShortString(), dqmpAddrStr)
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()
	conn, err := transport.Dial(dialCtx, dqmpAddrStr)
	if err != nil {
		log.Printf("CONN: Échec connexion DQMP vers %s (%s): %v\n", remotePeerID.ShortString(), dqmpAddrStr, err)
		n.peerManager.SetPeerDisconnected(remotePeerID, fmt.Errorf("échec connexion DQMP: %w", err))
		return
	}

	log.Printf("CONN: Connexion DQMP établie avec %s (%s)\n", remotePeerID.ShortString(), dqmpAddrStr)

	// Mettre à jour le manager avec la connexion réussie
	// Note: On passe l'ID du pair distant, qui devrait correspondre à celui obtenu via libp2p
	// Il faudrait vérifier/extraire l'ID du certificat TLS de la connexion QUIC pour confirmer ? (sécurité)
	// Pour l'instant, on fait confiance à l'adresse reçue.
	_, err = n.peerManager.SetPeerConnected(remotePeerID, conn)
	if err != nil {
		log.Printf("ERROR: Échec mise à jour PeerManager après connexion DQMP %s: %v\n", remotePeerID.ShortString(), err)
		// Fermer la connexion qu'on vient d'établir ?
		conn.CloseWithError(1, "Erreur interne PeerManager")
		n.peerManager.SetPeerDisconnected(remotePeerID, err) // Assurer état cohérent
		return
	}

	// Gérer la nouvelle connexion DQMP (comme on le faisait avant)
	n.wg.Add(1)
	go n.handleConnection(conn, false, remotePeerID) // false car initiée par nous et Passer l'ID pour l'associer

	// TODO: Associer le PeerID libp2p à ce pair DQMP dans PeerManager
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
func (n *Node) performIdentification(ctx context.Context, conn quic.Connection, isIncoming bool) (peer.ID, error) {
	var stream quic.Stream
	var err error

	// Ouvrir ou Accepter le stream dédié à l'identification.
	// Pour éviter les blocages, un côté ouvre, l'autre accepte.
	// Convention simple: le côté qui a initié la connexion QUIC (isIncoming=false) ouvre le stream 0 ?
	// Ou utiliser OpenStreamSync / AcceptStream avec le contexte.
	// Utilisons OpenStream/AcceptStream pour plus de clarté.

	if isIncoming {
		// Côté entrant : Accepter le stream d'identification initié par l'autre.
		log.Printf("IDENTIFY: [%s] Attente du stream d'identification...\n", conn.RemoteAddr())
		stream, err = conn.AcceptStream(ctx)
		if err != nil {
			return "", fmt.Errorf("échec acceptation stream identification: %w", err)
		}
		log.Printf("IDENTIFY: [%s] Stream d'identification [%d] accepté.\n", conn.RemoteAddr(), stream.StreamID())
		// Vérifier le protocole (si on l'avait défini pour le stream)
		// if stream.Protocol() != DQMPIdentifyProtocolID { stream.Reset(..); return "", errors.New("unexpected protocol") }
	} else {
		// Côté sortant : Ouvrir le stream d'identification.
		log.Printf("IDENTIFY: [%s] Ouverture du stream d'identification...\n", conn.RemoteAddr())
		stream, err = conn.OpenStreamSync(ctx) // Attend l'acceptation par l'autre pair
		if err != nil {
			return "", fmt.Errorf("échec ouverture stream identification: %w", err)
		}
		log.Printf("IDENTIFY: [%s] Stream d'identification [%d] ouvert.\n", conn.RemoteAddr(), stream.StreamID())
		// Définir le protocole (optionnel mais bonne pratique)
		// stream.SetProtocol(DQMPIdentifyProtocolID)
	}
	// S'assurer que le stream est fermé à la fin de cette fonction
	defer stream.Close()

	// Échange d'ID en parallèle (envoyer notre ID, recevoir l'ID de l'autre)
	var remotePID peer.ID
	var wgIdentify sync.WaitGroup
	var sendErr, recvErr error
	doneChan := make(chan struct{}) // Pour signaler la fin des deux goroutines

	wgIdentify.Add(2)

	// Goroutine pour envoyer notre ID
	go func() {
		defer wgIdentify.Done()
		log.Printf("IDENTIFY: [%s] Envoi de notre ID (%s)...\n", conn.RemoteAddr(), n.p2pHost.ID().ShortString())
		encoder := gob.NewEncoder(stream) // Utiliser gob pour simplicité
		// Définir un deadline d'écriture
		stream.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err := encoder.Encode(n.p2pHost.ID())
		stream.SetWriteDeadline(time.Time{}) // Reset deadline
		if err != nil {
			log.Printf("IDENTIFY: [%s] Erreur envoi ID: %v\n", conn.RemoteAddr(), err)
			sendErr = fmt.Errorf("échec envoi ID: %w", err)
			stream.CancelWrite(1) // Annuler l'écriture
		} else {
			log.Printf("IDENTIFY: [%s] Notre ID envoyé.\n", conn.RemoteAddr())
			// Fermer explicitement l'écriture après l'envoi ?
			// stream.Close() // Attention: ferme aussi la lecture! Non.
			// On laisse le defer stream.Close() global gérer.
		}
	}()

	// Goroutine pour recevoir l'ID de l'autre
	go func() {
		defer wgIdentify.Done()
		log.Printf("IDENTIFY: [%s] Attente de l'ID distant...\n", conn.RemoteAddr())
		decoder := gob.NewDecoder(stream) // Utiliser gob
		// Définir un deadline de lecture
		stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		err := decoder.Decode(&remotePID)
		stream.SetReadDeadline(time.Time{}) // Reset deadline
		if err != nil {
			log.Printf("IDENTIFY: [%s] Erreur réception ID: %v\n", conn.RemoteAddr(), err)
			recvErr = fmt.Errorf("échec réception ID: %w", err)
			stream.CancelRead(1) // Annuler la lecture
		} else {
			log.Printf("IDENTIFY: [%s] ID distant reçu: %s\n", conn.RemoteAddr(), remotePID.ShortString())
		}
	}()

	// Attendre la fin des deux goroutines
	go func() {
		wgIdentify.Wait()
		close(doneChan)
	}()

	// Attendre la fin ou le timeout du contexte global d'identification
	select {
	case <-doneChan:
		// Terminé
		if sendErr != nil {
			return "", sendErr
		}
		if recvErr != nil {
			return "", recvErr
		}
		// Vérifier si l'ID reçu est valide
		if remotePID == "" {
			return "", errors.New("ID distant reçu est vide")
		}
		// Vérifier qu'on n'est pas connecté à soi-même
		if remotePID == n.p2pHost.ID() {
			return "", errors.New("connexion à soi-même détectée pendant l'identification")
		}
		return remotePID, nil
	case <-ctx.Done():
		// Timeout ou annulation
		log.Printf("IDENTIFY: [%s] Timeout/Annulation pendant l'échange d'ID.\n", conn.RemoteAddr())
		// S'assurer que le stream est fermé/reset
		stream.Close() // Autre code d'erreur
		return "", fmt.Errorf("timeout/annulation pendant identification: %w", ctx.Err())
	}
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
		stream.CancelRead(quic.StreamErrorCode(1)) // Code d'erreur applicatif
		return
	}

	keyLenStr, valueLenStr := parts[1], parts[3]
	key := parts[2] // Récupérer la clé directement

	keyLen, errK := strconv.Atoi(keyLenStr)
	valueLen, errV := strconv.Atoi(valueLenStr)

	if errK != nil || errV != nil || keyLen < 0 || valueLen < 0 || keyLen != len(key) {
		log.Printf("STREAM: Longueurs key/value invalides reçues de %s: keyLen=%s, valueLen=%s, key='%s'\n", logID, keyLenStr, valueLenStr, key)
		stream.CancelRead(quic.StreamErrorCode(1))
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
		stream.CancelRead(quic.StreamErrorCode(1))
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
	connectedPeers := n.peerManager.GetConnectedPeers()
	if len(connectedPeers) == 0 {
		log.Println("REPLICATION: Aucun pair connecté trouvé pour la réplication.")
		return
	}

	// Sélectionner les cibles (stratégie simple : les N premiers)
	targets := connectedPeers
	if len(targets) > MaxReplicas {
		// TODO: Meilleure sélection (aléatoire, basée sur score énergie/latence...)
		targets = targets[:MaxReplicas]
	}

	log.Printf("REPLICATION: Tentative de réplication de la clé '%s' vers %d pair(s)...\n", key, len(targets))

	var wgReplication sync.WaitGroup
	for _, targetPeer := range targets {
		// Éviter de se répliquer à soi-même (ne devrait pas être dans GetConnectedPeers, mais par sécurité)
		if targetPeer.ID == n.p2pHost.ID() {
			continue
		}
		// Vérifier si la connexion est toujours valide
		if targetPeer.Connection == nil || targetPeer.Connection.Context().Err() != nil {
			log.Printf("REPLICATION: Connexion vers %s invalide, réplication ignorée.\n", targetPeer.ID.ShortString())
			continue
		}

		wgReplication.Add(1)
		go func(p *peers.Peer, k string, v []byte) {
			defer wgReplication.Done()
			logID := p.ID.ShortString()
			if logID == "" {
				logID = p.DQMPAddr.String()
			} // Fallback si ID inconnu

			log.Printf("REPLICATION: Envoi de '%s' vers %s...\n", k, logID)

			// Utiliser la connexion DQMP existante du PeerManager
			conn := p.Connection

			// Ouvrir un nouveau stream pour la réplication
			// Utiliser un contexte avec timeout pour l'ouverture du stream et l'écriture
			repCtx, repCancel := context.WithTimeout(ctx, 15*time.Second) // Timeout global pour l'opération
			defer repCancel()

			stream, err := conn.OpenStreamSync(repCtx) // Attend que le pair accepte
			if err != nil {
				log.Printf("REPLICATION: Échec ouverture stream vers %s: %v\n", logID, err)
				// La connexion est peut-être morte, mettre à jour l'état ?
				// Le handleConnection distant détectera la fermeture et mettra à jour.
				return
			}
			defer stream.Close() // Ferme l'écriture pour signaler la fin

			// Construire le message de commande
			command := fmt.Sprintf("%s %d %s %d\n", replicateStreamPrefix, len(k), k, len(v))

			// Écrire la commande puis la valeur
			stream.SetWriteDeadline(time.Now().Add(10 * time.Second)) // Timeout écriture
			_, err = stream.Write([]byte(command))
			if err == nil {
				_, err = stream.Write(v)
			}
			stream.SetWriteDeadline(time.Time{}) // Reset deadline

			if err != nil {
				log.Printf("REPLICATION: Échec écriture vers %s: %v\n", logID, err)
				// Annuler l'écriture et fermer le stream avec une erreur ?
				stream.CancelWrite(quic.StreamErrorCode(quic.ApplicationErrorCode(1)))
				return
			}

			log.Printf("REPLICATION: Donnée '%s' envoyée avec succès à %s.\n", k, logID)
			// Pas d'attente de ACK pour l'instant.

		}(targetPeer, key, value)
	}

	// Attendre la fin des goroutines de réplication (optionnel, car fire-and-forget)
	// wgReplication.Wait() // Si on veut attendre que les envois soient terminés
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
		// stream.CancelWrite(1)
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
		// stream.CancelRead(1)
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
