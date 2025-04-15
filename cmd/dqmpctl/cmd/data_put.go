// cmd/dqmpctl/cmd/data_put.go
package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var inputFile string // Flag pour lire depuis un fichier

// dataPutCmd represents the data put command
var dataPutCmd = &cobra.Command{
	Use:   "put <key> [value]",
	Short: "Stocke une paire clé-valeur sur un nœud",
	Long: `Envoie une requête PUT à l'API REST (/data/{key}) du nœud cible.
La valeur peut être fournie en argument ou lue depuis un fichier avec --input.`,
	Args: cobra.RangeArgs(1, 2), // Soit clé seule (avec --input), soit clé et valeur
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		var payloadReader io.Reader
		var payloadLen int64 = -1 // Taille inconnue initialement

		// Déterminer la source du payload (argument ou fichier)
		if len(args) == 2 { // Valeur en argument
			value := args[1]
			payloadReader = bytes.NewReader([]byte(value))
			payloadLen = int64(len(value))
		} else if inputFile != "" { // Valeur depuis fichier
			file, err := os.Open(inputFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Erreur: Impossible d'ouvrir le fichier d'entrée '%s': %v\n", inputFile, err)
				os.Exit(1)
			}
			defer file.Close()
			// Obtenir la taille pour le Content-Length (bonne pratique HTTP)
			fileInfo, err := file.Stat()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Erreur: Impossible d'obtenir les informations du fichier '%s': %v\n", inputFile, err)
				os.Exit(1)
			}
			payloadLen = fileInfo.Size()
			payloadReader = file
		} else {
			fmt.Fprintln(os.Stderr, "Erreur: Vous devez fournir une valeur en argument ou utiliser --input <fichier>.")
			os.Exit(1)
		}

		// Vérifier la taille max (si connue)
		// Note: MaxPayloadSize est défini dans pkg/data, pas idéal d'y accéder directement ici.
		// On pourrait le passer en config ou juste laisser le serveur rejeter. Laissons le serveur gérer.
		// const MaxPayloadSize = 240 // À éviter ici
		// if payloadLen > MaxPayloadSize { ... }

		targetURL := apiTarget + "/data/" + key
		fmt.Printf("Stockage de la clé '%s' sur %s...\n", key, targetURL)

		// Créer la requête PUT
		req, err := http.NewRequest(http.MethodPut, targetURL, payloadReader)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur création requête PUT: %v\n", err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/octet-stream") // Type de contenu brut
		if payloadLen >= 0 {
			req.ContentLength = payloadLen // Important pour le serveur
		}

		client := http.Client{Timeout: 15 * time.Second} // Timeout un peu plus long pour l'upload
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur lors de la requête PUT: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		// Vérifier le statut de la réponse
		if resp.StatusCode != http.StatusNoContent { // On attend 204 du serveur
			bodyBytes, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Erreur: Le serveur a répondu avec le statut %s\n", resp.Status)
			if len(bodyBytes) > 0 {
				fmt.Fprintf(os.Stderr, "Réponse: %s\n", string(bodyBytes))
			}
			// Tenter de donner une raison plus spécifique
			if resp.StatusCode == http.StatusRequestEntityTooLarge {
				fmt.Fprintln(os.Stderr, "(La taille du payload dépasse probablement la limite du serveur)")
			}
			os.Exit(1)
		}

		fmt.Printf("Succès: Clé '%s' stockée/mise à jour.\n", key)

	},
}

func init() {
	dataCmd.AddCommand(dataPutCmd)
	dataPutCmd.Flags().StringVarP(&inputFile, "input", "i", "", "Fichier contenant la valeur à stocker")
}
