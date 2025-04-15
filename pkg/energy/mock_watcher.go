// pkg/energy/mock_watcher.go
package energy

import (
	"log"
	"math/rand"
	"sync"
	"time"
)

// MockWatcher simule un gestionnaire d'énergie.
type MockWatcher struct {
	mu            sync.RWMutex
	profile       EcoProfile
	currentStatus Status
	stopChan      chan struct{}
	wg            sync.WaitGroup
	startTime     time.Time
}

// NewMockWatcher crée un simulateur.
func NewMockWatcher() *MockWatcher {
	// Simuler un appareil avec batterie
	profile := EcoProfile{
		HasBattery:       true,
		HasSolar:         false, // Pas de solaire pour ce mock
		MaxCapacityWH:    10,    // Batterie de 10 Wh
		EfficiencyFactor: 1.0,
	}

	// État initial: Batterie pleine, sur secteur, consommation de base
	initialStatus := Status{
		Timestamp:            time.Now(),
		Source:               SourceAC, // Commence sur secteur
		BatteryLevel:         100,      // Batterie pleine
		IsCharging:           false,    // Pas en charge au début
		CurrentConsumptionMW: 500,      // Consommation de base (0.5W)
		EstimatedUptime:      0,        // Pas d'estimation sur AC
		EcoScore:             0.9,      // Bon score sur AC
	}

	return &MockWatcher{
		profile:       profile,
		currentStatus: initialStatus,
		stopChan:      make(chan struct{}),
		startTime:     time.Now(),
	}
}

// Start lance la simulation en arrière-plan.
func (mw *MockWatcher) Start() error {
	log.Println("ENERGY(Mock): Démarrage de la simulation énergétique...")
	mw.wg.Add(1)
	go mw.runSimulation()
	return nil
}

// Stop arrête la simulation.
func (mw *MockWatcher) Stop() error {
	log.Println("ENERGY(Mock): Arrêt de la simulation énergétique...")
	close(mw.stopChan)
	mw.wg.Wait()
	log.Println("ENERGY(Mock): Simulation arrêtée.")
	return nil
}

// GetCurrentStatus retourne l'état simulé actuel.
func (mw *MockWatcher) GetCurrentStatus() (Status, error) {
	mw.mu.RLock()
	defer mw.mu.RUnlock()
	// Retourner une copie pour éviter les modifications externes
	statusCopy := mw.currentStatus
	statusCopy.Timestamp = time.Now() // Mettre à jour le timestamp
	return statusCopy, nil
}

// GetHardwareProfile retourne le profil matériel simulé.
func (mw *MockWatcher) GetHardwareProfile() EcoProfile {
	// Le profil est immuable, pas besoin de mutex
	return mw.profile
}

// runSimulation met à jour l'état énergétique simulé périodiquement.
func (mw *MockWatcher) runSimulation() {
	defer mw.wg.Done()
	ticker := time.NewTicker(5 * time.Second) // Mise à jour toutes les 5 secondes
	defer ticker.Stop()

	// État interne de la simulation
	currentCapacityWH := mw.profile.MaxCapacityWH // Commence plein
	isOnAC := true                                // Commence sur secteur

	for {
		select {
		case <-mw.stopChan:
			return
		case <-ticker.C:
			mw.mu.Lock() // Verrouiller pour modifier l'état

			// Simuler des changements de source AC/Batterie
			if rand.Intn(20) == 0 { // 1 chance sur 20 de changer de source
				isOnAC = !isOnAC
				mw.currentStatus.Source = SourceBattery
				if isOnAC {
					mw.currentStatus.Source = SourceAC
					log.Println("ENERGY(Mock): Passage sur secteur (AC)")
				} else {
					log.Println("ENERGY(Mock): Passage sur batterie")
				}
			}

			// Simuler la consommation (varie un peu)
			baseConsumption := float32(500) // 0.5W
			if !isOnAC {
				baseConsumption = 300
			} // Moins sur batterie ?
			mw.currentStatus.CurrentConsumptionMW = baseConsumption + rand.Float32()*200 // Ajoute jusqu'à 0.2W de variation

			// Simuler charge/décharge
			if mw.profile.HasBattery {
				consumptionW := mw.currentStatus.CurrentConsumptionMW / 1000.0
				intervalHours := float32(5.0 / 3600.0) // 5 secondes en heures

				if isOnAC {
					// Sur secteur : charge si pas plein
					chargeRateW := float32(5.0) // Charge à 5W
					currentCapacityWH += (chargeRateW - consumptionW) * intervalHours
					if currentCapacityWH > mw.profile.MaxCapacityWH {
						currentCapacityWH = mw.profile.MaxCapacityWH
					}
					mw.currentStatus.IsCharging = currentCapacityWH < mw.profile.MaxCapacityWH
				} else {
					// Sur batterie : décharge
					mw.currentStatus.IsCharging = false
					currentCapacityWH -= consumptionW * intervalHours
					if currentCapacityWH < 0 {
						currentCapacityWH = 0
						// Batterie vide ! Passer en mode panique ?
						log.Println("ENERGY(Mock): Batterie vide !")
					}
				}

				// Mettre à jour le niveau de batterie
				if mw.profile.MaxCapacityWH > 0 {
					mw.currentStatus.BatteryLevel = uint8((currentCapacityWH / mw.profile.MaxCapacityWH) * 100)
				} else {
					mw.currentStatus.BatteryLevel = 0
				}

				// Estimer l'autonomie (très simplifié)
				if !isOnAC && consumptionW > 0 {
					remainingHours := currentCapacityWH / consumptionW
					mw.currentStatus.EstimatedUptime = time.Duration(remainingHours * float32(time.Hour))
				} else {
					mw.currentStatus.EstimatedUptime = 0 // Pas d'estimation sur AC ou si conso nulle
				}

				// Calculer EcoScore (exemple simple)
				// Priorise AC, puis batterie haute, pénalise batterie faible
				if isOnAC {
					mw.currentStatus.EcoScore = 0.9
				} else {
					// Score linéaire de 0.8 (100%) à 0.1 (0%)
					mw.currentStatus.EcoScore = 0.1 + (float32(mw.currentStatus.BatteryLevel)/100.0)*0.7
				}

			} else {
				// Pas de batterie
				mw.currentStatus.BatteryLevel = 0
				mw.currentStatus.IsCharging = false
				mw.currentStatus.EstimatedUptime = 0
				if isOnAC {
					mw.currentStatus.EcoScore = 0.9
				} else {
					mw.currentStatus.EcoScore = 0.5
				} // Score moyen si source inconnue/USB?
			}

			mw.mu.Unlock()
		}
	}
}
