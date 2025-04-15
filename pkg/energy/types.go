// pkg/energy/types.go
package energy

import "time"

// PowerSource indique la source d'alimentation actuelle.
type PowerSource string

const (
	SourceUnknown  PowerSource = "unknown"
	SourceAC       PowerSource = "ac" // Secteur
	SourceBattery  PowerSource = "battery"
	SourceSolar    PowerSource = "solar" // Solaire (peut être combiné avec batterie)
	SourceUSBPower PowerSource = "usb"   // Alimenté par USB (peut charger ou non)
)

// EcoProfile représente les capacités énergétiques matérielles.
// Simplifié pour l'instant.
type EcoProfile struct {
	HasBattery       bool    `json:"has_battery"`
	HasSolar         bool    `json:"has_solar"`
	MaxCapacityWH    float32 `json:"max_capacity_wh,omitempty"` // Capacité batterie en Watt-heures
	EfficiencyFactor float32 `json:"efficiency_factor"`         // Facteur général (1.0 = normal)
}

// Status représente l'état énergétique actuel du nœud.
type Status struct {
	Timestamp            time.Time     `json:"timestamp"`
	Source               PowerSource   `json:"source"`
	BatteryLevel         uint8         `json:"battery_level_percent,omitempty"` // 0-100, si batterie présente
	IsCharging           bool          `json:"is_charging,omitempty"`           // Si sur batterie et en charge
	CurrentConsumptionMW float32       `json:"current_consumption_mw"`          // Consommation instantanée en milliwatts
	EstimatedUptime      time.Duration `json:"estimated_uptime,omitempty"`      // Durée restante estimée sur batterie
	EcoScore             float32       `json:"eco_score"`                       // Score combiné (0.0 - 1.0)
}

// Watcher définit l'interface pour surveiller l'état énergétique.
type Watcher interface {
	Start() error                      // Démarre la surveillance (si nécessaire)
	Stop() error                       // Arrête la surveillance
	GetCurrentStatus() (Status, error) // Obtient l'état actuel
	GetHardwareProfile() EcoProfile    // Obtient les caractéristiques matérielles
	// PredictDuration(availableJoules float32) (time.Duration, error) // Gardé pour plus tard
	// EnterLowPowerMode() error // Gardé pour plus tard
}

// TODO: Définir la structure EnergyInfo utilisée dans le DHT (spec 3.2)
// type DHTEnergyInfo struct { ... }
