// cmd/dqmpctl/cmd/peers.go
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time" // Import ajouté pour gérer time.Time

	"github.com/spf13/cobra"
)

// peersCmd represents the peers command
var peersCmd = &cobra.Command{
	Use:   "peers",
	Short: "Liste les pairs connus par un nœud DQMP via son API",
	Long:  `Interroge l'endpoint /peers de l'API REST du nœud cible spécifié par --target.`,
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		targetURL := apiTarget + "/peers"
		fmt.Printf("Récupération de la liste des pairs depuis %s...\n", targetURL)

		client := http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(targetURL)
		if err != nil { /* ... handle error ... */
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK { /* ... handle error ... */
		}

		// --- POINT CRITIQUE : Décodage et Affichage ---

		// 1. Définir la structure côté client pour DÉCODER le JSON reçu
		//    Cette structure DOIT correspondre à PeerInfoAPI DÉFINIE CÔTÉ SERVEUR !
		type PeerInfoClient struct { // Utiliser un nom différent pour éviter confusion
			PeerID      string    `json:"peer_id"` // Tag JSON doit correspondre
			State       string    `json:"state"`
			DQMPAddress string    `json:"dqmp_address"` // omitempty n'est pas pertinent ici
			Multiaddrs  []string  `json:"multiaddrs"`
			EcoScore    float32   `json:"eco_score"`
			LastSeen    time.Time `json:"last_seen"`
			LastError   string    `json:"last_error"`
		}
		var peersResp []PeerInfoClient // Utiliser le type client

		// 2. Décoder la réponse JSON dans notre structure client
		bodyBytes, err := io.ReadAll(resp.Body) // Lire d'abord pour debug si décodage échoue
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors de la lecture du corps de la réponse: %v\n", err)
			os.Exit(1)
		}
		// Logguer le JSON brut reçu pour vérifier son contenu
		// fmt.Printf("DEBUG: JSON reçu:\n%s\n", string(bodyBytes)) // Décommenter si besoin

		if err := json.Unmarshal(bodyBytes, &peersResp); err != nil { // Utiliser Unmarshal
			fmt.Fprintf(os.Stderr, "Erreur lors du décodage de la réponse JSON: %v\n", err)
			fmt.Fprintf(os.Stderr, "--- Corps Réponse Brute ---\n%s\n-------------------------\n", string(bodyBytes)) // Afficher le corps brut en cas d'erreur
			os.Exit(1)
		}

		// 3. Afficher les informations décodées
		fmt.Println("\n--- Pairs Connus ---")
		if len(peersResp) == 0 {
			fmt.Println("(Aucun pair connu)")
		} else {
			// Afficher les en-têtes corrigés
			fmt.Printf("%-58s | %-11s | %-21s | %-9s | %s\n", "Peer ID", "État", "Adresse DQMP", "EcoScore", "Erreur Dernière Conn.")
			fmt.Println(strings.Repeat("-", 60) + "|-" + strings.Repeat("-", 13) + "|-" + strings.Repeat("-", 23) + "|-" + strings.Repeat("-", 11) + "|-" + strings.Repeat("-", 25))

			for _, p := range peersResp {
				// Utiliser les champs de PeerInfoClient
				fmt.Printf("%-58s | %-11s | %-21s | %-9.3f | %s\n", // Utiliser %.3f pour le score
					p.PeerID,
					p.State,
					p.DQMPAddress,
					p.EcoScore,
					p.LastError,
				)
				// Optionnel: Afficher les multiaddrs si besoin
				// if len(p.Multiaddrs) > 0 {
				//    fmt.Printf("  Multiaddrs: %v\n", p.Multiaddrs)
				// }
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(peersCmd)
}
