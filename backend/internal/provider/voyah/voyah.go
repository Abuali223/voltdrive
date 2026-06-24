// Package voyah implements provider.VehicleProvider for Voyah (Dongfeng's
// premium EV brand).
//
// It WORKS today via an embedded simulator, and switches to the real Voyah
// cloud API automatically the moment credentials are configured: pass a
// BaseURL + TokenSource in Config (from env / Secret Manager) and every call
// routes through the generic oemrest client instead of the simulator. Adjust
// oemrest's endpoint paths + field tags to the official Voyah API spec.
package voyah

import (
	"context"

	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/oemrest"
	"voltdrive/backend/internal/provider/sim"
)

// Config holds the Voyah cloud API settings (from Secret Manager).
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

// New returns a working Voyah adapter. With cfg.BaseURL set it talks to the
// real cloud API; otherwise it runs the simulator.
func New(cfg Config) *Adapter {
	seed := []provider.Snapshot{{
		VehicleID: "voyah-001", Name: "Voyah Free", Online: true,
		Lock: provider.Locked, EngineOn: false,
		Energy:   provider.EnergyState{BatteryLevel: 78, RangeKm: 187},
		Climate:  provider.ClimateState{TargetC: 22, InsideC: 19, OutsideC: 14},
		Location: provider.Location{Lat: 40.7821, Lng: 72.3442, Heading: 90},
		Health:   provider.Health{OdometerKm: 12480, TirePressures: [4]int{230, 230, 228, 229}, ServiceDueKm: 4500},
	}}
	a := &Adapter{sim: sim.New("voyah", seed)}
	if cfg.BaseURL != "" && cfg.TokenSource != nil {
		a.rest = oemrest.New(cfg.BaseURL, cfg.TokenSource)
	}
	return a
}

func (a *Adapter) Brand() string { return "voyah" }

// be picks the live REST client when configured, else the simulator.
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
