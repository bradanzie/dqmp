// pkg/data/shard.go
package data

import (
	"bytes"
	"crypto/sha256" // Utiliser SHA256 standard, SHA3 peut être ajouté plus tard
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log"
	"time"
	// "golang.org/x/crypto/sha3" // Pour utiliser SHA3 plus tard si besoin
	// Pour EdDSA, on aura besoin d'une lib crypto, ex: "crypto/ed25519"
)

const (
	MetadataSize   = 16                                            // Taille fixe des métadonnées comme dans la spec
	IntegritySize  = 32                                            // SHA256 checksum pour l'instant
	MaxPayloadSize = 240                                           // Taille max du payload comme dans la spec
	TotalShardSize = MetadataSize + MaxPayloadSize + IntegritySize // Taille totale max

	// TODO: EdDSA signature size (64 bytes) to be added/handled
)

// Metadata contient les informations sur le shard.
// Simplifié pour l'instant (pas de Merkle Root ou Energy Sig).
type Metadata struct {
	Timestamp  int64  // Timestamp en nanosecondes (uint64)
	PayloadLen uint16 // Longueur réelle du payload (< MaxPayloadSize)
	// Reserved pour Merkle Root, Energy Sig, etc.
	Reserved [6]byte // Padding pour atteindre 16 octets (8 + 2 + 6)
}

// DataShard représente l'unité de stockage.
type DataShard struct {
	Meta    Metadata
	Payload []byte // Taille variable jusqu'à MaxPayloadSize
	// Checksum pour vérifier l'intégrité (SHA256(Meta+Payload))
	// Le champ Integrity de la spec est utilisé ici comme checksum.
}

// NewDataShard crée un nouveau shard avec le payload donné.
func NewDataShard(payload []byte) (*DataShard, error) {
	if len(payload) > MaxPayloadSize {
		return nil, errors.New("payload size exceeds maximum limit")
	}

	shard := &DataShard{
		Meta: Metadata{
			Timestamp:  time.Now().UnixNano(),
			PayloadLen: uint16(len(payload)),
		},
		Payload: make([]byte, len(payload)),
	}
	copy(shard.Payload, payload) // Copier le payload

	return shard, nil
}

// calculateChecksum calcule le checksum SHA256 sur les métadonnées et le payload.
func (ds *DataShard) calculateChecksum() ([IntegritySize]byte, error) {
	metaBytes, err := ds.Meta.MarshalBinary()
	if err != nil {
		return [IntegritySize]byte{}, err
	}
	// Concaténer Meta et Payload
	dataToHash := append(metaBytes, ds.Payload...)
	return sha256.Sum256(dataToHash), nil
}

// MarshalBinary sérialise le DataShard complet (Meta + Payload + Checksum) en []byte.
func (ds *DataShard) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)

	// 1. Sérialiser les métadonnées
	metaBytes, err := ds.Meta.MarshalBinary()
	if err != nil {
		return nil, err
	}
	buf.Write(metaBytes)

	// 2. Écrire le payload (taille réelle)
	buf.Write(ds.Payload)

	// 3. Calculer et ajouter le checksum
	checksum, err := ds.calculateChecksum()
	if err != nil {
		return nil, err
	}
	buf.Write(checksum[:])

	// Vérifier la taille totale (optionnel)
	// totalLen := len(metaBytes) + len(ds.Payload) + IntegritySize
	// if buf.Len() != totalLen {
	//     return nil, fmt.Errorf("taille marshalée inattendue: %d vs %d", buf.Len(), totalLen)
	// }

	return buf.Bytes(), nil
}

// UnmarshalBinary désérialise un []byte en DataShard et vérifie l'intégrité.
func (ds *DataShard) UnmarshalBinary(data []byte) error {
	if len(data) < MetadataSize+IntegritySize {
		return errors.New("données trop courtes pour être un DataShard valide (manque meta/checksum)")
	}

	buf := bytes.NewReader(data)

	// 1. Lire et désérialiser les métadonnées
	metaBytes := make([]byte, MetadataSize)
	if _, err := io.ReadFull(buf, metaBytes); err != nil {
		return fmt.Errorf("erreur lecture métadonnées: %w", err)
	}
	if err := ds.Meta.UnmarshalBinary(metaBytes); err != nil {
		return fmt.Errorf("erreur unmarshal métadonnées: %w", err)
	}

	// 2. Déterminer la taille du payload et lire le payload
	payloadLen := int(ds.Meta.PayloadLen)
	if payloadLen > MaxPayloadSize {
		return fmt.Errorf("longueur payload invalide dans métadonnées: %d", payloadLen)
	}
	// Vérifier si la taille restante est suffisante
	expectedRemaining := payloadLen + IntegritySize
	if buf.Len() < expectedRemaining {
		return fmt.Errorf("données insuffisantes pour payload (%d) et checksum (%d), reste: %d", payloadLen, IntegritySize, buf.Len())
	}

	ds.Payload = make([]byte, payloadLen)
	if _, err := io.ReadFull(buf, ds.Payload); err != nil {
		return fmt.Errorf("erreur lecture payload: %w", err)
	}

	// 3. Lire le checksum fourni
	providedChecksum := make([]byte, IntegritySize)
	if _, err := io.ReadFull(buf, providedChecksum); err != nil {
		return fmt.Errorf("erreur lecture checksum: %w", err)
	}

	// 4. Calculer le checksum attendu et comparer
	expectedChecksum, err := ds.calculateChecksum()
	if err != nil {
		return fmt.Errorf("erreur calcul checksum attendu: %w", err)
	}

	if !bytes.Equal(providedChecksum, expectedChecksum[:]) {
		return errors.New("vérification du checksum échouée")
	}

	// Vérifier s'il reste des données (ne devrait pas si le format est respecté)
	if buf.Len() > 0 {
		log.Printf("WARN: Données supplémentaires (%d octets) après désérialisation du shard.\n", buf.Len())
	}

	return nil
}

// --- Méthodes pour Metadata ---

// MarshalBinary sérialise Metadata en []byte (taille fixe MetadataSize).
func (m *Metadata) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, m.Timestamp); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, m.PayloadLen); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, m.Reserved); err != nil {
		return nil, err
	}

	if buf.Len() != MetadataSize {
		return nil, fmt.Errorf("taille metadata marshalée inattendue: %d vs %d", buf.Len(), MetadataSize)
	}
	return buf.Bytes(), nil
}

// UnmarshalBinary désérialise []byte en Metadata.
func (m *Metadata) UnmarshalBinary(data []byte) error {
	if len(data) != MetadataSize {
		return errors.New("taille incorrecte pour les métadonnées")
	}
	buf := bytes.NewReader(data)
	if err := binary.Read(buf, binary.LittleEndian, &m.Timestamp); err != nil {
		return err
	}
	if err := binary.Read(buf, binary.LittleEndian, &m.PayloadLen); err != nil {
		return err
	}
	if err := binary.Read(buf, binary.LittleEndian, &m.Reserved); err != nil {
		return err
	}
	return nil
}

// Helper pour l'import/export rapide (à mettre ailleurs plus tard)
func marshal(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func unmarshal(data []byte, v interface{}) error {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	return dec.Decode(v)
}
