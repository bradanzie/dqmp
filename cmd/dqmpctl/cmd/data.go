// cmd/dqmpctl/cmd/data.go
package cmd

import (
	"github.com/spf13/cobra"
)

// dataCmd represents the base command when called without any subcommands
var dataCmd = &cobra.Command{
	Use:   "data",
	Short: "Commandes pour interagir avec les données stockées sur un nœud DQMP",
	Long: `Permet de récupérer (get), stocker (put), lister (list) ou
supprimer (delete) des paires clé-valeur sur le nœud cible.`,
	// Args: cobra.NoArgs, // Ne pas mettre NoArgs ici pour permettre les sous-commandes
	// Run: func(cmd *cobra.Command, args []string) { ... }, // Pas d'action si appelé sans sous-commande
}

func init() {
	rootCmd.AddCommand(dataCmd) // Attacher 'data' à la racine 'dqmpctl'

	// Ici, on pourrait ajouter des flags persistants spécifiques à *toutes* les sous-commandes 'data'
	// Par exemple: dataCmd.PersistentFlags().String("namespace", "", "Espace de noms pour les données")
}
