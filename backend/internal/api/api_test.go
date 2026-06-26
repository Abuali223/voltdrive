package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"voltdrive/backend/internal/auth"
	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/mock"
)

func newTestServer() http.Handler {
	s := &Server{
		Registry: provider.NewRegistry(mock.New()),
		Verifier: auth.DevVerifier{},
		Perms:    auth.DevPermissions{},
	}
	return s.Routes()
}

func do(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	h := newTestServer()
	if rec := do(t, h, "GET", "/healthz", ""); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("readyz = %d", rec.Code)
	}
}

func TestSnapshotAuth(t *testing.T) {
	h := newTestServer()
	// No token → 401.
	if rec := do(t, h, "GET", "/v1/vehicles/voyah-001", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	// With token → 200 + JSON.
	rec := do(t, h, "GET", "/v1/vehicles/voyah-001", "u-ali:ali@x.uz")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var snap provider.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.VehicleID != "voyah-001" {
		t.Fatalf("vehicleId = %q", snap.VehicleID)
	}
}

func TestUnknownVehicle(t *testing.T) {
	h := newTestServer()
	if rec := do(t, h, "GET", "/v1/vehicles/nope-999", "u-ali"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestCommandFlow(t *testing.T) {
	h := newTestServer()
	// Unlock then verify state reflects it.
	rec := do(t, h, "POST", "/v1/vehicles/voyah-001/unlock", "u-ali")
	if rec.Code != 200 {
		t.Fatalf("unlock = %d", rec.Code)
	}
	var snap provider.Snapshot
	_ = json.Unmarshal(rec.Body.Bytes(), &snap)
	if snap.Lock != provider.Unlocked {
		t.Fatalf("lock = %q, want unlocked", snap.Lock)
	}
}

func TestClimateValidation(t *testing.T) {
	s := &Server{Registry: provider.NewRegistry(mock.New()), Verifier: auth.DevVerifier{}, Perms: auth.DevPermissions{}}
	h := s.Routes()
	req := httptest.NewRequest("POST", "/v1/vehicles/voyah-001/climate", strings.NewReader(`{"on":true,"targetC":99}`))
	req.Header.Set("Authorization", "Bearer u-ali")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for out-of-range temp, got %d", rec.Code)
	}
}

func TestAuxCommands(t *testing.T) {
	h := newTestServer()
	// Lights on → 200 and reflected in the snapshot.
	req := httptest.NewRequest("POST", "/v1/vehicles/voyah-001/lights", strings.NewReader(`{"on":true}`))
	req.Header.Set("Authorization", "Bearer u-ali")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("lights = %d", rec.Code)
	}
	var snap provider.Snapshot
	_ = json.Unmarshal(rec.Body.Bytes(), &snap)
	if !snap.LightsOn {
		t.Fatalf("lightsOn = false, want true")
	}
	// Horn → 200 (momentary, no state).
	if rec := do(t, h, "POST", "/v1/vehicles/voyah-001/horn", "u-ali"); rec.Code != 200 {
		t.Fatalf("horn = %d", rec.Code)
	}
	// Seat out-of-range → 400.
	req2 := httptest.NewRequest("POST", "/v1/vehicles/voyah-001/seat", strings.NewReader(`{"seat":"driver","recline":999}`))
	req2.Header.Set("Authorization", "Bearer u-ali")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("seat out-of-range = %d, want 400", rec2.Code)
	}
}

func TestCORSAllowlist(t *testing.T) {
	s := &Server{
		Registry:       provider.NewRegistry(mock.New()),
		Verifier:       auth.DevVerifier{},
		Perms:          auth.DevPermissions{},
		AllowedOrigins: []string{"https://eldi-79bf9.web.app"},
	}
	h := s.Routes()
	// Allowed origin is reflected.
	req := httptest.NewRequest("OPTIONS", "/v1/vehicles/voyah-001", nil)
	req.Header.Set("Origin", "https://eldi-79bf9.web.app")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://eldi-79bf9.web.app" {
		t.Fatalf("allow-origin = %q", got)
	}
	// PUT and DELETE must be advertised so the browser allows fleet/routines
	// saves and revoke/remove operations (preflight otherwise blocks them).
	for _, m := range []string{"PUT", "DELETE"} {
		if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), m) {
			t.Fatalf("Allow-Methods missing %s: %q", m, rec.Header().Get("Access-Control-Allow-Methods"))
		}
	}
	// Disallowed origin is not reflected.
	req2 := httptest.NewRequest("OPTIONS", "/v1/vehicles/voyah-001", nil)
	req2.Header.Set("Origin", "https://evil.example")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Access-Control-Allow-Origin"); got == "https://evil.example" {
		t.Fatalf("evil origin reflected: %q", got)
	}
}

func TestRateLimit(t *testing.T) {
	s := &Server{
		Registry:   provider.NewRegistry(mock.New()),
		Verifier:   auth.DevVerifier{},
		Perms:      auth.DevPermissions{},
		RatePerSec: 1, RateBurst: 3,
	}
	h := s.Routes()
	limited := false
	for i := 0; i < 10; i++ {
		rec := do(t, h, "GET", "/v1/vehicles/voyah-001", "u-ali")
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("expected a 429 within burst+overflow")
	}
	_ = context.Background()
}
