// cmd/dqmpctl/cmd/file_upload.go
package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// fileUploadCmd represents the file upload command
var fileUploadCmd = &cobra.Command{
	Use:   "upload <fichier_local> <chemin_destination_dqmp>",
	Short: "Upload un fichier local vers le réseau DQMP",
	Long: `Lit un fichier local et l'envoie au nœud cible pour être découpé,
stocké et répliqué sur le réseau sous le chemin de destination spécifié.`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		localPath := args[0]
		destinationPath := args[1]

		// 1. Ouvrir le fichier local
		file, err := os.Open(localPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur ouverture fichier local '%s': %v\n", localPath, err)
			os.Exit(1)
		}
		defer file.Close()

		// Optionnel: Obtenir la taille pour l'affichage
		fileInfo, _ := file.Stat()
		fileSize := int64(0)
		if fileInfo != nil {
			fileSize = fileInfo.Size()
		}

		// 2. Construire l'URL de l'API
		// Ajouter le chemin comme query parameter
		targetURL := apiTarget + "/files/upload?path=" + url.QueryEscape(destinationPath)
		fmt.Printf("Upload de '%s' (%d octets) vers '%s' sur %s...\n", filepath.Base(localPath), fileSize, destinationPath, apiTarget)

		// 3. Créer la requête POST avec le corps du fichier
		// Utiliser un contexte avec un timeout potentiellement long pour l'upload
		// Le timeout de http.Client s'applique à toute l'opération (connexion + envoi + réponse)
		// Il faut peut-être un timeout très long ou pas de timeout client et gérer via contexte.
		client := http.Client{Timeout: 30 * time.Minute}              // Timeout long (30 min) pour gros fichiers
		req, err := http.NewRequest(http.MethodPost, targetURL, file) // file implémente io.Reader
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur création requête POST: %v\n", err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		// Le Content-Length sera défini par la lib http si la taille est connue (cas de os.File)

		// 4. Envoyer la requête
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur pendant l'upload: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		// 5. Vérifier la réponse
		bodyBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusCreated {
			fmt.Fprintf(os.Stderr, "Erreur: Upload échoué, statut %s\nRéponse: %s\n", resp.Status, string(bodyBytes))
			os.Exit(1)
		}

		fmt.Printf("Succès: Fichier uploadé.\nRéponse du serveur: %s\n", string(bodyBytes))
	},
}

func init() {
	fileCmd.AddCommand(fileUploadCmd) // Attacher à 'file'
	// Ajouter des flags spécifiques à 'upload' si besoin
}
