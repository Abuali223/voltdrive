package realtime

import (
	"context"
	"testing"

	"voltdrive/backend/internal/provider"
)

// capturingAlerter records alerts for assertions.
type capturingAlerter struct{ kinds []string }

func (c *capturingAlerter) VehicleAlert(_ context.Context, _, kind, _ string) {
	c.kinds = append(c.kinds, kind)
}

func TestSecurityWatch(t *testing.T) {
	base := provider.Snapshot{
		VehicleID: "v1", Name: "Voyah", Lock: provider.Locked,
		Energy:   provider.EnergyState{BatteryLevel: 50},
		Location: provider.Location{Lat: 41.3, Lng: 69.2},
	}

	cases := []struct {
		name string
		mut  func(s *provider.Snapshot)
		want string
	}{
		{"moved while locked", func(s *provider.Snapshot) { s.Location.Lat = 41.31 }, "moved_while_locked"},
		{"unlocked", func(s *provider.Snapshot) { s.Lock = provider.Unlocked }, "unlocked"},
		{"low battery", func(s *provider.Snapshot) { s.Energy.BatteryLevel = 10 }, "low_battery"},
		{"no change", func(s *provider.Snapshot) {}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cap := &capturingAlerter{}
			h := NewHub(nil).WithAlerter(cap)
			cur := base
			tc.mut(&cur)
			prev := base
			h.securityWatch(context.Background(), &prev, cur)

			if tc.want == "" {
				if len(cap.kinds) != 0 {
					t.Fatalf("expected no alert, got %v", cap.kinds)
				}
				return
			}
			if len(cap.kinds) != 1 || cap.kinds[0] != tc.want {
				t.Fatalf("want alert %q, got %v", tc.want, cap.kinds)
			}
		})
	}
}
