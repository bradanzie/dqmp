// cmd/dqmpctl/cmd/data_list.go
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

// dataListCmd represents the data list command
var dataListCmd = &cobra.Command{
	Use:   "list",
	Short: "Liste les clés stockées sur un nœud",
	Long:  `Interroge l'API REST (GET /data/) du nœud cible pour lister toutes les clés.`,
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		targetURL := apiTarget + "/data/" // URL sans clé
		fmt.Printf("Listage des clés depuis %s...\n", targetURL)

		client := http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(targetURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors de la requête GET: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Erreur: Le serveur a répondu avec le statut %s\n", resp.Status)
			if len(bodyBytes) > 0 {
				fmt.Fprintf(os.Stderr, "Réponse: %s\n", string(bodyBytes))
			}
			os.Exit(1)
		}

		// Décoder la réponse JSON (attendue comme un tableau de strings)
		var keys []string
		if err := json.NewDecoder(resp.Body).Decode(&keys); err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors du décodage de la réponse JSON: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("\n--- Clés Stockées ---")
		if len(keys) == 0 {
			fmt.Println("(Aucune clé trouvée)")
		} else {
			for _, key := range keys {
				fmt.Println(key)
			}
		}
		fmt.Printf("Total: %d clé(s).\n", len(keys))
	},
}

func init() {
	rootCmd.AddCommand(dataListCmd)
}
