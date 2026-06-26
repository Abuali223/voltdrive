package immobilizer

import (
	"context"
	"errors"
	"testing"
)

// TestIsBlockedFailClosed verifies the anti-theft fail-closed behaviour: when
// the store cannot be read, an unknown vehicle is denied, while vehicles with a
// last-known state fall back to that cached value.
func TestIsBlockedFailClosed(t *testing.T) {
	// A token source that always errors makes Get fail before any network call.
	errTok := func(context.Context) (string, error) { return "", errors.New("token unavailable") }
	s := NewStore("proj", errTok)
	ctx := context.Background()

	// Unknown vehicle + store error → must fail CLOSED (blocked).
	if !s.IsBlocked(ctx, "veh-unknown") {
		t.Fatal("expected unknown vehicle to be blocked when the store errors")
	}

	// Last-known "not immobilized" → keep the car usable through a glitch.
	s.mu.Lock()
	s.cache["veh-ok"] = false
	s.mu.Unlock()
	if s.IsBlocked(ctx, "veh-ok") {
		t.Fatal("expected cached false (not immobilized) to allow start")
	}

	// Last-known "immobilized" → stay blocked through a glitch.
	s.mu.Lock()
	s.cache["veh-locked"] = true
	s.mu.Unlock()
	if !s.IsBlocked(ctx, "veh-locked") {
		t.Fatal("expected cached true (immobilized) to keep the car blocked")
	}
}
