// Package deepal implements provider.VehicleProvider for Deepal (Changan's EV
// brand). Works today via the embedded simulator; the real Changan/Deepal
// cloud API drops in by replacing each a.sim.X(...) call with an HTTP request.
package deepal

import (
	"context"
	"net/http"
	"time"

	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/sim"
)

// Config holds the Deepal cloud API settings (from Secret Manager).
type Config struct {
	BaseURL     string
	TokenSource func(ctx context.Context) (string, error)
}

// Adapter talks to Deepal. Simulator-backed until the real API is wired.
type Adapter struct {
	cfg    Config
	client *http.Client
	sim    *sim.Engine
}

// New returns a working Deepal adapter.
func New(cfg Config) *Adapter {
	seed := []provider.Snapshot{{
		VehicleID: "deepal-002", Name: "Deepal S07", Online: true,
		Lock: provider.Locked, EngineOn: false,
		Energy:   provider.EnergyState{BatteryLevel: 54, RangeKm: 142},
		Climate:  provider.ClimateState{TargetC: 21, InsideC: 18, OutsideC: 14},
		Location: provider.Location{Lat: 41.299496, Lng: 69.268408, Heading: 180},
		Health:   provider.Health{OdometerKm: 8120, TirePressures: [4]int{232, 231, 230, 230}, ServiceDueKm: 6800},
	}}
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}, sim: sim.New("deepal", seed)}
}

func (a *Adapter) Brand() string { return "deepal" }
func (a *Adapter) live() bool    { return a.cfg.BaseURL != "" && a.cfg.TokenSource != nil }

func (a *Adapter) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	// if a.live() { return a.snapshotHTTP(ctx, id) }  // TODO: real Deepal API
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
