// Package provider defines the universal vehicle-control interface.
// Every car brand (Tesla, BYD, Voyah, ...) is implemented as an adapter
// that satisfies this interface. The rest of the app never depends on a
// specific brand — only on VehicleProvider.
package provider

import (
	"context"
	"errors"
)

// ErrUnsupported is returned when a brand's API does not expose a command
// (e.g. some cars do not support remote engine start).
var ErrUnsupported = errors.New("command not supported by this vehicle")

// ErrNotFound is returned when a vehicle id is unknown to the provider.
var ErrNotFound = errors.New("vehicle not found")

// LockState describes whether the doors are locked.
type LockState string

const (
	Locked   LockState = "locked"
	Unlocked LockState = "unlocked"
)

// Location is the vehicle GPS position.
type Location struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Heading   float64 `json:"heading"`   // degrees 0-359
	Speed     float64 `json:"speed"`     // km/h
	UpdatedAt int64   `json:"updatedAt"` // unix seconds
}

// Battery / fuel state. For ICE cars Battery* fields are zero and Fuel is used.
type EnergyState struct {
	BatteryLevel   int     `json:"batteryLevel"`   // percent 0-100 (EV/hybrid)
	RangeKm        int     `json:"rangeKm"`        // estimated remaining range
	Charging       bool    `json:"charging"`       // currently charging
	ChargeRateKw   float64 `json:"chargeRateKw"`   // current charge power
	FuelPercent    int     `json:"fuelPercent"`    // ICE/hybrid fuel level
	PluggedIn      bool    `json:"pluggedIn"`      // cable connected
}

// ClimateState describes HVAC.
type ClimateState struct {
	On            bool    `json:"on"`
	TargetC       float64 `json:"targetC"`
	InsideC       float64 `json:"insideC"`
	OutsideC      float64 `json:"outsideC"`
	DefrostOn     bool    `json:"defrostOn"`
}

// Health is technical condition / maintenance info.
type Health struct {
	OdometerKm     int      `json:"odometerKm"`
	TirePressures  [4]int   `json:"tirePressures"` // kPa: FL, FR, RL, RR
	ServiceDueKm   int      `json:"serviceDueKm"`
	Faults         []string `json:"faults"` // active diagnostic trouble codes
}

// Snapshot is the full vehicle state returned to the app in one call.
type Snapshot struct {
	VehicleID string       `json:"vehicleId"`
	Name      string       `json:"name"`
	Online    bool         `json:"online"`
	Lock      LockState    `json:"lock"`
	EngineOn  bool         `json:"engineOn"`
	Energy    EnergyState  `json:"energy"`
	Climate   ClimateState `json:"climate"`
	Location  Location     `json:"location"`
	Health    Health       `json:"health"`
	UpdatedAt int64        `json:"updatedAt"`
}

// VehicleProvider is the universal contract every brand adapter implements.
// All methods take a context (for timeout/cancel) and a vehicleID.
type VehicleProvider interface {
	// Brand returns the adapter name, e.g. "tesla", "byd", "mock".
	Brand() string

	// Snapshot returns the full current state of the vehicle.
	Snapshot(ctx context.Context, vehicleID string) (Snapshot, error)

	// Lock / Unlock the doors.
	Lock(ctx context.Context, vehicleID string) error
	Unlock(ctx context.Context, vehicleID string) error

	// RemoteStart / RemoteStop the engine/drive system.
	RemoteStart(ctx context.Context, vehicleID string) error
	RemoteStop(ctx context.Context, vehicleID string) error

	// Climate control: on/off with a target temperature in Celsius.
	SetClimate(ctx context.Context, vehicleID string, on bool, targetC float64) error
}
