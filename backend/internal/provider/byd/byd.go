// Package byd implements provider.VehicleProvider for BYD vehicles. Works today
// via the embedded simulator; the real BYD fleet API (B2B contract) drops in by
// replacing each a.sim.X(...) call with an HTTP request.
package byd

import (
	"context"
	"net/http"
	"time"

	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/sim"
)

// Config holds the BYD fleet API settings (from Secret Manager).
type Config struct {
	BaseURL     string
	TokenSource func(ctx context.Context) (string, error)
}

// Adapter talks to BYD. Simulator-backed until the real API is wired.
type Adapter struct {
	cfg    Config
	client *http.Client
	sim    *sim.Engine
}

// New returns a working BYD adapter.
func New(cfg Config) *Adapter {
	seed := []provider.Snapshot{{
		VehicleID: "byd-004", Name: "BYD Han", Online: true,
		Lock: provider.Locked, EngineOn: false,
		Energy:   provider.EnergyState{BatteryLevel: 90, RangeKm: 410},
		Climate:  provider.ClimateState{TargetC: 23, InsideC: 22, OutsideC: 14},
		Location: provider.Location{Lat: 40.784, Lng: 72.34, Heading: 270},
		Health:   provider.Health{OdometerKm: 5300, TirePressures: [4]int{235, 235, 234, 234}, ServiceDueKm: 9700},
	}}
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}, sim: sim.New("byd", seed)}
}

func (a *Adapter) Brand() string { return "byd" }
func (a *Adapter) live() bool    { return a.cfg.BaseURL != "" && a.cfg.TokenSource != nil }

func (a *Adapter) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	// if a.live() { return a.snapshotHTTP(ctx, id) }  // TODO: real BYD fleet API
	return a.sim.Snapshot(ctx, id)
}
func (a *Adapter) Lock(ctx context.Context, id string) error        { return a.sim.Lock(ctx, id) }
func (a *Adapter) Unlock(ctx context.Context, id string) error      { return a.sim.Unlock(ctx, id) }
func (a *Adapter) RemoteStart(ctx context.Context, id string) error { return a.sim.RemoteStart(ctx, id) }
func (a *Adapter) RemoteStop(ctx context.Context, id string) error  { return a.sim.RemoteStop(ctx, id) }
func (a *Adapter) SetClimate(ctx context.Context, id string, on bool, t float64) error {
	return a.sim.SetClimate(ctx, id, on, t)
}

// --- Auxiliary controls (delegated to the simulator until the real API is wired) ---
func (a *Adapter) SetLights(ctx context.Context, id string, on bool) error {
	return a.sim.SetLights(ctx, id, on)
}
func (a *Adapter) SetTrunk(ctx context.Context, id string, open bool) error {
	return a.sim.SetTrunk(ctx, id, open)
}
func (a *Adapter) Honk(ctx context.Context, id string) error {
	return a.sim.Honk(ctx, id)
}
func (a *Adapter) SetSeat(ctx context.Context, id string, seat provider.SeatCmd) error {
	return a.sim.SetSeat(ctx, id, seat)
}
