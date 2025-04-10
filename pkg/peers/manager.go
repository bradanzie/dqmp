// pkg/peers/manager.go
package peers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
)

// PeerState représente l'état de connectivité DQMP avec un pair.
type PeerState string

const (
	StateDiscovered PeerState = "Discovered" // Connu via DHT/Bootstrap, pas de connexion DQMP
	StateConnecting PeerState = "Connecting" // Tentative de connexion DQMP en cours
	StateConnected  PeerState = "Connected"  // Connexion DQMP active
	StateFailed     PeerState = "Failed"     // Tentative de connexion DQMP échouée récemment
	StateRemoved    PeerState = "Removed"    // Marqué pour suppression (optionnel)
)

// Peer représente un nœud distant connu.
type Peer struct {
	ID         peer.ID         // Identifiant unique Libp2p (clé primaire)
	Multiaddrs []ma.Multiaddr  // Adresses Libp2p connues
	DQMPAddr   net.Addr        // Adresse QUIC DQMP (peut être nil)
	Connection quic.Connection // Connexion QUIC DQMP active (peut être nil)
	State      PeerState       // État actuel de la connexion DQMP
	LastSeen   time.Time       // Dernière fois qu'on a eu une interaction/découverte
	LastError  error           // Dernière erreur de connexion (optionnel)
	// TODO: Ajouter Score Énergétique, Latence, etc.
}

// Manager gère l'ensemble des pairs connus et actifs.
type Manager struct {
	peers       map[peer.ID]*Peer // Clé: PeerID libp2p
	peersByAddr map[string]*Peer  // Index secondaire par Addr.String() de la connexion DQMP
	mu          sync.RWMutex
	selfID      peer.ID // ID du nœud local pour éviter d'ajouter soi-même
}

// NewManager crée un nouveau gestionnaire de pairs.
func NewManager(selfID peer.ID) *Manager {
	return &Manager{
		peers:       make(map[peer.ID]*Peer),
		peersByAddr: make(map[string]*Peer),
		selfID:      selfID,
	}
}

