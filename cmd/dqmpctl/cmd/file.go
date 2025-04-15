// cmd/dqmpctl/cmd/file.go
package cmd

import (
	"github.com/spf13/cobra"
)

// fileCmd represents the base command for file operations
var fileCmd = &cobra.Command{
	Use:   "file",
	Short: "Commandes pour gérer les fichiers sur le réseau DQMP",
	Long:  `Permet d'uploader, de télécharger, de lister ou de supprimer des fichiers complets stockés de manière distribuée.`,
}

func init() {
	rootCmd.AddCommand(fileCmd)
	// Ajouter des flags persistants pour 'file' si nécessaire
}
