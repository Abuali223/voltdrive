package provider

import (
	"context"
	"testing"
)

// fullProv does not implement Capabler → should report all capabilities.
type fullProv struct{}

func (fullProv) Brand() string                                           { return "full" }
func (fullProv) Snapshot(context.Context, string) (Snapshot, error)      { return Snapshot{}, nil }
func (fullProv) Lock(context.Context, string) error                      { return nil }
func (fullProv) Unlock(context.Context, string) error                    { return nil }
func (fullProv) RemoteStart(context.Context, string) error               { return nil }
func (fullProv) RemoteStop(context.Context, string) error                { return nil }
func (fullProv) SetClimate(context.Context, string, bool, float64) error { return nil }

// limitedProv implements Capabler with a reduced set.
type limitedProv struct{ fullProv }

func (limitedProv) Capabilities() []string { return []string{CapLock, CapLocation} }

func TestCapabilitiesOf(t *testing.T) {
	if got := CapabilitiesOf(fullProv{}); len(got) != len(AllCapabilities) {
		t.Fatalf("non-Capabler should get all caps, got %d", len(got))
	}
	got := CapabilitiesOf(limitedProv{})
	if len(got) != 2 || got[0] != CapLock || got[1] != CapLocation {
		t.Fatalf("Capabler should get its declared subset, got %v", got)
	}
}
