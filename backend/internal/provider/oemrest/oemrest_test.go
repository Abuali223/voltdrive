package oemrest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"voltdrive/backend/internal/provider"
)

// TestLiveSnapshotAndCommand spins up a fake OEM cloud and verifies the client
// authenticates, maps the status JSON to a Snapshot, and POSTs commands.
func TestLiveSnapshotAndCommand(t *testing.T) {
	var gotAuth, gotCmd string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == "GET" && r.URL.Path == "/vehicles/voyah-001/status":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"online":true,"locked":true,"engineOn":false,"battery":77,"rangeKm":188,
				"climate":{"on":false,"targetC":22,"insideC":19},"location":{"lat":40.78,"lng":72.34},
				"odometerKm":12480,"tirePressuresKpa":[230,230,228,229]}`)
		case r.Method == "POST" && r.URL.Path == "/vehicles/voyah-001/commands":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			gotCmd, _ = body["command"].(string)
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, func(context.Context) (string, error) { return "tok-123", nil })

	snap, err := c.Snapshot(context.Background(), "voyah-001")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if snap.Lock != provider.Locked || snap.Energy.BatteryLevel != 77 || snap.Energy.RangeKm != 188 {
		t.Fatalf("bad mapping: %+v", snap)
	}
	if snap.Health.OdometerKm != 12480 || snap.Location.Lat != 40.78 {
		t.Fatalf("bad mapping: %+v", snap)
	}

	if err := c.RemoteStart(context.Background(), "voyah-001"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if gotCmd != "engineStart" {
		t.Fatalf("command = %q, want engineStart", gotCmd)
	}
}

func TestLiveNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := New(srv.URL, func(context.Context) (string, error) { return "t", nil })
	if _, err := c.Snapshot(context.Background(), "ghost"); err != provider.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
