// pkg/energy/linux_watcher.go
package energy

import (
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sysPowerSupplyPath = "/sys/class/power_supply"

// LinuxWatcher lit les informations depuis /sys/class/power_supply.
type LinuxWatcher struct {
	mu             sync.RWMutex
	profile        EcoProfile
	lastStatus     Status
	stopChan       chan struct{}
	wg             sync.WaitGroup
	updateInterval time.Duration

	// Chemins détectés pour les composants
	batteryPath string
	acPath      string
}

// NewLinuxWatcher tente de créer un watcher basé sur sysfs.
// Retourne nil, nil si sysfs n'est pas utilisable ou si aucune batterie/AC n'est trouvé.
func NewLinuxWatcher(interval time.Duration) (*LinuxWatcher, error) {
	if _, err := os.Stat(sysPowerSupplyPath); os.IsNotExist(err) {
		log.Println("ENERGY(Linux): Répertoire /sys/class/power_supply non trouvé. Watcher Linux non disponible.")
		return nil, nil // Pas une erreur fatale, juste non disponible
	}

	lw := &LinuxWatcher{
		updateInterval: interval,
		stopChan:       make(chan struct{}),
		profile: EcoProfile{
			EfficiencyFactor: 1.0, // Peut être affiné plus tard
		},
	}

	// Détecter les composants AC et Batterie
	entries, err := ioutil.ReadDir(sysPowerSupplyPath)
	if err != nil {
		log.Printf("ENERGY(Linux): Erreur lecture de %s: %v. Watcher Linux non disponible.", sysPowerSupplyPath, err)
		return nil, nil
	}

	foundAC := false
	foundBattery := false
	for _, entry := range entries {
		entryPath := filepath.Join(sysPowerSupplyPath, entry.Name())
		typePath := filepath.Join(entryPath, "type")
		typeBytes, err := ioutil.ReadFile(typePath)
		if err != nil {
			continue // Ignorer les entrées sans fichier 'type'
		}
		devType := strings.TrimSpace(string(typeBytes))

		switch devType {
		case "Mains": // Nom commun pour l'adaptateur secteur
			if !foundAC {
				log.Printf("ENERGY(Linux): Adaptateur secteur détecté : %s", entry.Name())
				lw.acPath = entryPath
				foundAC = true
			}
		case "Battery":
			if !foundBattery {
				log.Printf("ENERGY(Linux): Batterie détectée : %s", entry.Name())
				lw.batteryPath = entryPath
				lw.profile.HasBattery = true
				// Essayer de lire la capacité max (peut échouer)
				capWH, _ := readFloatFromFile(filepath.Join(entryPath, "charge_full_design")) // ou energy_full_design ? en µWh
				capAh, _ := readFloatFromFile(filepath.Join(entryPath, "charge_full"))        // en µAh ?
				voltage, _ := readFloatFromFile(filepath.Join(entryPath, "voltage_now"))      // en µV
				if capWH > 0 {
					lw.profile.MaxCapacityWH = capWH / 1000000.0 // Convertir µWh en Wh
				} else if capAh > 0 && voltage > 0 {
					// Estimer Wh = (Ah * V)
					lw.profile.MaxCapacityWH = (capAh / 1000000.0) * (voltage / 1000000.0)
				}
				log.Printf("ENERGY(Linux): Capacité batterie estimée: %.2f Wh\n", lw.profile.MaxCapacityWH)
				foundBattery = true
			}
			// TODO: Gérer le solaire ? Nécessite un moyen de détection standardisé.
		}
	}

	if !foundAC && !foundBattery {
		log.Println("ENERGY(Linux): Aucun adaptateur secteur ou batterie trouvé dans sysfs. Watcher Linux non disponible.")
		return nil, nil
	}

	// Lire l'état initial
	status, err := lw.readAndUpdateStatus()
	if err != nil {
		log.Printf("ENERGY(Linux): Erreur lors de la lecture de l'état initial: %v. Watcher Linux non disponible.", err)
		return nil, nil // Ne pas démarrer si la lecture initiale échoue
	}
	lw.lastStatus = status

	log.Println("ENERGY(Linux): Watcher initialisé avec succès.")
	return lw, nil
}

// Start lance la surveillance périodique.
func (lw *LinuxWatcher) Start() error {
	log.Println("ENERGY(Linux): Démarrage de la surveillance sysfs...")
	lw.wg.Add(1)
	go lw.runUpdateLoop()
	return nil
}

// Stop arrête la surveillance.
func (lw *LinuxWatcher) Stop() error {
	log.Println("ENERGY(Linux): Arrêt de la surveillance sysfs...")
	close(lw.stopChan)
	lw.wg.Wait()
	log.Println("ENERGY(Linux): Surveillance arrêtée.")
	return nil
}

// GetCurrentStatus retourne le dernier état lu.
func (lw *LinuxWatcher) GetCurrentStatus() (Status, error) {
	lw.mu.RLock()
	defer lw.mu.RUnlock()
	statusCopy := lw.lastStatus
	statusCopy.Timestamp = time.Now() // Mettre à jour le timestamp
	return statusCopy, nil
}

// GetHardwareProfile retourne le profil matériel détecté.
func (lw *LinuxWatcher) GetHardwareProfile() EcoProfile {
	return lw.profile
}

// runUpdateLoop lit périodiquement les informations sysfs.
func (lw *LinuxWatcher) runUpdateLoop() {
	defer lw.wg.Done()
	ticker := time.NewTicker(lw.updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lw.stopChan:
			return
		case <-ticker.C:
			_, err := lw.readAndUpdateStatus()
			if err != nil {
				log.Printf("ENERGY(Linux): Erreur lors de la mise à jour du statut: %v", err)
				// Que faire ? Continuer ? Arrêter le watcher ?
				// Pour l'instant, on continue en espérant que ça redevienne lisible.
			}
		}
	}
}

