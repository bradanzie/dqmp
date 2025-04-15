// pkg/api/server.go
package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	// Importer les packages nécessaires du projet
	"github.com/bradanzie/dqmp/pkg/config" // Adaptez
	"github.com/bradanzie/dqmp/pkg/data"
	"github.com/bradanzie/dqmp/pkg/energy"
	"github.com/bradanzie/dqmp/pkg/peers" // Adaptez
	// "github.com/votre-utilisateur/dqmp-project/pkg/dqmp" // On aura besoin d'une réf au Node plus tard
)

type NodeReplicator interface {
	ReplicateData(ctx context.Context, key string, value []byte)
	// Ajouter d'autres méthodes si l'API a besoin d'autres interactions avec le nœud
}

// Server encapsule le serveur HTTP de l'API REST.
type Server struct {
	config        *config.APIConfig
	peerManager   *peers.Manager // Référence au gestionnaire de pairs
	energyWatcher energy.Watcher
	dataManager   *data.Manager
	replicator    NodeReplicator
	startTime     time.Time // Heure de démarrage du nœud
	nodeVersion   string    // Version du nœud (on pourrait la passer)
	httpServer    *http.Server
}

// NewServer crée une nouvelle instance du serveur API.
// On passe les dépendances nécessaires (config, peer manager, etc.).
func NewServer(
	cfg *config.APIConfig,
	pm *peers.Manager,
	dm *data.Manager,
	watcher energy.Watcher,
	replicator NodeReplicator, // Prend l'interface
	startTime time.Time,
	version string,
) *Server {
	if !cfg.Enabled {
		return nil
	}
	return &Server{
		config:        cfg,
		peerManager:   pm,
		dataManager:   dm,
		energyWatcher: watcher,
		replicator:    replicator, // Stocker l'interface
		startTime:     startTime,
		nodeVersion:   version,
	}
}

// Start lance le serveur HTTP dans une goroutine.
func (s *Server) Start() error {
	if s == nil {
		log.Println("API: Serveur désactivé, ne démarre pas.")
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/peers", s.handlePeers) // Enregistre le handler handlePeers
	mux.HandleFunc("/data/", s.handleData)
	mux.HandleFunc("/energy/status", s.handleEnergyStatus)
	mux.HandleFunc("/files/upload", s.handleFileUpload)     // POST
	mux.HandleFunc("/files/download", s.handleFileDownload) // GET
	mux.HandleFunc("/files/list", s.handleFileList)         // GET (Optionnel)
	mux.HandleFunc("/files/delete", s.handleFileDelete)     // DELETE (Optionnel)

	s.httpServer = &http.Server{
		Addr:         s.config.Listen,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("API: Démarrage du serveur HTTP sur %s\n", s.config.Listen)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API: Échec du démarrage du serveur HTTP: %v\n", err)
		}
		log.Println("API: Serveur HTTP arrêté.")
	}()
	return nil
}

// Stop arrête proprement le serveur HTTP.
func (s *Server) Stop(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	log.Println("API: Arrêt du serveur HTTP...")
	err := s.httpServer.Shutdown(ctx)
	if err != nil {
		log.Printf("API: Erreur lors de l'arrêt gracieux du serveur HTTP: %v\n", err)
	} else {
		log.Println("API: Serveur HTTP arrêté proprement.")
	}
	return err
}

// --- Handlers ---

// handleStatus renvoie des informations générales sur le nœud.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { /* ... */
		return
	}
	status := struct {
		Version      string    `json:"version"`
		StartTime    time.Time `json:"start_time"`
		Uptime       string    `json:"uptime"`
		GoVersion    string    `json:"go_version"`
		NumCPU       int       `json:"num_cpu"`
		NumGoroutine int       `json:"num_goroutine"`
		PeerCount    int       `json:"peer_count"`
	}{
		Version:      s.nodeVersion,
		StartTime:    s.startTime,
		Uptime:       time.Since(s.startTime).Round(time.Second).String(),
		GoVersion:    runtime.Version(),
		NumCPU:       runtime.NumCPU(),
		NumGoroutine: runtime.NumGoroutine(),
		PeerCount:    len(s.peerManager.GetAllPeers()),
	}
	writeJSONResponse(w, http.StatusOK, status)
}

