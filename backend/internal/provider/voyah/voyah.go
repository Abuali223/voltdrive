// Package voyah implements provider.VehicleProvider for Voyah (Dongfeng's
// premium EV brand).
//
// It WORKS today via an embedded simulator (so the whole app is demonstrable),
// and is structured so the real Voyah cloud API drops in method-by-method.
//
// Connecting the real API (when granted by Voyah HQ / the official distributor):
//  1. Put the base URL + OAuth token source in Config (loaded from Secret Manager).
//  2. Replace each `a.sim.X(...)` call below with the real HTTP request, e.g.
//        GET  {BaseURL}/vehicle/{vin}/status            -> Snapshot
//        POST {BaseURL}/vehicle/{vin}/command/door       -> Lock/Unlock
//        POST {BaseURL}/vehicle/{vin}/command/engine     -> RemoteStart/Stop
//        POST {BaseURL}/vehicle/{vin}/command/hvac        -> SetClimate
//     using a.do(...) with the Authorization: Bearer token.
package voyah

import (
	"context"
	"net/http"
	"time"

	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/sim"
)

// Config holds the Voyah cloud API settings (from Secret Manager).
type Config struct {
	BaseURL     string
	TokenSource func(ctx context.Context) (string, error)
}

// Adapter talks to Voyah. Until the real API is wired, it serves the simulator.
type Adapter struct {
	cfg    Config
	client *http.Client
	sim    *sim.Engine
}

// New returns a working Voyah adapter (simulator-backed until the real API is wired).
func New(cfg Config) *Adapter {
	seed := []provider.Snapshot{{
		VehicleID: "voyah-001", Name: "Voyah Free", Online: true,
		Lock: provider.Locked, EngineOn: false,
		Energy:   provider.EnergyState{BatteryLevel: 78, RangeKm: 187},
		Climate:  provider.ClimateState{TargetC: 22, InsideC: 19, OutsideC: 14},
		Location: provider.Location{Lat: 41.311081, Lng: 69.240562, Heading: 90},
		Health:   provider.Health{OdometerKm: 12480, TirePressures: [4]int{230, 230, 228, 229}, ServiceDueKm: 4500},
	}}
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}, sim: sim.New("voyah", seed)}
}

func (a *Adapter) Brand() string { return "voyah" }

// live reports whether real API credentials are configured.
func (a *Adapter) live() bool { return a.cfg.BaseURL != "" && a.cfg.TokenSource != nil }

func (a *Adapter) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	// if a.live() { return a.snapshotHTTP(ctx, id) }  // TODO: real Voyah API
	return a.sim.Snapshot(ctx, id)
}
func (a *Adapter) Lock(ctx context.Context, id string) error        { return a.sim.Lock(ctx, id) }
func (a *Adapter) Unlock(ctx context.Context, id string) error      { return a.sim.Unlock(ctx, id) }
func (a *Adapter) RemoteStart(ctx context.Context, id string) error { return a.sim.RemoteStart(ctx, id) }
func (a *Adapter) RemoteStop(ctx context.Context, id string) error  { return a.sim.RemoteStop(ctx, id) }
func (a *Adapter) SetClimate(ctx context.Context, id string, on bool, t float64) error {
	return a.sim.SetClimate(ctx, id, on, t)
}