// readAndUpdateStatus lit les fichiers sysfs et met à jour l'état interne.
func (lw *LinuxWatcher) readAndUpdateStatus() (Status, error) {
	status := Status{Timestamp: time.Now()}
	var battStatusStr string
	// var err error

	// 1. Déterminer la source et l'état de charge
	if lw.acPath != "" {
		online, _ := readIntFromFile(filepath.Join(lw.acPath, "online"))
		if online == 1 {
			status.Source = SourceAC
			// Si sur AC, vérifier l'état de la batterie pour savoir si elle charge
			if lw.batteryPath != "" {
				battStatusStr, _ = readStringFromFile(filepath.Join(lw.batteryPath, "status"))
				status.IsCharging = (battStatusStr == "Charging")
			}
		} else {
			// Pas sur AC, on est sur Batterie (ou inconnu si pas de batterie)
			if lw.batteryPath != "" {
				status.Source = SourceBattery
				battStatusStr, _ = readStringFromFile(filepath.Join(lw.batteryPath, "status"))
				status.IsCharging = (battStatusStr == "Charging") // Peut charger via USB?
				// Si le status est inconnu ou "Discharging", on n'est pas en charge.
				if battStatusStr != "Charging" {
					status.IsCharging = false
				}
			} else {
				status.Source = SourceUnknown // Pas d'AC, pas de batterie
			}
		}
	} else if lw.batteryPath != "" {
		// Pas d'AC détecté, mais une batterie
		status.Source = SourceBattery
		battStatusStr, _ = readStringFromFile(filepath.Join(lw.batteryPath, "status"))
		status.IsCharging = (battStatusStr == "Charging")
		if battStatusStr != "Charging" {
			status.IsCharging = false
		}
	} else {
		status.Source = SourceUnknown // Rien détecté
	}

	// 2. Lire le niveau de batterie
	if lw.profile.HasBattery && lw.batteryPath != "" {
		capacity, errCap := readIntFromFile(filepath.Join(lw.batteryPath, "capacity"))
		if errCap == nil {
			status.BatteryLevel = uint8(capacity)
		} else {
			log.Printf("ENERGY(Linux): Avertissement - Impossible de lire la capacité de la batterie: %v", errCap)
			// Garder l'ancienne valeur? Mettre à 0? Pour l'instant, on laisse la valeur par défaut 0.
		}
	}

	// 3. Simuler la consommation actuelle
	// Très difficile à obtenir précisément sans RAPL/compteurs spécifiques.
	// Simuler basé sur la source et l'état de charge.
	baseConsumption := float32(500) // 0.5W sur AC par défaut
	if status.Source == SourceBattery {
		if status.IsCharging {
			baseConsumption = 100
		} else {
			baseConsumption = 300
		} // Moins sur batterie, très peu si en charge?
	} else if status.Source == SourceUnknown {
		baseConsumption = 200 // Faible conso si source inconnue
	}
	status.CurrentConsumptionMW = baseConsumption + rand.Float32()*200 // Variation

	// 4. Estimer l'autonomie (très approximatif)
	if status.Source == SourceBattery && !status.IsCharging && lw.profile.MaxCapacityWH > 0 {
		currentEnergyWH := (float32(status.BatteryLevel) / 100.0) * lw.profile.MaxCapacityWH
		consumptionW := status.CurrentConsumptionMW / 1000.0
		if consumptionW > 0.01 { // Éviter division par zéro/valeurs infimes
			remainingHours := currentEnergyWH / consumptionW
			status.EstimatedUptime = time.Duration(remainingHours * float32(time.Hour))
		}
	}

	// 5. Calculer l'EcoScore
	if status.Source == SourceAC {
		status.EcoScore = 0.9
	} else if status.Source == SourceBattery {
		// Score linéaire basé sur niveau batterie, pénalité si décharge rapide?
		status.EcoScore = 0.1 + (float32(status.BatteryLevel)/100.0)*0.7
		// Ajouter une pénalité si la décharge est rapide (autonomie faible) ?
		if status.EstimatedUptime > 0 && status.EstimatedUptime < 1*time.Hour {
			status.EcoScore *= 0.8 // Réduire le score si moins d'1h restante
		}
	} else {
		status.EcoScore = 0.3 // Score faible si source inconnue
	}

	// Mettre à jour l'état interne
	lw.mu.Lock()
	lw.lastStatus = status
	lw.mu.Unlock()

	return status, nil // Retourner le statut lu (erreur sera nil si on arrive ici)
}

// --- Helpers pour lire les fichiers sysfs ---

func readStringFromFile(filePath string) (string, error) {
	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readIntFromFile(filePath string) (int64, error) {
	strVal, err := readStringFromFile(filePath)
	if err != nil {
		return 0, err
	}
	intVal, err := strconv.ParseInt(strVal, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing int from '%s' in %s: %w", strVal, filePath, err)
	}
	return intVal, nil
}

func readFloatFromFile(filePath string) (float32, error) {
	strVal, err := readStringFromFile(filePath)
	if err != nil {
		return 0, err
	}
	floatVal, err := strconv.ParseFloat(strVal, 32)
	if err != nil {
		return 0, fmt.Errorf("parsing float from '%s' in %s: %w", strVal, filePath, err)
	}
	return float32(floatVal), nil
}
