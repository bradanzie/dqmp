// pkg/config/config.go
package config

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config est la structure principale de configuration
type Config struct {
	Network   NetworkConfig   `mapstructure:"network"`
	Energy    EnergyConfig    `mapstructure:"energy"`
	Logging   LoggingConfig   `mapstructure:"logging"`
	API       APIConfig       `mapstructure:"api"`
	Data      DataConfig      `mapstructure:"data"`
	Discovery DiscoveryConfig `mapstructure:"discovery"`
	// Ajoutez d'autres sections ici au fur et à mesure
}

type NetworkConfig struct {
	Listen    string          `mapstructure:"listen"`
	Multipath MultipathConfig `mapstructure:"multipath"`
	DHT       DHTConfig       `mapstructure:"dht"`
}

type MultipathConfig struct {
	Enabled  bool `mapstructure:"enabled"`
	MaxPaths int  `mapstructure:"max_paths"`
}

type DHTConfig struct {
	BootstrapNodes []string `mapstructure:"bootstrap_nodes"`
}

type EnergyConfig struct {
	Policy     string           `mapstructure:"policy"`
	Thresholds EnergyThresholds `mapstructure:"thresholds"`
}

type EnergyThresholds struct {
	Critical int `mapstructure:"critical"`
	Warning  int `mapstructure:"warning"`
}

type LoggingConfig struct {
	Level string `mapstructure:"level"`
}

// APIConfig contient la configuration pour l'API REST
type APIConfig struct {
	Listen  string `mapstructure:"listen"`  // Adresse d'écoute de l'API (ex: ":8080")
	Enabled bool   `mapstructure:"enabled"` // Pour activer/désactiver l'API
}

type DataConfig struct {
	Directory string `mapstructure:"directory"` // Chemin vers le stockage
}

type DiscoveryConfig struct {
	ListenAddrs       []string      `mapstructure:"listen_addrs"`       // Multiaddrs libp2p (ex: "/ip4/0.0.0.0/tcp/0")
	BootstrapPeers    []string      `mapstructure:"bootstrap_peers"`    // Multiaddrs des bootstrap nodes libp2p
	Rendezvous        string        `mapstructure:"rendezvous"`         // Chaîne d'annonce/découverte pour DQMP
	IdentityPath      string        `mapstructure:"identity_path"`      // Chemin vers le fichier clé privée libp2p
	DiscoveryInterval time.Duration `mapstructure:"discovery_interval"` // Intervalle de recherche DHT
}

// DefaultConfigPath est le chemin par défaut pour le fichier de config
var DefaultConfigPath = "config/default.yaml"

// Load lit la configuration depuis un fichier et les variables d'environnement.
func Load(configPath *string) (*Config, error) {
	v := viper.New()

	// Définir les valeurs par défaut (même si elles sont aussi dans le fichier YAML)
	v.SetDefault("network.listen", ":4242")
	v.SetDefault("network.multipath.enabled", false)
	v.SetDefault("network.multipath.max_paths", 1)
	v.SetDefault("energy.policy", "balanced")
	v.SetDefault("energy.thresholds.critical", 5)
	v.SetDefault("energy.thresholds.warning", 20)
	v.SetDefault("logging.level", "info")
	v.SetDefault("api.listen", ":8002")
	v.SetDefault("api.enabled", true)
	v.SetDefault("data.directory", "./dqmp_node_data")
	v.SetDefault("discovery.listen_addrs", []string{
		"/ip4/0.0.0.0/tcp/0",         // Laisse libp2p choisir un port TCP libre
		"/ip4/0.0.0.0/udp/0/quic-v1", // Laisse libp2p choisir un port QUIC libre (si on utilise leur QUIC)
		// Pourrait être un port fixe si nécessaire: "/ip4/0.0.0.0/tcp/4001"
	})
	v.SetDefault("discovery.bootstrap_peers", []string{
		// Ajouter ici les multiaddrs des nœuds bootstrap libp2p publics ou de votre réseau
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59beuWjBeUFzxA43sKyPkTCaxUSYphgcPNMFGkUgu",
		"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
		// ... d'autres si connus
	})
	v.SetDefault("discovery.rendezvous", "dqmp-network-v1.0")    // Clé pour trouver les autres nœuds DQMP
	v.SetDefault("discovery.identity_path", "dqmp_identity.key") // Chemin relatif par défaut
	v.SetDefault("discovery.discovery_interval", "1m")           // Rechercher des pairs toutes les minutes

	// Utiliser le chemin fourni ou le chemin par défaut
	pathToUse := DefaultConfigPath
	if configPath != nil && *configPath != "" {
		pathToUse = *configPath
	}

	v.SetConfigFile(pathToUse)

	// Lire le fichier de config s'il existe
	if err := v.ReadInConfig(); err != nil {
		// Ignorer l'erreur si le fichier n'existe pas, mais utiliser les défauts
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err // Erreur de lecture autre que fichier non trouvé
		}
		// Fichier non trouvé, on utilisera les valeurs par défaut/env vars
	}

	// Lire aussi les variables d'environnement (ex: DQMP_NETWORK_LISTEN=:4243)
	v.SetEnvPrefix("DQMP")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Unmarshal la configuration dans la struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	// Attention: ne pas faire ça en prod.
	if cfg.Data.Directory == "./dqmp_node_data" && strings.Contains(cfg.Network.Listen, ":") {
		parts := strings.Split(cfg.Network.Listen, ":")
		if len(parts) == 2 && parts[1] != "" {
			cfg.Data.Directory = fmt.Sprintf("./dqmp_node_data_%s", parts[1])
		}
	}

	listenPort := ""
	if strings.Contains(cfg.Network.Listen, ":") {
		parts := strings.Split(cfg.Network.Listen, ":")
		if len(parts) == 2 && parts[1] != "" {
			listenPort = parts[1]
		}
	}

	if listenPort != "" {
		if cfg.Data.Directory == "./dqmp_node_data" {
			cfg.Data.Directory = fmt.Sprintf("./dqmp_node_data_%s", listenPort)
		}
		// Générer un chemin d'identité unique basé sur le port DQMP
		// Attention: si le port change, l'identité change! Mieux vaut chemin fixe.
		// On le fait ici pour simplifier les tests locaux multiples.
		if cfg.Discovery.IdentityPath == "dqmp_identity.key" {
			cfg.Discovery.IdentityPath = fmt.Sprintf("dqmp_identity_%s.key", listenPort)
		}
	}

	if !cfg.API.Enabled && cfg.API.Listen != "" {
		log.Println("WARN: L'API est désactivée mais une adresse d'écoute est spécifiée.")
		// cfg.API.Listen = "" // Optionnel: forcer l'adresse vide si désactivé
	}
	return &cfg, nil
}

// AddConfigFlag ajoute un flag --config au FlagSet de pflag
func AddConfigFlag(fs *pflag.FlagSet) *string {
	return fs.StringP("config", "c", "", "Path to configuration file (default: "+DefaultConfigPath+")")
}
