// Package mock is a fully working in-memory VehicleProvider.
// It lets the whole system run end-to-end today, before any official
// manufacturer API is connected. Real adapters (tesla, byd, ...) replace
// it brand by brand without changing the app or the API layer.
package mock

import (
	"context"
	"math"
	"sync"
	"time"

	"voltdrive/backend/internal/provider"
)

type vehicle struct {
	snap provider.Snapshot
}

// Provider is a thread-safe, simulated fleet of vehicles.
type Provider struct {
	mu       sync.RWMutex
	vehicles map[string]*vehicle
}

// New returns a mock provider seeded with the VoltDrive demo cars.
func New() *Provider {
	now := time.Now().Unix()
	p := &Provider{vehicles: map[string]*vehicle{}}
	p.vehicles["voyah-001"] = &vehicle{snap: provider.Snapshot{
		VehicleID: "voyah-001", Name: "Voyah Free", Online: true,
		Lock: provider.Locked, EngineOn: false,
		Energy:   provider.EnergyState{BatteryLevel: 78, RangeKm: 187, FuelPercent: 0},
		Climate:  provider.ClimateState{On: false, TargetC: 22, InsideC: 19, OutsideC: 14},
		Location: provider.Location{Lat: 41.311081, Lng: 69.240562, Heading: 90, UpdatedAt: now},
		Health:   provider.Health{OdometerKm: 12480, TirePressures: [4]int{230, 230, 228, 229}, ServiceDueKm: 4500},
		UpdatedAt: now,
	}}
	p.vehicles["deepal-002"] = &vehicle{snap: provider.Snapshot{
		VehicleID: "deepal-002", Name: "Deepal S07", Online: true,
		Lock: provider.Locked, EngineOn: false,
		Energy:   provider.EnergyState{BatteryLevel: 54, RangeKm: 142, FuelPercent: 0},
		Climate:  provider.ClimateState{On: false, TargetC: 21, InsideC: 18, OutsideC: 14},
		Location: provider.Location{Lat: 41.299496, Lng: 69.268408, Heading: 180, UpdatedAt: now},
		Health:   provider.Health{OdometerKm: 8120, TirePressures: [4]int{232, 231, 230, 230}, ServiceDueKm: 6800},
		UpdatedAt: now,
	}}
	return p
}

func (p *Provider) Brand() string { return "mock" }

func (p *Provider) get(id string) (*vehicle, error) {
	v, ok := p.vehicles[id]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return v, nil
}

// Snapshot recomputes a couple of live-ish values so the UI feels real.
func (p *Provider) Snapshot(_ context.Context, id string) (provider.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, err := p.get(id)
	if err != nil {
		return provider.Snapshot{}, err
	}
	now := time.Now().Unix()
	// Cabin temperature drifts toward target when climate is on.
	if v.snap.Climate.On {
		diff := v.snap.Climate.TargetC - v.snap.Climate.InsideC
		v.snap.Climate.InsideC += math.Round(diff*0.2*10) / 10
	}
	v.snap.UpdatedAt = now
	v.snap.Location.UpdatedAt = now
	return v.snap, nil
}

func (p *Provider) Lock(_ context.Context, id string) error {
	return p.mutate(id, func(v *vehicle) error { v.snap.Lock = provider.Locked; return nil })
}

func (p *Provider) Unlock(_ context.Context, id string) error {
	return p.mutate(id, func(v *vehicle) error { v.snap.Lock = provider.Unlocked; return nil })
}

func (p *Provider) RemoteStart(_ context.Context, id string) error {
	return p.mutate(id, func(v *vehicle) error { v.snap.EngineOn = true; return nil })
}

func (p *Provider) RemoteStop(_ context.Context, id string) error {
	return p.mutate(id, func(v *vehicle) error { v.snap.EngineOn = false; return nil })
}

func (p *Provider) SetClimate(_ context.Context, id string, on bool, targetC float64) error {
	return p.mutate(id, func(v *vehicle) error {
		v.snap.Climate.On = on
		if targetC > 0 {
			v.snap.Climate.TargetC = targetC
		}
		return nil
	})
}

func (p *Provider) mutate(id string, fn func(*vehicle) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, err := p.get(id)
	if err != nil {
		return err
	}
	if err := fn(v); err != nil {
		return err
	}
	v.snap.UpdatedAt = time.Now().Unix()
	return nil
}
