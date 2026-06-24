// Package api exposes the VoltDrive HTTP/JSON control surface.
// It is brand-agnostic: every request is routed through the provider
// Registry to whichever adapter owns the target vehicle, and every
// mutating request is checked against the caller's RBAC role.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"voltdrive/backend/internal/auth"
	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/realtime"
)

// Server holds dependencies for the HTTP handlers.
type Server struct {
	Registry       *provider.Registry
	Verifier       auth.Verifier
	Perms          auth.Permissions
	Hub            *realtime.Hub // optional: real-time telemetry stream
	AllowedOrigins []string      // CORS allowlist (empty = permissive "*")
	RatePerSec     float64       // per-IP request rate (default 20)
	RateBurst      float64       // per-IP burst (default 40)
}

// Routes builds the http.Handler with all endpoints registered and the
// hardening middleware chain applied (recovery, request-id, logging, security
// headers, CORS allowlist, rate limiting).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Liveness + readiness (no auth) — used by Cloud Run / uptime checks.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Full vehicle state.
	mux.HandleFunc("GET /v1/vehicles/{id}", s.guard(auth.ActView, s.handleSnapshot))

	// Commands. Each is checked against the matching action.
	mux.HandleFunc("POST /v1/vehicles/{id}/lock", s.guard(auth.ActLock, s.cmd("lock")))
	mux.HandleFunc("POST /v1/vehicles/{id}/unlock", s.guard(auth.ActLock, s.cmd("unlock")))
	mux.HandleFunc("POST /v1/vehicles/{id}/start", s.guard(auth.ActStart, s.cmd("start")))
	mux.HandleFunc("POST /v1/vehicles/{id}/stop", s.guard(auth.ActStart, s.cmd("stop")))
	mux.HandleFunc("POST /v1/vehicles/{id}/climate", s.guard(auth.ActClimate, s.handleClimate))

	// Real-time telemetry stream (WebSocket).
	if s.Hub != nil {
		mux.HandleFunc("GET /v1/vehicles/{id}/stream", s.guardStream(auth.ActView, s.handleStream))
	}

	rps, burst := s.RatePerSec, s.RateBurst
	if rps <= 0 {
		rps = 20
	}
	if burst <= 0 {
		burst = 40
	}
	lim := newLimiter(rps, burst)

	return chain(mux,
		recoverMW,
		requestID,
		accessLog,
		securityHeaders,
		corsAllowlist(s.AllowedOrigins),
		lim.middleware,
	)
}

// authorize authenticates the caller (with the given token) and checks their
// role for the target vehicle. On failure it writes the error and returns false.
func (s *Server) authorize(ctx context.Context, w http.ResponseWriter, r *http.Request, action auth.Action, token string) (auth.User, bool) {
	user, err := s.Verifier.Verify(ctx, token)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return auth.User{}, false
	}
	role, ok := s.Perms.RoleFor(ctx, user.UID, r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusForbidden, "no access to this vehicle")
		return auth.User{}, false
	}
	if !role.Can(action) {
		writeErr(w, http.StatusForbidden, "role '"+string(role)+"' cannot "+string(action))
		return auth.User{}, false
	}
	return user, true
}

// guard wraps a normal request/response handler with a 10s timeout plus
// authentication + per-vehicle authorization. Tokens come from the
// Authorization header only.
func (s *Server) guard(action auth.Action, next func(http.ResponseWriter, *http.Request, auth.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		user, ok := s.authorize(ctx, w, r, action, bearerHeader(r))
		if !ok {
			return
		}
		next(w, r.WithContext(ctx), user)
	}
}

// guardStream is like guard but without the request timeout (long-lived
// streaming). WebSocket clients cannot set headers, so the token may also
// arrive as the ?access_token= query parameter.
func (s *Server) guardStream(action auth.Action, next func(http.ResponseWriter, *http.Request, auth.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.authorize(r.Context(), w, r, action, bearerWS(r))
		if !ok {
			return
		}
		next(w, r, user)
	}
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request, _ auth.User) {
	id := r.PathValue("id")
	snap, err := s.Registry.For(id).Snapshot(r.Context(), id)
	if err != nil {
		writeProviderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleStream upgrades to WebSocket and pushes live telemetry. The guard
// has already authenticated/authorized the caller. Note: the per-request
// timeout context is not applied here — streams are long-lived.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, _ auth.User) {
	s.Hub.ServeWS(w, r, r.PathValue("id"), originHosts(s.AllowedOrigins))
}

// audit records who performed which command on which vehicle, and the result.
func audit(r *http.Request, user auth.User, vehicleID, action string, err error) {
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	log.Printf("AUDIT uid=%s email=%s vehicle=%s action=%s result=%s id=%v",
		user.UID, user.Email, vehicleID, action, result, r.Context().Value(reqIDKey))
}

// cmd returns a handler for a simple no-body command.
func (s *Server) cmd(name string) func(http.ResponseWriter, *http.Request, auth.User) {
	return func(w http.ResponseWriter, r *http.Request, user auth.User) {
		id := r.PathValue("id")
		p := s.Registry.For(id)
		var err error
		switch name {
		case "lock":
			err = p.Lock(r.Context(), id)
		case "unlock":
			err = p.Unlock(r.Context(), id)
		case "start":
			err = p.RemoteStart(r.Context(), id)
		case "stop":
			err = p.RemoteStop(r.Context(), id)
		}
		audit(r, user, id, name, err)
		if err != nil {
			writeProviderErr(w, err)
			return
		}
		s.replyState(w, r, id)
	}
}

type climateReq struct {
	On      bool    `json:"on"`
	TargetC float64 `json:"targetC"`
}

func (s *Server) handleClimate(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	var req climateReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Validate the requested cabin temperature.
	if req.On && (req.TargetC < 14 || req.TargetC > 32) {
		writeErr(w, http.StatusBadRequest, "targetC must be between 14 and 32")
		return
	}
	err := s.Registry.For(id).SetClimate(r.Context(), id, req.On, req.TargetC)
	audit(r, user, id, "climate", err)
	if err != nil {
		writeProviderErr(w, err)
		return
	}
	s.replyState(w, r, id)
}

// replyState returns the fresh snapshot after a successful command so the
// app can update its UI from a single round-trip.
func (s *Server) replyState(w http.ResponseWriter, r *http.Request, id string) {
	snap, err := s.Registry.For(id).Snapshot(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// ---- helpers ----

// bearerHeader extracts the token from the Authorization header only.
func bearerHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return h[7:]
	}
	return ""
}

// bearerWS additionally allows ?access_token= (WebSocket clients cannot set
// the Authorization header).
func bearerWS(r *http.Request) string {
	if t := bearerHeader(r); t != "" {
		return t
	}
	return r.URL.Query().Get("access_token")
}

// originHosts converts allowed origin URLs (https://host) to host patterns for
// the WebSocket origin check.
func originHosts(origins []string) []string {
	out := make([]string, 0, len(origins))
	for _, o := range origins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			out = append(out, u.Host)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeProviderErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrNotFound):
		writeErr(w, http.StatusNotFound, "vehicle not found")
	case errors.Is(err, provider.ErrUnsupported):
		writeErr(w, http.StatusNotImplemented, "command not supported by this vehicle")
	default:
		writeErr(w, http.StatusBadGateway, "vehicle provider error")
	}
}

