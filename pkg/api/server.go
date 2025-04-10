// pkg/api/server.go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	// Importer les packages nécessaires du projet
	"github.com/bradanzie/dqmp/pkg/config" // Adaptez
	"github.com/bradanzie/dqmp/pkg/data"
	"github.com/bradanzie/dqmp/pkg/peers" // Adaptez
	// "github.com/votre-utilisateur/dqmp-project/pkg/dqmp" // On aura besoin d'une réf au Node plus tard
)

type NodeReplicator interface {
	ReplicateData(ctx context.Context, key string, value []byte)
	// Ajouter d'autres méthodes si l'API a besoin d'autres interactions avec le nœud
}

// Server encapsule le serveur HTTP de l'API REST.
type Server struct {
	config      *config.APIConfig
	peerManager *peers.Manager // Référence au gestionnaire de pairs
	dataManager *data.Manager
	replicator  NodeReplicator
	startTime   time.Time // Heure de démarrage du nœud
	nodeVersion string    // Version du nœud (on pourrait la passer)
	httpServer  *http.Server
}

// NewServer crée une nouvelle instance du serveur API.
// On passe les dépendances nécessaires (config, peer manager, etc.).
func NewServer(
	cfg *config.APIConfig,
	pm *peers.Manager,
	dm *data.Manager,
	replicator NodeReplicator,
	startTime time.Time,
	version string,
) *Server {
	if !cfg.Enabled {
		return nil // Retourne nil si l'API n'est pas activée
	}
	return &Server{
		config:      cfg,
		peerManager: pm,
		dataManager: dm,
		replicator:  replicator,
		startTime:   startTime,
		nodeVersion: version, // Exemple: passer une version
	}
}

// Start lance le serveur HTTP dans une goroutine.
func (s *Server) Start() error {
	if s == nil { // Vérifier si le serveur a été créé (était enabled)
		log.Println("API: Serveur désactivé, ne démarre pas.")
		return nil
	}

	mux := http.NewServeMux() // Utiliser le multiplexeur standard

	// Enregistrer les handlers pour les routes
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/peers", s.handlePeers)
	// Ajouter le préfixe standard (optionnel mais bonne pratique)
	// mux.Handle("/dqmp/v1/", http.StripPrefix("/dqmp/v1", mux)) // Si on veut préfixer
	mux.HandleFunc("/data/", s.handleData) // Notez le / final

	s.httpServer = &http.Server{
		Addr:         s.config.Listen,
		Handler:      mux,
		ReadTimeout:  5 * time.Second, // Délais de base
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("API: Démarrage du serveur HTTP sur %s\n", s.config.Listen)
	// Lancer dans une goroutine pour ne pas bloquer
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
		return nil // Pas démarré ou désactivé
	}
	log.Println("API: Arrêt du serveur HTTP...")
	err := s.httpServer.Shutdown(ctx) // Arrêt gracieux avec timeout
	if err != nil {
		log.Printf("API: Erreur lors de l'arrêt gracieux du serveur HTTP: %v\n", err)
		// Tenter un arrêt immédiat si l'arrêt gracieux échoue ?
		// s.httpServer.Close()
	} else {
		log.Println("API: Serveur HTTP arrêté proprement.")
	}
	return err // Retourner l'erreur de Shutdown
}

// --- Handlers ---

// handleStatus renvoie des informations générales sur le nœud.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	status := struct {
		Version      string    `json:"version"`
		StartTime    time.Time `json:"start_time"`
		Uptime       string    `json:"uptime"`
		GoVersion    string    `json:"go_version"`
		NumCPU       int       `json:"num_cpu"`
		NumGoroutine int       `json:"num_goroutine"`
		PeerCount    int       `json:"peer_count"` // Ajout du nombre de pairs
	}{
		Version:      s.nodeVersion, // Utiliser une vraie version plus tard
		StartTime:    s.startTime,
		Uptime:       time.Since(s.startTime).Round(time.Second).String(),
		GoVersion:    runtime.Version(),
		NumCPU:       runtime.NumCPU(),
		NumGoroutine: runtime.NumGoroutine(),
		PeerCount:    len(s.peerManager.GetAllPeers()), // Accès au peer manager
	}

	writeJSONResponse(w, http.StatusOK, status)
}

// handlePeers renvoie la liste des pairs connus par le PeerManager.
// pkg/api/server.go

// handlePeers renvoie la liste des pairs connus par le PeerManager.
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	allPeers := s.peerManager.GetAllPeers() // Récupère la liste

	// Définir la structure de la réponse
	type PeerInfoAPI struct { // Renommer pour éviter conflit si PeerInfo existe ailleurs
		PeerID      string    `json:"peer_id"`
		State       string    `json:"state"`
		DQMPAddress string    `json:"dqmp_address,omitempty"`
		Multiaddrs  []string  `json:"multiaddrs,omitempty"` // Sera omis si vide/nil
		LastSeen    time.Time `json:"last_seen"`
		LastError   string    `json:"last_error,omitempty"` // Sera omis si vide/nil
	}

	response := make([]PeerInfoAPI, 0, len(allPeers)) // Initialiser avec capacité

	for _, p := range allPeers {
		// Vérification cruciale: ignorer si le pair est nil (ne devrait pas arriver, mais par sécurité)
		if p == nil {
			continue
		}

		// Préparer les champs avec vérifications nil
		maddrsStr := []string{}  // Initialiser à vide, pas nil
		if p.Multiaddrs != nil { // Vérifier si le slice Multiaddrs est nil
			maddrsStr = make([]string, 0, len(p.Multiaddrs)) // Initialiser avec capacité
			for _, ma := range p.Multiaddrs {
				if ma != nil { // Vérifier si l'adresse elle-même est nil
					maddrsStr = append(maddrsStr, ma.String())
				}
			}
		}

		dqmpAddrStr := ""
		if p.DQMPAddr != nil { // Vérifier si l'adresse DQMP est nil
			dqmpAddrStr = p.DQMPAddr.String()
		}

		errStr := ""
		if p.LastError != nil { // Vérifier si l'erreur est nil
			errStr = p.LastError.Error()
		}

		peerIDStr := ""
		if p.ID != "" { // Vérifier si l'ID est non vide
			peerIDStr = p.ID.String()
		} else {
			peerIDStr = "(Inconnu)" // Indiquer si l'ID n'a pas pu être déterminé
		}

		response = append(response, PeerInfoAPI{
			PeerID:      peerIDStr,
			State:       string(p.State), // L'état ne devrait pas être nil
			DQMPAddress: dqmpAddrStr,
			Multiaddrs:  maddrsStr,  // Utiliser le slice potentiellement vide
			LastSeen:    p.LastSeen, // Time.Time n'est pas un pointeur
			LastError:   errStr,
		})
	}

	writeJSONResponse(w, http.StatusOK, response) // Envoyer la réponse JSON
}