// AddOrUpdateDiscoveredPeer ajoute ou met à jour un pair découvert (ex: via DHT).
// Met à jour les Multiaddrs et le LastSeen. Ne crée pas de connexion DQMP.
func (m *Manager) AddOrUpdateDiscoveredPeer(info peer.AddrInfo) *Peer {
	// Ignorer soi-même
	if info.ID == m.selfID {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	peer, exists := m.peers[info.ID]
	now := time.Now()

	if !exists {
		peer = &Peer{
			ID:         info.ID,
			Multiaddrs: info.Addrs,
			State:      StateDiscovered,
			LastSeen:   now,
		}
		m.peers[info.ID] = peer
		log.Printf("PEERS: Nouveau pair découvert (via Discovery): %s\n", info.ID.ShortString())
	} else {
		// Mettre à jour les Multiaddrs et LastSeen
		// TODO: Logique de fusion plus intelligente des adresses ?
		peer.Multiaddrs = info.Addrs // Remplacer pour l'instant
		peer.LastSeen = now
		// Ne pas changer l'état s'il est déjà Connecting ou Connected
		if peer.State == StateFailed || peer.State == StateRemoved {
			peer.State = StateDiscovered // Peut être redécouvert
			peer.LastError = nil
		}
		// log.Printf("PEERS: Pair découvert mis à jour: %s\n", info.ID.ShortString())
	}

	return peer
}

// SetPeerConnecting marque un pair comme étant en cours de connexion DQMP.
func (m *Manager) SetPeerConnecting(id peer.ID, dqmpAddr net.Addr) (*Peer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	peer, exists := m.peers[id]
	if !exists {
		// Ne devrait pas arriver si découvert d'abord, mais gérons le cas
		peer = &Peer{ID: id, State: StateDiscovered, LastSeen: time.Now()}
		m.peers[id] = peer
	}

	// Vérifier si une connexion est déjà active ou en cours pour cette adresse DQMP
	if dqmpAddr != nil {
		addrStr := dqmpAddr.String()
		if existingPeer, ok := m.peersByAddr[addrStr]; ok && existingPeer.ID != id {
			// Conflit: une autre PeerID est déjà associée à cette adresse DQMP !
			log.Printf("WARN: Tentative de connexion à %s (%s) alors que déjà associé à %s",
				addrStr, id.ShortString(), existingPeer.ID.ShortString())
			// return nil, fmt.Errorf("l'adresse DQMP %s est déjà utilisée par le pair %s", addrStr, existingPeer.ID.ShortString())
			// Pour l'instant, on permet, mais c'est suspect.
		}
	}

	// Ne mettre à jour que si on n'est pas déjà connecté
	if peer.State != StateConnected {
		peer.State = StateConnecting
		peer.DQMPAddr = dqmpAddr // Mémoriser l'adresse cible
		peer.LastSeen = time.Now()
		peer.LastError = nil // Reset de la dernière erreur
		log.Printf("PEERS: Pair %s marqué comme Connecting (DQMP Addr: %s)\n", id.ShortString(), dqmpAddr)
	} else {
		log.Printf("PEERS: Tentative de marquer %s comme Connecting alors qu'il est déjà Connected.\n", id.ShortString())
	}

	return peer, nil
}

// SetPeerConnected met à jour un pair lorsqu'une connexion DQMP est établie.
// Nécessite l'ID du pair et la connexion QUIC active.
func (m *Manager) SetPeerConnected(id peer.ID, conn quic.Connection) (*Peer, error) {
	// ... (check conn nil) ...
	dqmpAddr := conn.RemoteAddr()
	addrStr := dqmpAddr.String()
	m.mu.Lock()
	defer m.mu.Unlock()
	peer, exists := m.peers[id]
	if !exists {
		// Connexion entrante de pair inconnu ? Ou ID fourni incorrect ?
		// Si l'ID est vide "", c'est probablement une connexion entrante non identifiée.
		if id == "" {
			log.Printf("WARN: SetPeerConnected appelé avec ID vide pour connexion %s. Impossible de stocker par ID.", addrStr)
			// Que faire ? Créer une entrée temporaire basée sur l'adresse ?
			// Ou simplement ne pas l'ajouter à la map m.peers ?
			// Pour l'instant, ne l'ajoutons pas à m.peers si l'ID est vide.
			// Mais faut-il l'ajouter à m.peersByAddr ? Peut-être pas non plus sans ID.
			return nil, fmt.Errorf("impossible d'associer une connexion sans PeerID")
		}
		// Si l'ID n'est pas vide mais n'existe pas, on le crée.
		log.Printf("PEERS: Connexion DQMP établie avec un pair précédemment inconnu ou non connecté %s (ID: %s)\n", addrStr, id.ShortString())
		peer = &Peer{ID: id}
		m.peers[id] = peer
	}
	// ... (gestion conflit adresse) ...
	peer.Connection = conn
	peer.DQMPAddr = dqmpAddr
	peer.State = StateConnected
	peer.LastSeen = time.Now()
	peer.LastError = nil
	m.peersByAddr[addrStr] = peer // Ajouter à l'index par adresse
	log.Printf("PEERS: Pair %s marqué comme Connected (DQMP Addr: %s)\n", id.ShortString(), addrStr)
	return peer, nil
}

// SetPeerDisconnected met à jour l'état d'un pair après une déconnexion DQMP ou échec.
func (m *Manager) SetPeerDisconnected(id peer.ID, reason error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	peer, exists := m.peers[id]
	if !exists {
		return // Pair non connu, rien à faire
	}

	log.Printf("PEERS: Déconnexion/Échec pour le pair %s. Raison: %v\n", id.ShortString(), reason)

	// Supprimer de l'index par adresse si l'adresse est connue
	if peer.DQMPAddr != nil {
		delete(m.peersByAddr, peer.DQMPAddr.String())
	}

	// Mettre à jour l'état
	peer.Connection = nil
	peer.DQMPAddr = nil // On ne sait plus où le joindre via DQMP
	peer.LastError = reason
	if reason != nil && !errors.Is(reason, context.Canceled) { // Ne pas marquer comme Failed si c'est un arrêt normal
		peer.State = StateFailed
	} else {
		peer.State = StateDiscovered // Retour à l'état découvert après déconnexion normale
	}
	peer.LastSeen = time.Now()
}

// GetPeer récupère un pair par son PeerID.
func (m *Manager) GetPeer(id peer.ID) (*Peer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	peer, exists := m.peers[id]
	return peer, exists
}

// GetPeerByDQMPAddr récupère un pair par son adresse de connexion DQMP.
func (m *Manager) GetPeerByDQMPAddr(addrStr string) (*Peer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	peer, exists := m.peersByAddr[addrStr]
	return peer, exists
}

// RemovePeer supprime un pair du manager (moins utile maintenant qu'on a des états).
// On pourrait préférer marquer comme StateRemoved et nettoyer périodiquement.
func (m *Manager) RemovePeer(id peer.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	peer, exists := m.peers[id]
	if exists {
		// Supprimer de l'index par adresse
		if peer.DQMPAddr != nil {
			delete(m.peersByAddr, peer.DQMPAddr.String())
		}
		// Supprimer de la map principale
		delete(m.peers, id)
		log.Printf("PEERS: Pair %s supprimé du manager.\n", id.ShortString())
	}
}

// GetAllPeers renvoie une copie de la liste de tous les pairs connus.
func (m *Manager) GetAllPeers() []*Peer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Peer, 0, len(m.peers))
	for _, peer := range m.peers {
		list = append(list, peer)
	}
	return list
}

// GetPeerByDQMPAddr récupère un pair par son adresse de connexion DQMP.
func (m *Manager) GetPeerByAddr(addrStr string) (*Peer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	peer, exists := m.peersByAddr[addrStr]
	return peer, exists
}

// GetActiveConnections renvoie les connexions QUIC actives.
func (m *Manager) GetActiveConnections() []quic.Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conns := make([]quic.Connection, 0, len(m.peers))
	for _, peer := range m.peers {
		if peer.Connection != nil {
			// Vérifier si la connexion est toujours active (le contexte n'est pas terminé)
			// Note: quic-go ne fournit pas de méthode IsClosed() simple.
			// On pourrait vérifier conn.Context().Err() != nil, mais c'est implicite.
			// Pour l'instant, on suppose qu'une connexion non-nil est potentiellement active.
			// Une meilleure gestion de l'état serait nécessaire (ex: écouter Context().Done()).
			conns = append(conns, peer.Connection)
		}
	}
	return conns
}

// GetConnectedPeers renvoie la liste des pairs avec une connexion DQMP active.
func (m *Manager) GetConnectedPeers() []*Peer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Peer, 0, len(m.peers))
	for _, peer := range m.peers {
		if peer.State == StateConnected && peer.Connection != nil {
			// Re-vérifier si la connexion est vraiment active ?
			// Le contexte de la connexion est le meilleur indicateur.
			// if peer.Connection.Context().Err() == nil {
			list = append(list, peer)
			// }
		}
	}
	return list
}
