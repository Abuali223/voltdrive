// Package deepal implements provider.VehicleProvider for Deepal (Changan's EV brand).
//
// It serves the simulator until real credentials are configured, then switches
// to the live OEM cloud API via the generic oemrest client (adjust oemrest's
// paths + field tags to the official deepal API spec).
package deepal

import (
	"context"

	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/oemrest"
	"voltdrive/backend/internal/provider/sim"
)

// Config holds the deepal cloud API settings (from Secret Manager).
type Config struct {
	BaseURL     string
	TokenSource func(ctx context.Context) (string, error)
}

// backend is the method set both the simulator and the live REST client share.
type backend interface {
	Snapshot(ctx context.Context, id string) (provider.Snapshot, error)
	Lock(ctx context.Context, id string) error
	Unlock(ctx context.Context, id string) error
	RemoteStart(ctx context.Context, id string) error
	RemoteStop(ctx context.Context, id string) error
	SetClimate(ctx context.Context, id string, on bool, targetC float64) error
	SetLights(ctx context.Context, id string, on bool) error
	SetTrunk(ctx context.Context, id string, open bool) error
	Honk(ctx context.Context, id string) error
	SetSeat(ctx context.Context, id string, seat provider.SeatCmd) error
}

// Adapter serves the simulator until real credentials switch it to the live API.
type Adapter struct {
	sim  *sim.Engine
	rest *oemrest.Client
}

// New returns a working deepal adapter. With cfg.BaseURL set it talks to the
// real cloud API; otherwise it runs the simulator.
func New(cfg Config) *Adapter {
	seed := []provider.Snapshot{
		{
			VehicleID: "deepal-002", Name: "Deepal S07", Online: true,
			Lock: provider.Locked, EngineOn: false,
			Energy:   provider.EnergyState{BatteryLevel: 54, RangeKm: 142},
			Climate:  provider.ClimateState{TargetC: 21, InsideC: 18, OutsideC: 14},
			Location: provider.Location{Lat: 40.7835, Lng: 72.346, Heading: 180},
			Health:   provider.Health{OdometerKm: 8120, TirePressures: [4]int{232, 231, 230, 230}, ServiceDueKm: 6800},
		},
		{
			VehicleID: "deepal-005", Name: "Deepal SL03", Online: true,
			Lock: provider.Locked, EngineOn: false,
			Energy:   provider.EnergyState{BatteryLevel: 71, RangeKm: 305},
			Climate:  provider.ClimateState{TargetC: 22, InsideC: 20, OutsideC: 14},
			Location: provider.Location{Lat: 40.792, Lng: 72.351, Heading: 90},
			Health:   provider.Health{OdometerKm: 14200, TirePressures: [4]int{230, 230, 229, 229}, ServiceDueKm: 800},
		},
		{
			VehicleID: "deepal-006", Name: "Deepal S05", Online: true,
			Lock: provider.Unlocked, EngineOn: false,
			Energy:   provider.EnergyState{BatteryLevel: 48, RangeKm: 198},
			Climate:  provider.ClimateState{TargetC: 23, InsideC: 19, OutsideC: 14},
			Location: provider.Location{Lat: 40.776, Lng: 72.338, Heading: 270},
			Health:   provider.Health{OdometerKm: 5300, TirePressures: [4]int{233, 232, 231, 231}, ServiceDueKm: 9700},
		},
		{
			VehicleID: "deepal-007", Name: "Deepal S09", Online: true,
			Lock: provider.Locked, EngineOn: false,
			Energy:   provider.EnergyState{BatteryLevel: 88, RangeKm: 520},
			Climate:  provider.ClimateState{TargetC: 21, InsideC: 21, OutsideC: 14},
			Location: provider.Location{Lat: 40.801, Lng: 72.36, Heading: 45},
			Health:   provider.Health{OdometerKm: 2100, TirePressures: [4]int{235, 235, 234, 234}, ServiceDueKm: 11900},
		},
	}
	a := &Adapter{sim: sim.New("deepal", seed)}
	if cfg.BaseURL != "" && cfg.TokenSource != nil {
		a.rest = oemrest.New(cfg.BaseURL, cfg.TokenSource)
	}
	return a
}

func (a *Adapter) Brand() string { return "deepal" }

func (a *Adapter) be() backend {
	if a.rest != nil {
		return a.rest
	}
	return a.sim
}

func (a *Adapter) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	return a.be().Snapshot(ctx, id)
}
func (a *Adapter) Lock(ctx context.Context, id string) error   { return a.be().Lock(ctx, id) }
func (a *Adapter) Unlock(ctx context.Context, id string) error { return a.be().Unlock(ctx, id) }
func (a *Adapter) RemoteStart(ctx context.Context, id string) error {
	return a.be().RemoteStart(ctx, id)
}
func (a *Adapter) RemoteStop(ctx context.Context, id string) error { return a.be().RemoteStop(ctx, id) }
func (a *Adapter) SetClimate(ctx context.Context, id string, on bool, t float64) error {
	return a.be().SetClimate(ctx, id, on, t)
}
func (a *Adapter) SetLights(ctx context.Context, id string, on bool) error {
	return a.be().SetLights(ctx, id, on)
}
func (a *Adapter) SetTrunk(ctx context.Context, id string, open bool) error {
	return a.be().SetTrunk(ctx, id, open)
}
func (a *Adapter) Honk(ctx context.Context, id string) error { return a.be().Honk(ctx, id) }
func (a *Adapter) SetSeat(ctx context.Context, id string, seat provider.SeatCmd) error {
	return a.be().SetSeat(ctx, id, seat)
}
