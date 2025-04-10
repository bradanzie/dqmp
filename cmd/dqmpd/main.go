// cmd/dqmpd/main.go
package main

import (
	"log"
	"os"

	"github.com/bradanzie/dqmp/pkg/config" // Adaptez le chemin
	"github.com/bradanzie/dqmp/pkg/dqmp"   // Adaptez le chemin
	"github.com/spf13/pflag"
)

func main() {
	// Configuration des flags en utilisant pflag
	fs := pflag.NewFlagSet("dqmpd", pflag.ExitOnError)
	configPath := config.AddConfigFlag(fs)
	// Ajoutez d'autres flags ici si nécessaire

	// Parser les flags
	err := fs.Parse(os.Args[1:])
	if err != nil {
		log.Fatalf("ERROR: Erreur lors du parsing des flags: %v", err)
	}

	// Charger la configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("ERROR: Impossible de charger la configuration: %v\n", err)
	}

	// TODO: Initialiser un logger plus avancé basé sur cfg.Logging.Level

	// Créer le nœud DQMP
	node, err := dqmp.NewNode(cfg)
	if err != nil {
		log.Fatalf("ERROR: Impossible de créer le nœud DQMP: %v\n", err)
	}

	// Démarrer le nœud et attendre l'arrêt
	if err := node.Run(); err != nil {
		log.Fatalf("ERROR: Le nœud s'est arrêté avec une erreur: %v\n", err)
	}

	log.Println("INFO: dqmpd terminé proprement.")
}
