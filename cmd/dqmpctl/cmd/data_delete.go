// cmd/dqmpctl/cmd/data_delete.go
package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// dataDeleteCmd represents the data delete command
var dataDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Supprime une paire clé-valeur d'un nœud",
	Long:  `Envoie une requête DELETE à l'API REST (/data/{key}) du nœud cible.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		targetURL := apiTarget + "/data/" + key
		fmt.Printf("Suppression de la clé '%s' sur %s...\n", key, targetURL)

		// Créer la requête DELETE
		req, err := http.NewRequest(http.MethodDelete, targetURL, nil) // Pas de corps pour DELETE
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur création requête DELETE: %v\n", err)
			os.Exit(1)
		}

		client := http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors de la requête DELETE: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		// Vérifier le statut de la réponse
		if resp.StatusCode != http.StatusNoContent { // On attend 204
			bodyBytes, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Erreur: Le serveur a répondu avec le statut %s\n", resp.Status)
			if len(bodyBytes) > 0 {
				fmt.Fprintf(os.Stderr, "Réponse: %s\n", string(bodyBytes))
			}
			os.Exit(1)
		}

		fmt.Printf("Succès: Clé '%s' supprimée (si elle existait).\n", key)
	},
}

func init() {
	rootCmd.AddCommand(dataDeleteCmd)
}
