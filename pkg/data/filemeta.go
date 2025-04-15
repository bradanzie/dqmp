// pkg/data/filemeta.go
package data

import (
	"crypto/sha256"
	"encoding/base32"
	"time"
)

const MetadataKeyPrefix = "_metadata_/" // Préfixe pour les clés de métadonnées

// FileMetadata contient les informations sur un fichier stocké.
type FileMetadata struct {
	Filename     string    `json:"filename"`  // Nom original du fichier
	Filesize     int64     `json:"filesize"`  // Taille totale en octets
	Blocksize    int       `json:"blocksize"` // Taille de shard utilisée (MaxPayloadSize)
	TotalShards  int       `json:"total_shards"`
	Shards       []string  `json:"shards"`        // Liste ordonnée des clés des shards
	ChecksumType string    `json:"checksum_type"` // Ex: "SHA256"
	Checksum     string    `json:"checksum"`      // Checksum global du fichier (hexadécimal)
	UploadTime   time.Time `json:"upload_time"`
}

// Helper pour générer la clé de métadonnées
func GetMetadataKey(filePath string) string {
	// TODO: Nettoyer/Normaliser filePath (ex: enlever / de début)
	return MetadataKeyPrefix + filePath
}

// Helper pour générer une clé de shard (exemple simple, non optimal)
// Préférer un hash du contenu pour la déduplication.
// var shardCounter uint64 // Pas thread-safe ! À améliorer.
// func GenerateShardKey(filename string) string {
//     atomic.AddUint64(&shardCounter, 1)
// 	return fmt.Sprintf("dqmp_shard_%s_%d", filename, shardCounter)
// }

// Utiliser un hash du contenu est mieux
func GenerateShardKey(data []byte) string {
	hash := sha256.Sum256(data)
	// Utiliser seulement une partie du hash ou l'encoder en base64/hex pour la clé?
	// Utilisons l'encodage Base32 pour une clé plus courte et URL-safe
	return "dqmp_shard_" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:16]) // 16 octets = 26 chars Base32
}
