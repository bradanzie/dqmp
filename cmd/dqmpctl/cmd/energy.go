// cmd/dqmpctl/cmd/energy.go
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	// Importer les types energy si nécessaire pour l'affichage formaté
	// "github.com/bradanzie/dqmp/pkg/energy"
)

// energyCmd représente la commande 'energy' de base
var energyCmd = &cobra.Command{
	Use:   "energy",
	Short: "Commandes liées à la gestion de l'énergie",
	Long:  `Permet d'interroger ou de configurer les aspects énergétiques d'un nœud DQMP.`,
}

// energyStatusCmd représente la sous-commande 'energy status'
var energyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Affiche le statut énergétique actuel du nœud cible",
	Long:  `Interroge l'endpoint /energy/status de l'API REST du nœud.`,
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		targetURL := apiTarget + "/energy/status"
		fmt.Printf("Interrogation du statut énergétique sur %s...\n", targetURL)

		client := http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(targetURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Erreur requête: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Erreur: Statut %s\nRéponse: %s\n", resp.Status, string(bodyBytes))
			os.Exit(1)
		}

		// Décoder la réponse JSON (structure energy.Status)
		// Utiliser une map générique pour un affichage simple
		var statusResp map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
			fmt.Fprintf(os.Stderr, "Erreur décodage JSON: %v\n", err)
			os.Exit(1)
		}

		// Afficher joliment
		fmt.Println("\n--- Statut Énergétique ---")
		// Afficher dans un ordre logique si possible
		keysToShow := []string{
			"timestamp", "source", "battery_level_percent", "is_charging",
			"current_consumption_mw", "estimated_uptime", "eco_score",
		}
		for _, key := range keysToShow {
			if val, ok := statusResp[key]; ok {
				// Formatage spécial pour certaines valeurs
				displayVal := fmt.Sprintf("%v", val)
				if key == "timestamp" {
					if tStr, ok := val.(string); ok {
						parsedTime, pErr := time.Parse(time.RFC3339Nano, tStr)
						if pErr == nil {
							displayVal = parsedTime.Local().Format("15:04:05 MST")
						}
					}
				} else if key == "estimated_uptime" {
					// La valeur est probablement un nombre (secondes) ou une string "Xs"
					if uptimeFloat, ok := val.(float64); ok {
						displayVal = time.Duration(uptimeFloat * float64(time.Second)).Round(time.Second).String()
					} else if uptimeStr, ok := val.(string); ok {
						// Essayer de parser la durée string "Xs"
						dur, pErr := time.ParseDuration(uptimeStr)
						if pErr == nil {
							displayVal = dur.Round(time.Second).String()
						} else {
							displayVal = uptimeStr
						} // Afficher brut si parse échoue
					}
				} else if key == "battery_level_percent" {
					displayVal = fmt.Sprintf("%.0f%%", val)
				} else if key == "current_consumption_mw" {
					displayVal = fmt.Sprintf("%.1f mW", val)
				} else if key == "eco_score" {
					displayVal = fmt.Sprintf("%.2f", val)
				}
				fmt.Printf("%-25s: %s\n", key, displayVal)
			}
		}
		// Afficher les autres clés si elles existent
		fmt.Println("---------------------------")
		for key, val := range statusResp {
			found := false
			for _, knownKey := range keysToShow {
				if key == knownKey {
					found = true
					break
				}
			}
			if !found {
				fmt.Printf("%-25s: %v\n", key, val)
			}
		}

	},
}

func init() {
	rootCmd.AddCommand(energyCmd)         // Attacher 'energy' à la racine
	energyCmd.AddCommand(energyStatusCmd) // Attacher 'status' à 'energy'
}
