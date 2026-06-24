package sim

import (
	"context"
	"testing"

	"voltdrive/backend/internal/provider"
)

func newEngine() *Engine {
	return New("test", []provider.Snapshot{{
		VehicleID: "t-1",
		Lock:      provider.Locked,
		Climate:   provider.ClimateState{On: false, TargetC: 22, InsideC: 30},
	}})
}

func TestSimCommands(t *testing.T) {
	ctx := context.Background()
	e := newEngine()

	if err := e.Unlock(ctx, "t-1"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if s, _ := e.Snapshot(ctx, "t-1"); s.Lock != provider.Unlocked {
		t.Fatalf("lock = %q, want unlocked", s.Lock)
	}

	if err := e.RemoteStart(ctx, "t-1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if s, _ := e.Snapshot(ctx, "t-1"); !s.EngineOn {
		t.Fatal("engine should be on after RemoteStart")
	}
}

func TestSimUnknownVehicle(t *testing.T) {
	e := newEngine()
	if _, err := e.Snapshot(context.Background(), "ghost"); err != provider.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestSimClimateDrift verifies the cabin temperature converges toward the
// climate target across repeated snapshots once climate is on.
func TestSimClimateDrift(t *testing.T) {
	ctx := context.Background()
	e := newEngine()
	if err := e.SetClimate(ctx, "t-1", true, 22); err != nil {
		t.Fatalf("setclimate: %v", err)
	}
	first, _ := e.Snapshot(ctx, "t-1")
	for i := 0; i < 20; i++ {
		e.Snapshot(ctx, "t-1")
	}
	last, _ := e.Snapshot(ctx, "t-1")
	// Started at 30, target 22 → inside temp must have dropped.
	if last.Climate.InsideC >= first.Climate.InsideC {
		t.Fatalf("inside temp did not drift toward target: first=%.1f last=%.1f",
			first.Climate.InsideC, last.Climate.InsideC)
	}
}
