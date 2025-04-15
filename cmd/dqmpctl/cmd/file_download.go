// cmd/dqmpctl/cmd/file_download.go
package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// fileDownloadCmd represents the file download command
var fileDownloadCmd = &cobra.Command{
	Use:   "download <chemin_source_dqmp> <fichier_local>",
	Short: "Télécharge un fichier depuis le réseau DQMP vers un fichier local",
	Long:  `Récupère les shards d'un fichier depuis le nœud cible (qui les trouve sur le réseau) et les réassemble dans un fichier local.`,
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		sourcePath := args[0]
		localPath := args[1]

		// 1. Construire l'URL de l'API
		targetURL := apiTarget + "/files/download?path=" + url.QueryEscape(sourcePath)
		fmt.Printf("Téléchargement de '%s' depuis %s vers '%s'...\n", sourcePath, apiTarget, localPath)

		// 2. Créer le fichier de destination local
		outFile, err := os.Create(localPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur création fichier local '%s': %v\n", localPath, err)
			os.Exit(1)
		}
		defer outFile.Close()

		// 3. Effectuer la requête GET
		// Utiliser un timeout long pour le téléchargement
		client := http.Client{Timeout: 30 * time.Minute}
		resp, err := client.Get(targetURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur requête GET: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		// 4. Vérifier le statut avant de lire le corps
		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body) // Lire pour afficher l'erreur
			fmt.Fprintf(os.Stderr, "Erreur: Téléchargement échoué, statut %s\nRéponse: %s\n", resp.Status, string(bodyBytes))
			// Supprimer le fichier local potentiellement vide créé
			outFile.Close()
			os.Remove(localPath)
			os.Exit(1)
		}

		// Afficher les infos du header si besoin
		fmt.Printf("Nom de fichier serveur: %s\n", resp.Header.Get("Content-Disposition"))
		fmt.Printf("Taille attendue: %s octets\n", resp.Header.Get("Content-Length"))

		// 5. Copier le corps de la réponse (le fichier) dans le fichier local
		bytesCopied, err := io.Copy(outFile, resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur pendant la copie des données: %v\n", err)
			// Le fichier local peut être incomplet
			os.Exit(1)
		}

		fmt.Printf("Succès: Téléchargement terminé. %d octets écrits dans '%s'.\n", bytesCopied, localPath)
		// TODO: Vérifier le checksum si possible ?
	},
}

func init() {
	fileCmd.AddCommand(fileDownloadCmd) // Attacher à 'file'
	// Ajouter des flags spécifiques à 'download' si besoin
}
