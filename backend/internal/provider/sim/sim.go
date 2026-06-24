// Package sim is a reusable, thread-safe vehicle simulator. Every brand
// adapter embeds a sim.Engine so it WORKS end-to-end today (returns live-ish
// state, accepts commands) before the real manufacturer API is connected.
//
// When a brand's real API is granted, the adapter replaces its delegating
// method bodies with HTTP calls — the Engine stays as an offline fallback for
// development and tests.
package sim

import (
	"context"
	"math"
	"sync"
	"time"

	"voltdrive/backend/internal/provider"
)

// Engine holds a set of simulated vehicles for one brand.
type Engine struct {
	brand string
	mu    sync.RWMutex
	cars  map[string]*provider.Snapshot
	seats map[string]map[string]int // vehicleID -> seat -> recline angle
}

// New creates an Engine for a brand, seeded with the given vehicles.
func New(brand string, seed []provider.Snapshot) *Engine {
	e := &Engine{brand: brand, cars: map[string]*provider.Snapshot{}, seats: map[string]map[string]int{}}
	now := time.Now().Unix()
	for i := range seed {
		s := seed[i]
		s.UpdatedAt = now
		s.Location.UpdatedAt = now
		e.cars[s.VehicleID] = &s
	}
	return e
}

func (e *Engine) Brand() string { return e.brand }

func (e *Engine) get(id string) (*provider.Snapshot, error) {
	s, ok := e.cars[id]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return s, nil
}

// Snapshot returns the current state, nudging a couple of values so the UI
// feels alive (cabin temperature drifts toward the climate target).
func (e *Engine) Snapshot(_ context.Context, id string) (provider.Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.get(id)
	if err != nil {
		return provider.Snapshot{}, err
	}
	now := time.Now().Unix()
	if s.Climate.On {
		diff := s.Climate.TargetC - s.Climate.InsideC
		s.Climate.InsideC += math.Round(diff*0.2*10) / 10
	}
	s.UpdatedAt = now
	s.Location.UpdatedAt = now
	return *s, nil
}

func (e *Engine) Lock(_ context.Context, id string) error {
	return e.mutate(id, func(s *provider.Snapshot) { s.Lock = provider.Locked })
}
func (e *Engine) Unlock(_ context.Context, id string) error {
	return e.mutate(id, func(s *provider.Snapshot) { s.Lock = provider.Unlocked })
}
func (e *Engine) RemoteStart(_ context.Context, id string) error {
	return e.mutate(id, func(s *provider.Snapshot) { s.EngineOn = true })
}
func (e *Engine) RemoteStop(_ context.Context, id string) error {
	return e.mutate(id, func(s *provider.Snapshot) { s.EngineOn = false })
}
func (e *Engine) SetClimate(_ context.Context, id string, on bool, targetC float64) error {
	return e.mutate(id, func(s *provider.Snapshot) {
		s.Climate.On = on
		if targetC > 0 {
			s.Climate.TargetC = targetC
		}
	})
}

// --- Auxiliary capabilities (exterior lights, trunk, horn, seat memory) ---

func (e *Engine) SetLights(_ context.Context, id string, on bool) error {
	return e.mutate(id, func(s *provider.Snapshot) { s.LightsOn = on })
}
func (e *Engine) SetTrunk(_ context.Context, id string, open bool) error {
	return e.mutate(id, func(s *provider.Snapshot) { s.TrunkOpen = open })
}

// Honk is momentary (no persisted state) — it only validates the vehicle exists.
func (e *Engine) Honk(_ context.Context, id string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.cars[id]
	if !ok {
		return provider.ErrNotFound
	}
	return nil
}

// SetSeat stores a per-seat recline angle so the position persists across
// snapshots (a stand-in for the real car's seat-memory feature).
func (e *Engine) SetSeat(_ context.Context, id string, seat provider.SeatCmd) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.cars[id]; !ok {
		return provider.ErrNotFound
	}
	if e.seats[id] == nil {
		e.seats[id] = map[string]int{}
	}
	e.seats[id][seat.Seat] = seat.Recline
	return nil
}

func (e *Engine) mutate(id string, fn func(*provider.Snapshot)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.get(id)
	if err != nil {
		return err
	}
	fn(s)
	s.UpdatedAt = time.Now().Unix()
	return nil
}
