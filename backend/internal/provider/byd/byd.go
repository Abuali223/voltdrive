// Package byd implements provider.VehicleProvider for BYD.
//
// It serves the simulator until real credentials are configured, then switches
// to the live OEM cloud API via the generic oemrest client (adjust oemrest's
// paths + field tags to the official byd API spec).
package byd

import (
	"context"

	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/oemrest"
	"voltdrive/backend/internal/provider/sim"
)

// Config holds the byd cloud API settings (from Secret Manager).
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

// New returns a working byd adapter. With cfg.BaseURL set it talks to the
// real cloud API; otherwise it runs the simulator.
func New(cfg Config) *Adapter {
	seed := []provider.Snapshot{{
		VehicleID: "byd-004", Name: "BYD Han", Online: true,
		Lock: provider.Locked, EngineOn: false,
		Energy:   provider.EnergyState{BatteryLevel: 90, RangeKm: 410},
		Climate:  provider.ClimateState{TargetC: 23, InsideC: 22, OutsideC: 14},
		Location: provider.Location{Lat: 40.784, Lng: 72.34, Heading: 270},
		Health:   provider.Health{OdometerKm: 5300, TirePressures: [4]int{235, 235, 234, 234}, ServiceDueKm: 9700},
	}}
	a := &Adapter{sim: sim.New("byd", seed)}
	if cfg.BaseURL != "" && cfg.TokenSource != nil {
		a.rest = oemrest.New(cfg.BaseURL, cfg.TokenSource)
	}
	return a
}

func (a *Adapter) Brand() string { return "byd" }

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
