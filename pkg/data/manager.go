// pkg/data/manager.go
package data

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	dbFileName    = "dqmp_data.db"
	defaultBucket = "dqmp_shards"
)

// ErrNotFound est retourné quand une clé n'est pas trouvée.
var ErrNotFound = errors.New("clé non trouvée")

// Manager gère le stockage local des DataShards.
type Manager struct {
	db     *bolt.DB
	dbPath string
	mu     sync.RWMutex // Pour protéger l'accès concurrentiel si nécessaire (Bolt gère transactions)
}

// NewManager crée et initialise un nouveau Data Manager.
func NewManager(dataDir string) (*Manager, error) {
	if dataDir == "" {
		dataDir = "." // Répertoire courant par défaut
	}
	// Créer le répertoire s'il n'existe pas
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, fmt.Errorf("impossible de créer le répertoire de données '%s': %w", dataDir, err)
	}

	dbPath := filepath.Join(dataDir, dbFileName)
	log.Printf("DATA: Initialisation du stockage local sur %s\n", dbPath)

	// Ouvrir la base BoltDB
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("impossible d'ouvrir la base de données BoltDB '%s': %w", dbPath, err)
	}

	// S'assurer que le bucket par défaut existe
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(defaultBucket))
		if err != nil {
			return fmt.Errorf("impossible de créer le bucket '%s': %w", defaultBucket, err)
		}
		return nil
	})
	if err != nil {
		db.Close() // Fermer la DB si la création du bucket échoue
		return nil, err
	}

	log.Println("DATA: Stockage local initialisé avec succès.")
	return &Manager{
		db:     db,
		dbPath: dbPath,
	}, nil
}

// Close ferme proprement la base de données BoltDB.
func (m *Manager) Close() error {
	if m.db != nil {
		log.Println("DATA: Fermeture du stockage local...")
		return m.db.Close()
	}
	return nil
}

// Put stocke un payload sous une clé donnée.
// Remplace la valeur si la clé existe déjà.
func (m *Manager) Put(key string, payload []byte) error {
	shard, err := NewDataShard(payload)
	if err != nil {
		return fmt.Errorf("erreur création shard: %w", err)
	}

	shardBytes, err := shard.MarshalBinary()
	if err != nil {
		return fmt.Errorf("erreur sérialisation shard: %w", err)
	}

	err = m.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(defaultBucket))
		if b == nil {
			// Ne devrait pas arriver car on le crée à l'init
			return errors.New("bucket par défaut non trouvé")
		}
		return b.Put([]byte(key), shardBytes)
	})

	if err == nil {
		log.Printf("DATA: Donnée stockée pour la clé '%s' (%d octets payload)\n", key, len(payload))
	} else {
		log.Printf("DATA: Échec stockage clé '%s': %v\n", key, err)
	}
	return err
}

// Get récupère le payload associé à une clé.
// Retourne ErrNotFound si la clé n'existe pas.
func (m *Manager) Get(key string) ([]byte, error) {
	var shardBytes []byte

	err := m.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(defaultBucket))
		if b == nil {
			return errors.New("bucket par défaut non trouvé")
		}
		val := b.Get([]byte(key))
		if val == nil {
			return ErrNotFound // Clé non trouvée
		}
		// Copier la valeur car elle n'est valide que pendant la transaction View
		shardBytes = make([]byte, len(val))
		copy(shardBytes, val)
		return nil
	})

	if err != nil {
		if err != ErrNotFound {
			log.Printf("DATA: Échec lecture clé '%s': %v\n", key, err)
		}
		return nil, err
	}

	// Désérialiser et vérifier le shard
	var shard DataShard
	if err := shard.UnmarshalBinary(shardBytes); err != nil {
		log.Printf("DATA: Échec désérialisation/vérification clé '%s': %v\n", key, err)
		// Que faire ? Retourner une erreur spécifique ? Supprimer la donnée corrompue ?
		// Pour l'instant, on retourne l'erreur.
		return nil, fmt.Errorf("donnée corrompue pour la clé '%s': %w", key, err)
	}

	log.Printf("DATA: Donnée récupérée pour la clé '%s' (%d octets payload)\n", key, len(shard.Payload))
	return shard.Payload, nil
}

// Delete supprime une clé et sa valeur associée.
// Ne retourne pas d'erreur si la clé n'existe pas.
func (m *Manager) Delete(key string) error {
	err := m.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(defaultBucket))
		if b == nil {
			return errors.New("bucket par défaut non trouvé")
		}
		// BoltDB Delete ne retourne pas d'erreur si la clé n'existe pas
		return b.Delete([]byte(key))
	})

	if err == nil {
		log.Printf("DATA: Donnée supprimée pour la clé '%s'\n", key)
	} else {
		log.Printf("DATA: Échec suppression clé '%s': %v\n", key, err)
	}
	return err
}

// ListKeys liste toutes les clés présentes dans le bucket.
// Attention: peut être coûteux sur de grandes bases.
func (m *Manager) ListKeys() ([]string, error) {
	var keys []string
	err := m.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(defaultBucket))
		if b == nil {
			return errors.New("bucket par défaut non trouvé")
		}
		return b.ForEach(func(k, v []byte) error {
			keys = append(keys, string(k))
			return nil // Continue l'itération
		})
	})
	if err != nil {
		log.Printf("DATA: Erreur lors du listage des clés: %v", err)
		return nil, err
	}
	log.Printf("DATA: %d clés listées depuis le stockage.\n", len(keys))
	return keys, nil
}

// TODO: Ajouter des méthodes pour gérer les métadonnées (Merkle, Energy Sig)
// TODO: Gérer la réplication (ERAS) - nécessitera interaction avec PeerManager/Transport
