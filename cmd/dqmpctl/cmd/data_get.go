// cmd/dqmpctl/cmd/data_get.go
package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var outputFile string // Flag pour écrire dans un fichier

// dataGetCmd represents the data get command
var dataGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Récupère la valeur associée à une clé depuis un nœud",
	Long:  `Interroge l'API REST (GET /data/{key}) du nœud cible pour récupérer la valeur.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		targetURL := apiTarget + "/data/" + key
		fmt.Printf("Récupération de la clé '%s' depuis %s...\n", key, targetURL)

		client := http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(targetURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors de la requête GET: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			fmt.Fprintf(os.Stderr, "Erreur: Clé '%s' non trouvée sur le nœud.\n", key)
			os.Exit(1)
		}
		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Erreur: Le serveur a répondu avec le statut %s\n", resp.Status)
			if len(bodyBytes) > 0 {
				fmt.Fprintf(os.Stderr, "Réponse: %s\n", string(bodyBytes))
			}
			os.Exit(1)
		}

		// Déterminer la sortie (stdout ou fichier)
		var writer io.Writer = os.Stdout
		if outputFile != "" {
			file, err := os.Create(outputFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Erreur: Impossible de créer le fichier de sortie '%s': %v\n", outputFile, err)
				os.Exit(1)
			}
			defer file.Close()
			writer = file
			fmt.Printf("Écriture de la valeur dans '%s'...\n", outputFile)
		} else {
			fmt.Println("\n--- Valeur ---")
		}

		// Copier le corps de la réponse vers la sortie
		bytesCopied, err := io.Copy(writer, resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors de la lecture/écriture de la valeur: %v\n", err)
			os.Exit(1)
		}

		if outputFile == "" {
			fmt.Println("\n--------------") // Séparateur pour stdout
		}
		fmt.Printf("Succès: %d octets récupérés.\n", bytesCopied)

	},
}

func init() {
	// Créer une sous-commande 'data' pour grouper
	// dataCmd := &cobra.Command{Use: "data", Short: "Interagir avec les données stockées"}
	// rootCmd.AddCommand(dataCmd)
	// dataCmd.AddCommand(dataGetCmd)
	// // Ajouter les autres commandes data à dataCmd

	// Pour l'instant, on les ajoute à la racine pour la simplicité
	dataCmd.AddCommand(dataGetCmd)
	dataGetCmd.Flags().StringVarP(&outputFile, "output", "o", "", "Fichier où écrire la valeur (défaut: stdout)")
}