// handlePeers renvoie la liste des pairs connus par le PeerManager.
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	// 1. Vérifier Méthode HTTP
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Println("API: DÉBUT requête /peers") // Log Entrée

	// 2. Récupérer les pairs depuis le Manager
	allPeers := s.peerManager.GetAllPeers()
	log.Printf("API: PeerManager retourné %d pairs", len(allPeers)) // Log Nombre

	// 3. Définir la structure de la réponse JSON
	type PeerInfoAPI struct {
		PeerID      string    `json:"peer_id"`
		State       string    `json:"state"`
		DQMPAddress string    `json:"dqmp_address,omitempty"`
		Multiaddrs  []string  `json:"multiaddrs,omitempty"`
		EcoScore    float32   `json:"eco_score"`
		LastSeen    time.Time `json:"last_seen"`
		LastError   string    `json:"last_error,omitempty"`
	}

	// 4. Préparer la slice de réponse
	response := make([]PeerInfoAPI, 0, len(allPeers)) // Initialiser avec capacité

	// 5. Boucler sur les pairs récupérés
	for i, p := range allPeers {
		// 5a. Log de debug CRUCIAL pour chaque pair
		log.Printf("API: Traitement Peer[%d]: Ptr=%p, EstNil=%t", i, p, p == nil)
		if p == nil {
			log.Printf("API: Peer[%d] est NIL, skip.", i)
			continue // Ignorer les pointeurs nil (ne devrait pas arriver)
		}
		// Afficher les champs bruts *avant* formatage pour voir leur état
		log.Printf("API: Peer[%d] Contenu BRUT: ID='%s', State='%s', DQMPAddr=%v, Connection=%t, LastError=%v, MultiaddrsLen=%d, LastSeen=%v",
			i, p.ID, p.State, p.DQMPAddr, p.Connection != nil, p.LastError, len(p.Multiaddrs), p.LastSeen)

		// 5b. Préparer les champs pour la réponse JSON (avec gestion des nils/vides)
		peerIDStr := "(Inconnu)" // Défaut si l'ID est vide
		if p.ID != "" {
			peerIDStr = p.ID.String()
		}

		maddrsStr := []string{} // Toujours initialiser (jamais nil)
		if p.Multiaddrs != nil {
			maddrsStr = make([]string, 0, len(p.Multiaddrs))
			for _, ma := range p.Multiaddrs {
				if ma != nil { // Vérifier que l'adresse elle-même n'est pas nil
					maddrsStr = append(maddrsStr, ma.String())
				}
			}
		}

		dqmpAddrStr := ""
		if p.DQMPAddr != nil {
			dqmpAddrStr = p.DQMPAddr.String()
		}

		errStr := ""
		if p.LastError != nil {
			errStr = p.LastError.Error()
		}

		// 5c. Ajouter l'élément formaté à la réponse
		response = append(response, PeerInfoAPI{
			PeerID:      peerIDStr,
			State:       string(p.State), // Le type string ne peut pas être nil
			DQMPAddress: dqmpAddrStr,
			Multiaddrs:  maddrsStr,
			EcoScore:    p.EcoScore,
			LastSeen:    p.LastSeen, // time.Time n'est pas un pointeur
			LastError:   errStr,
		})
	} // Fin de la boucle for

	// 6. Envoyer la réponse JSON
	log.Printf("API: FIN requête /peers. Envoi de %d enregistrements.", len(response)) // Log Sortie
	writeJSONResponse(w, http.StatusOK, response)
}

