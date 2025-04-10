// cmd/dqmpctl/cmd/root.go
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Variable pour le flag --config (si on ajoute une config au CLI plus tard)
var (
	cfgFile   string
	apiTarget string // Nouvelle variable globale pour l'adresse API
)

// rootCmd représente la commande de base sans arguments
var rootCmd = &cobra.Command{
	Use:   "dqmpctl",
	Short: "Un outil CLI pour interagir avec les nœuds DQMP",
	Long: `dqmpctl permet d'envoyer des commandes de contrôle, de récupérer
des informations et de gérer un réseau DQMP.`,
	// Décommenter si la commande racine doit faire quelque chose :
	// Run: func(cmd *cobra.Command, args []string) { },
}

// Execute ajoute toutes les commandes enfants à la commande racine et définit les flags.
// Est appelé par main.main(). Ne doit être appelé qu'une seule fois par rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erreur: %s\n", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Flags globaux (si nécessaire)
	rootCmd.PersistentFlags().StringVarP(&apiTarget, "target", "t", "http://127.0.0.1:8080", "Adresse de l'API REST du nœud cible (ex: http://host:port)")

	// Flags locaux à la commande racine (si nécessaire)
	// rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

// initConfig lit le fichier de config et les variables d'env si définis.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		// Search config in home directory with name ".dqmpctl" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".dqmpctl")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}