// handleData gère les requêtes sur les données (GET, PUT, DELETE).
// On pourrait ajouter un préfixe comme /dqmp/v1/data/ pour versionner l'API.
// On pourrait aussi utiliser un routeur comme gorilla/mux pour plus de flexibilité.
// On pourrait aussi ajouter un middleware pour la gestion des erreurs.
func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	// Extraire la clé de l'URL, ex: /data/mykey -> mykey
	key := strings.TrimPrefix(r.URL.Path, "/data/")
	if key == "" {
		if r.Method == http.MethodGet {
			// Si GET /data/ (sans clé), lister les clés
			s.handleListKeys(w, r)
			return
		}
		http.Error(w, "Clé manquante dans l'URL (/data/{key})", http.StatusBadRequest)
		return
	}

	// Dispatcher selon la méthode HTTP
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
	log.Printf("API: Requête GET pour la clé '%s'\n", key)
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

	// Renvoyer le payload brut. Le Content-Type pourrait être configurable ou deviné.
	// Pour l'instant, on utilise application/octet-stream.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(payload)
	if err != nil {
		// Difficile de faire quoi que ce soit si l'écriture échoue ici
		log.Printf("API: Erreur lors de l'écriture de la réponse GET pour '%s': %v", key, err)
	}
}

// handlePutData gère les requêtes PUT pour stocker des données par clé.
func (s *Server) handlePutData(w http.ResponseWriter, r *http.Request, key string) {
	log.Printf("API: Requête PUT pour la clé '%s'\n", key)

	// Lire le corps de la requête (le payload)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("API: Erreur lecture body PUT '%s': %v", key, err)
		http.Error(w, "Erreur lecture du corps de la requête", http.StatusBadRequest)
		return
	}
	// Ne pas oublier de fermer le Body, même si io.ReadAll le fait souvent.
	defer r.Body.Close()

	// Vérifier la taille (déjà fait dans NewDataShard, mais on peut le faire ici aussi)
	if len(payload) > data.MaxPayloadSize {
		http.Error(w, fmt.Sprintf("La taille du payload (%d) dépasse la limite (%d)", len(payload), data.MaxPayloadSize), http.StatusRequestEntityTooLarge)
		return
	}

	// 1. Stocker localement
	err = s.dataManager.Put(key, payload)
	if err != nil {
		log.Printf("API: Erreur interne Put data '%s': %v", key, err)
		http.Error(w, "Erreur interne du serveur lors du stockage", http.StatusInternalServerError)
		return
	}
	log.Printf("API: Donnée '%s' stockée localement.\n", key)

	// 2. Initier la réplication via l'interface
	// Vérifier que replicator n'est pas nil (bonne pratique)
	if s.replicator != nil {
		go s.replicator.ReplicateData(context.Background(), key, payload)
	} else {
		log.Println("WARN: Replicator non défini, impossible de lancer la réplication.")
	}

	// Répondre avec succès (201 ou 204)
	// 201 Created si c'est une nouvelle ressource, 204 No Content si mise à jour.
	// Pour simplifier, utilisons 204.
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteData gère les requêtes DELETE pour supprimer des données par clé.
func (s *Server) handleDeleteData(w http.ResponseWriter, r *http.Request, key string) {
	log.Printf("API: Requête DELETE pour la clé '%s'\n", key)
	err := s.dataManager.Delete(key)
	if err != nil {
		log.Printf("API: Erreur interne Delete data '%s': %v", key, err)
		http.Error(w, "Erreur interne du serveur lors de la suppression", http.StatusInternalServerError)
		return
	}
	// Répondre avec succès
	w.WriteHeader(http.StatusNoContent)
}

// handleListKeys gère les requêtes GET pour lister toutes les clés.
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	log.Println("API: Requête GET pour lister les clés")
	keys, err := s.dataManager.ListKeys()
	if err != nil {
		log.Printf("API: Erreur interne List keys: %v", err)
		http.Error(w, "Erreur interne du serveur", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, http.StatusOK, keys)
}

// --- Utilitaires ---

// writeJSONResponse est une fonction helper pour envoyer des réponses JSON.
func writeJSONResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if data != nil {
		if err := json.NewEncoder(w).Encode(data); err != nil {
			// Logguer l'erreur côté serveur, mais difficile d'envoyer une erreur au client maintenant
			log.Printf("API: Erreur d'encodage JSON: %v\n", err)
			// On pourrait tenter d'écrire une erreur texte mais l'en-tête est déjà envoyé
			// http.Error(w, `{"error": "Internal server error during JSON encoding"}`, http.StatusInternalServerError)
		}
	}
}