// handleData gère les requêtes sur les données (GET, PUT, DELETE).
// On pourrait ajouter un préfixe comme /dqmp/v1/data/ pour versionner l'API.
// On pourrait aussi utiliser un routeur comme gorilla/mux pour plus de flexibilité.
// On pourrait aussi ajouter un middleware pour la gestion des erreurs.
func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/data/")
	if key == "" {
		if r.Method == http.MethodGet {
			s.handleListKeys(w, r)
			return
		}
		http.Error(w, "Clé manquante dans l'URL (/data/{key})", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleGetData(w, r, key)
	case http.MethodPut:
		s.handlePutData(w, r, key)
	case http.MethodDelete:
		s.handleDeleteData(w, r, key)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetData gère les requêtes GET pour récupérer des données par clé.
func (s *Server) handleGetData(w http.ResponseWriter, r *http.Request, key string) {
	payload, err := s.dataManager.Get(key)
	if err != nil {
		if errors.Is(err, data.ErrNotFound) {
			http.Error(w, "Clé non trouvée", http.StatusNotFound)
		} else {
			log.Printf("API: Erreur interne Get data '%s': %v", key, err)
			http.Error(w, "Erreur interne du serveur", http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		log.Printf("API: Erreur lors de l'écriture de la réponse GET pour '%s': %v", key, err)
	}
}

// handlePutData gère les requêtes PUT pour stocker des données par clé.
func (s *Server) handlePutData(w http.ResponseWriter, r *http.Request, key string) {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Erreur lecture du corps de la requête", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	if len(payload) > data.MaxPayloadSize {
		http.Error(w, fmt.Sprintf("La taille du payload (%d) dépasse la limite (%d)", len(payload), data.MaxPayloadSize), http.StatusRequestEntityTooLarge)
		return
	}

	err = s.dataManager.Put(key, payload)
	if err != nil {
		log.Printf("API: Erreur interne Put data '%s': %v", key, err)
		http.Error(w, "Erreur interne du serveur lors du stockage", http.StatusInternalServerError)
		return
	}
	log.Printf("API: Donnée '%s' stockée localement.\n", key)

	if s.replicator != nil {
		go s.replicator.ReplicateData(context.Background(), key, payload)
	} else {
		log.Println("WARN: Replicator non défini, impossible de lancer la réplication.")
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteData gère les requêtes DELETE pour supprimer des données par clé.
func (s *Server) handleDeleteData(w http.ResponseWriter, r *http.Request, key string) {
	err := s.dataManager.Delete(key)
	if err != nil {
		log.Printf("API: Erreur interne Delete data '%s': %v", key, err)
		http.Error(w, "Erreur interne du serveur lors de la suppression", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListKeys gère les requêtes GET pour lister toutes les clés.
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.dataManager.ListKeys()
	if err != nil {
		log.Printf("API: Erreur interne List keys: %v", err)
		http.Error(w, "Erreur interne du serveur", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, keys)
}

// handleEnergyStatus renvoie l'état énergétique actuel du nœud.
func (s *Server) handleEnergyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.energyWatcher == nil {
		log.Println("API: EnergyWatcher non disponible pour /energy/status")
		http.Error(w, "Service de surveillance énergétique non disponible", http.StatusServiceUnavailable)
		return
	}

	status, err := s.energyWatcher.GetCurrentStatus()
	if err != nil {
		log.Printf("API: Erreur lors de la récupération du statut énergétique: %v", err)
		http.Error(w, "Erreur interne du serveur", http.StatusInternalServerError)
		return
	}

	// On pourrait aussi ajouter le profil matériel à la réponse
	// profile := s.energyWatcher.GetHardwareProfile()
	// combinedResp := struct { ... } { Status: status, Profile: profile }
	// writeJSONResponse(w, http.StatusOK, combinedResp)

	writeJSONResponse(w, http.StatusOK, status) // Envoyer juste le statut pour l'instant
}

// handleFileUpload gère l'upload d'un fichier complet.
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Obtenir le chemin de destination depuis la query string ?path=...
	destinationPath := r.URL.Query().Get("path")
	if destinationPath == "" {
		http.Error(w, "Paramètre 'path' manquant dans l'URL", http.StatusBadRequest)
		return
	}
	// TODO: Valider/Nettoyer destinationPath (sécurité !)
	originalFilename := filepath.Base(destinationPath) // Extraire le nom de fichier

	log.Printf("API: Requête UPLOAD pour path '%s'\n", destinationPath)

	// 2. Préparer le traitement du fichier streamé
	hasher := sha256.New()
	// TeeReader copie ce qui est lu depuis r.Body vers le hasher *et* le retourne pour traitement
	teeReader := io.TeeReader(r.Body, hasher)
	reader := bufio.NewReaderSize(teeReader, data.MaxPayloadSize*2) // Buffer un peu plus grand

	shardKeys := make([]string, 0)
	totalBytesRead := int64(0)
	shardBuffer := make([]byte, data.MaxPayloadSize)
	shardCount := 0

	// 3. Boucle de lecture/découpage/stockage des shards
	for {
		bytesRead, err := reader.Read(shardBuffer) // Lire jusqu'à MaxPayloadSize

		if bytesRead > 0 {
			shardData := shardBuffer[:bytesRead]
			totalBytesRead += int64(bytesRead)
			shardCount++

			// Générer la clé du shard (basée sur le hash du contenu)
			shardKey := data.GenerateShardKey(shardData)
			shardKeys = append(shardKeys, shardKey)

			// Stocker le shard (déclenche la réplication)
			// Utiliser un contexte avec timeout ?
			errStore := s.dataManager.Put(shardKey, shardData)
			if errStore != nil {
				log.Printf("API(Upload): Erreur stockage shard %d pour '%s': %v", shardCount, destinationPath, errStore)
				http.Error(w, fmt.Sprintf("Erreur interne lors du stockage du shard %d", shardCount), http.StatusInternalServerError)
				// TODO: Nettoyer les shards déjà uploadés ? Complexe.
				return
			}
			// log.Printf("API(Upload): Shard %d (%s) stocké pour '%s'", shardCount, shardKey, destinationPath) // Peut être verbeux
		}

		// Vérifier la condition de fin
		if err != nil {
			if err == io.EOF {
				break // Fin normale du fichier
			}
			// Autre erreur de lecture
			log.Printf("API(Upload): Erreur lecture body pour '%s': %v", destinationPath, err)
			http.Error(w, "Erreur lecture du corps de la requête", http.StatusInternalServerError)
			// TODO: Cleanup ?
			return
		}
	} // Fin boucle for

	// 4. Calculer le checksum final et créer les métadonnées
	finalChecksum := hasher.Sum(nil)
	metadata := data.FileMetadata{
		Filename:     originalFilename,
		Filesize:     totalBytesRead,
		Blocksize:    data.MaxPayloadSize,
		TotalShards:  shardCount,
		Shards:       shardKeys,
		ChecksumType: "SHA256",
		Checksum:     hex.EncodeToString(finalChecksum), // Stocker en hex
		UploadTime:   time.Now(),
	}

	// 5. Stocker les métadonnées
	metadataKey := data.GetMetadataKey(destinationPath)
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		log.Printf("API(Upload): Erreur Marshal metadata pour '%s': %v", destinationPath, err)
		http.Error(w, "Erreur interne création métadonnées", http.StatusInternalServerError)
		// TODO: Cleanup ?
		return
	}
	err = s.dataManager.Put(metadataKey, metadataBytes) // Réplique les métadonnées
	if err != nil {
		log.Printf("API(Upload): Erreur stockage metadata pour '%s': %v", destinationPath, err)
		http.Error(w, "Erreur interne stockage métadonnées", http.StatusInternalServerError)
		// TODO: Cleanup ?
		return
	}

	log.Printf("API: Upload terminé pour '%s'. %d shards, %d octets. Checksum: %s\n",
		destinationPath, shardCount, totalBytesRead, metadata.Checksum)

	// 6. Répondre succès
	w.Header().Set("Location", "/files/download?path="+url.QueryEscape(destinationPath)) // Lien vers le fichier
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"message": "Upload successful", "path": "%s", "shards": %d, "size": %d}`, destinationPath, shardCount, totalBytesRead)
}

// handleFileDownload gère le téléchargement d'un fichier complet.
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	sourcePath := r.URL.Query().Get("path")
	if sourcePath == "" {
		http.Error(w, "Paramètre 'path' manquant", http.StatusBadRequest)
		return
	}

	log.Printf("API: Requête DOWNLOAD pour path '%s'\n", sourcePath)

	// 1. Récupérer les métadonnées
	metadataKey := data.GetMetadataKey(sourcePath)
	metadataBytes, err := s.dataManager.Get(metadataKey)
	if err != nil {
		if errors.Is(err, data.ErrNotFound) {
			http.Error(w, "Fichier (métadonnées) non trouvé", http.StatusNotFound)
			return
		}
		log.Printf("API(Download): Erreur Get metadata '%s': %v", sourcePath, err)
		http.Error(w, "Erreur interne lecture métadonnées", http.StatusInternalServerError)
		return
	}

	var metadata data.FileMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		log.Printf("API(Download): Erreur Unmarshal metadata '%s': %v", sourcePath, err)
		http.Error(w, "Erreur interne format métadonnées", http.StatusInternalServerError)
		return
	}

	// 2. Préparer les en-têtes de réponse HTTP
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, metadata.Filename))
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.Filesize, 10))
	// Envoyer les en-têtes maintenant (pas de retour en arrière possible après)
	w.WriteHeader(http.StatusOK)

	// 3. Streamer les shards vers la réponse
	hasher := sha256.New() // Pour vérifier le checksum à la volée (optionnel)
	totalBytesWritten := int64(0)

	for i, shardKey := range metadata.Shards {
		// Récupérer le shard (peut venir du cache local ou d'un autre nœud via réplication implicite)
		shardData, err := s.dataManager.Get(shardKey)
		if err != nil {
			// Si un shard manque, le téléchargement échoue !
			log.Printf("API(Download): ERREUR - Shard manquant '%s' (part %d) pour fichier '%s': %v", shardKey, i+1, sourcePath, err)
			// Impossible d'envoyer une erreur HTTP car les en-têtes sont partis.
			// On peut juste arrêter d'écrire. Le client verra une fin prématurée.
			// Alternative: utiliser HTTP chunked encoding et envoyer une fin spéciale ? Complexe.
			// TODO: Meilleure gestion d'erreur ici (ex: logguer et couper la connexion)
			return
		}

		// Écrire le shard dans la réponse HTTP
		bytesWritten, err := w.Write(shardData)
		if err != nil {
			// Le client a probablement fermé la connexion
			log.Printf("API(Download): Erreur écriture shard %d pour '%s' vers client: %v", i+1, sourcePath, err)
			return // Arrêter le transfert
		}
		totalBytesWritten += int64(bytesWritten)

		// Mettre à jour le hash si on vérifie
		hasher.Write(shardData)

		// Flush la réponse pour envoyer les données au fur et à mesure (important pour gros fichiers)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	// 4. Vérification finale (optionnelle)
	finalChecksum := hasher.Sum(nil)
	finalChecksumHex := hex.EncodeToString(finalChecksum)
	if finalChecksumHex != metadata.Checksum {
		log.Printf("API(Download): ERREUR CHECKSUM pour '%s'! Attendu: %s, Calculé: %s", sourcePath, metadata.Checksum, finalChecksumHex)
		// Trop tard pour signaler au client via HTTP status...
	}

	log.Printf("API: Download terminé pour '%s'. %d octets envoyés.\n", sourcePath, totalBytesWritten)
	if totalBytesWritten != metadata.Filesize {
		log.Printf("WARN(Download): Taille envoyée (%d) différente de la taille attendue (%d) pour '%s'", totalBytesWritten, metadata.Filesize, sourcePath)
	}
}

// handleFileList (Optionnel - Simple listage des clés de métadonnées)
func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { /* ... */
		return
	}
	log.Println("API: Requête LIST FILES")
	allKeys, err := s.dataManager.ListKeys()
	if err != nil { /* ... handle error ... */
		return
	}

	filePaths := []string{}
	for _, key := range allKeys {
		if strings.HasPrefix(key, data.MetadataKeyPrefix) {
			filePaths = append(filePaths, strings.TrimPrefix(key, data.MetadataKeyPrefix))
		}
	}
	writeJSONResponse(w, http.StatusOK, filePaths)
}

// handleFileDelete (Optionnel - Plus complexe car il faut supprimer les shards)
func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete { /* ... */
		return
	}
	sourcePath := r.URL.Query().Get("path")
	if sourcePath == "" { /* ... */
		return
	}
	log.Printf("API: Requête DELETE pour path '%s'\n", sourcePath)

	// 1. Lire les métadonnées
	metadataKey := data.GetMetadataKey(sourcePath)
	metadataBytes, err := s.dataManager.Get(metadataKey)
	if err != nil { /* ... handle not found etc ... */
		return
	}
	var metadata data.FileMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil { /* ... handle error ... */
		return
	}

	// 2. Supprimer tous les shards associés
	deletedShards := 0
	errorsShards := []string{}
	for _, shardKey := range metadata.Shards {
		errDel := s.dataManager.Delete(shardKey)
		if errDel != nil {
			log.Printf("API(Delete): Erreur suppression shard '%s' pour fichier '%s': %v", shardKey, sourcePath, errDel)
			errorsShards = append(errorsShards, shardKey)
		} else {
			deletedShards++
		}
	}

	// 3. Supprimer les métadonnées elles-mêmes
	errDelMeta := s.dataManager.Delete(metadataKey)
	if errDelMeta != nil {
		log.Printf("API(Delete): Erreur suppression métadonnées '%s': %v", metadataKey, errDelMeta)
		// Que faire ? Les shards sont peut-être supprimés...
		http.Error(w, "Erreur partielle lors de la suppression (métadonnées)", http.StatusInternalServerError)
		return
	}

	log.Printf("API: Delete terminé pour '%s'. %d/%d shards supprimés. Erreurs sur: %v\n", sourcePath, deletedShards, len(metadata.Shards), errorsShards)
	if len(errorsShards) > 0 {
		http.Error(w, fmt.Sprintf("Suppression partielle, %d shards non supprimés", len(errorsShards)), http.StatusConflict) // Ou 207 Multi-Status
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Utilitaires ---

// writeJSONResponse est une fonction helper pour envoyer des réponses JSON.
func writeJSONResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if data != nil {
		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("API: Erreur d'encodage JSON: %v\n", err)
		}
	}
}
