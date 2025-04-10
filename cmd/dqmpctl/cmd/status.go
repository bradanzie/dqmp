// cmd/dqmpctl/cmd/status.go
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Récupère le statut général d'un nœud DQMP via son API",
	Long:  `Interroge l'endpoint /status de l'API REST du nœud cible spécifié par --target.`,
	Args:  cobra.NoArgs, // Pas d'arguments spécifiques pour cette commande
	Run: func(cmd *cobra.Command, args []string) {
		targetURL := apiTarget + "/status" // Construire l'URL complète
		fmt.Printf("Interrogation du statut sur %s...\n", targetURL)

		client := http.Client{Timeout: 5 * time.Second} // Client HTTP simple avec timeout
		resp, err := client.Get(targetURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors de la requête vers %s: %v\n", targetURL, err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body) // Lire le corps même si erreur
			fmt.Fprintf(os.Stderr, "Erreur: le serveur a répondu avec le statut %s\n", resp.Status)
			if len(bodyBytes) > 0 {
				fmt.Fprintf(os.Stderr, "Réponse: %s\n", string(bodyBytes))
			}
			os.Exit(1)
		}

		// Décoder la réponse JSON
		var statusResp map[string]interface{} // Utiliser une map générique pour afficher facilement
		if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors du décodage de la réponse JSON: %v\n", err)
			os.Exit(1)
		}

		// Afficher joliment les informations
		fmt.Println("\n--- Statut du Nœud ---")
		for key, val := range statusResp {
			// Formater le temps différemment ?
			if key == "start_time" {
				if tStr, ok := val.(string); ok {
					parsedTime, pErr := time.Parse(time.RFC3339Nano, tStr)
					if pErr == nil {
						val = parsedTime.Local().Format("2006-01-02 15:04:05 MST")
					}
				}
			}
			fmt.Printf("%-15s: %v\n", key, val)
		}
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
	// Pas de flags spécifiques pour status pour l'instant
}
