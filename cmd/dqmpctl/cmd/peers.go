// cmd/dqmpctl/cmd/peers.go
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

// peersCmd represents the peers command
var peersCmd = &cobra.Command{
	Use:   "peers",
	Short: "Liste les pairs connus par un nœud DQMP via son API",
	Long:  `Interroge l'endpoint /peers de l'API REST du nœud cible spécifié par --target.`,
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		targetURL := apiTarget + "/peers"
		fmt.Printf("Récupération de la liste des pairs depuis %s...\n", targetURL)

		client := http.Client{Timeout: 10 * time.Second} // Un peu plus long pour les pairs ?
		resp, err := client.Get(targetURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors de la requête vers %s: %v\n", targetURL, err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Erreur: le serveur a répondu avec le statut %s\n", resp.Status)
			if len(bodyBytes) > 0 {
				fmt.Fprintf(os.Stderr, "Réponse: %s\n", string(bodyBytes))
			}
			os.Exit(1)
		}

		// Structure attendue de la réponse (tableau de PeerInfo)
		type PeerInfo struct {
			Address     string `json:"address"`
			IsConnected bool   `json:"is_connected"`
		}
		var peersResp []PeerInfo

		if err := json.NewDecoder(resp.Body).Decode(&peersResp); err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors du décodage de la réponse JSON: %v\n", err)
			os.Exit(1)
		}

		// Afficher la liste
		fmt.Println("\n--- Pairs Connus ---")
		if len(peersResp) == 0 {
			fmt.Println("(Aucun pair connu)")
		} else {
			fmt.Printf("%-25s | %s\n", "Adresse", "Connecté")
			fmt.Println("--------------------------|-----------")
			for _, p := range peersResp {
				fmt.Printf("%-25s | %t\n", p.Address, p.IsConnected)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(peersCmd)
}
