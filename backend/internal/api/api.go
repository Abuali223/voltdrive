// Package api exposes the VoltDrive HTTP/JSON control surface.
// It is brand-agnostic: every request is routed through the provider
// Registry to whichever adapter owns the target vehicle, and every
// mutating request is checked against the caller's RBAC role.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"voltdrive/backend/internal/admins"
	"voltdrive/backend/internal/auth"
	"voltdrive/backend/internal/devices"
	"voltdrive/backend/internal/geofence"
	"voltdrive/backend/internal/guestkey"
	"voltdrive/backend/internal/members"
	"voltdrive/backend/internal/notify"
	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/realtime"
	"voltdrive/backend/internal/schedule"
	"voltdrive/backend/internal/subscription"
)

// Server holds dependencies for the HTTP handlers.
type Server struct {
	Registry         *provider.Registry
	Verifier         auth.Verifier
	Perms            auth.Permissions
	Hub              *realtime.Hub       // optional: real-time telemetry stream
	Members          *members.Store      // optional: family/shared-access management
	Devices          *devices.Store      // optional: FCM device-token registry
	Schedules        *schedule.Store     // optional: departure-timer storage
	GuestKeys        *guestkey.Store     // optional: time-limited guest access
	Geofences        *geofence.Store     // optional: safe-zone storage
	Subscriptions    *subscription.Store // optional: freemium plan/billing state
	Admins           *admins.Store       // optional: additional admin emails (super = OwnerEmail)
	FCM              *notify.FCM         // optional: push sender (for the test endpoint)
	AllowedOrigins   []string            // CORS allowlist (empty = permissive "*")
	OwnerEmail       string              // bootstrap fleet owner (full access without a Firestore grant)
	TrustedProxyHops int                 // proxies between us and the client (for real-IP extraction)
	RatePerSec       float64             // per-IP request rate (default 20)
	RateBurst        float64             // per-IP burst (default 40)
}

// Routes builds the http.Handler with all endpoints registered and the
// hardening middleware chain applied (recovery, request-id, logging, security
// headers, CORS allowlist, rate limiting).
func (s *Server) Routes() http.Handler {
	trustedProxyHops = s.TrustedProxyHops // for spoof-resistant client-IP extraction
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

	// Auxiliary controls (lights, trunk, horn, seat). Providers that don't
	// implement provider.Auxiliary return 501 (not supported).
	mux.HandleFunc("POST /v1/vehicles/{id}/lights", s.guard(auth.ActStart, s.handleLights))
	mux.HandleFunc("POST /v1/vehicles/{id}/trunk", s.guard(auth.ActLock, s.handleTrunk))
	mux.HandleFunc("POST /v1/vehicles/{id}/horn", s.guard(auth.ActStart, s.handleHorn))
	mux.HandleFunc("POST /v1/vehicles/{id}/seat", s.guard(auth.ActClimate, s.handleSeat))

	// Family / shared-access management (owner only).
	if s.Members != nil {
		mux.HandleFunc("GET /v1/vehicles/{id}/members", s.guard(auth.ActView, s.handleMembersList))
		mux.HandleFunc("POST /v1/vehicles/{id}/members", s.guard(auth.ActManage, s.handleMembersPut))
		mux.HandleFunc("DELETE /v1/vehicles/{id}/members", s.guard(auth.ActManage, s.handleMembersDelete))
	}

	// Push-notification device registration + test (any authenticated user).
	if s.Devices != nil {
		mux.HandleFunc("POST /v1/devices", s.guardUser(s.handleDeviceRegister))
		if s.FCM != nil {
			mux.HandleFunc("POST /v1/devices/test", s.guardUser(s.handleDeviceTest))
		}
	}

	// Departure timer (daily warm-up schedule).
	if s.Schedules != nil {
		mux.HandleFunc("GET /v1/vehicles/{id}/schedule", s.guard(auth.ActView, s.handleScheduleGet))
		mux.HandleFunc("PUT /v1/vehicles/{id}/schedule", s.guard(auth.ActStart, s.handleSchedulePut))
	}

	// Panic alarm: flash lights + sound the horn. Owner/driver only.
	mux.HandleFunc("POST /v1/vehicles/{id}/panic", s.guard(auth.ActStart, s.handlePanic))

	// Guest keys: owner issues/lists/revokes; guests redeem the code with no
	// account (the code is the credential) and send only scoped commands.
	if s.GuestKeys != nil {
		mux.HandleFunc("GET /v1/vehicles/{id}/guestkeys", s.guard(auth.ActManage, s.handleGuestKeysList))
		mux.HandleFunc("POST /v1/vehicles/{id}/guestkeys", s.guard(auth.ActManage, s.handleGuestKeyCreate))
		mux.HandleFunc("DELETE /v1/vehicles/{id}/guestkeys/{code}", s.guard(auth.ActManage, s.handleGuestKeyRevoke))
		mux.HandleFunc("POST /v1/guest/redeem", s.handleGuestRedeem)
		mux.HandleFunc("POST /v1/guest/command", s.handleGuestCommand)
	}

	// Geofence (safe zone). Owner-managed; the watcher alerts on exit.
	if s.Geofences != nil {
		mux.HandleFunc("GET /v1/vehicles/{id}/geofence", s.guard(auth.ActView, s.handleGeofenceGet))
		mux.HandleFunc("PUT /v1/vehicles/{id}/geofence", s.guard(auth.ActManage, s.handleGeofencePut))
	}

	// Subscriptions (freemium): user reads own plan, starts a trial or requests
	// a paid tier; admins list everyone and activate/revoke after payment.
	if s.Subscriptions != nil {
		mux.HandleFunc("GET /v1/subscription", s.guardUser(s.handleSubGet))
		mux.HandleFunc("POST /v1/subscription/trial", s.guardUser(s.handleSubTrial))
		mux.HandleFunc("POST /v1/subscription/request", s.guardUser(s.handleSubRequest))
		mux.HandleFunc("GET /v1/admin/subscriptions", s.guardUser(s.handleAdminSubs))
		mux.HandleFunc("POST /v1/admin/subscriptions/{uid}/activate", s.guardUser(s.handleAdminActivate))
		mux.HandleFunc("POST /v1/admin/subscriptions/{uid}/revoke", s.guardUser(s.handleAdminRevoke))
	}

	// Admin management (super admin only): list / add / remove admins.
	mux.HandleFunc("GET /v1/admin/whoami", s.guardUser(s.handleWhoAmI))
	if s.Admins != nil {
		mux.HandleFunc("GET /v1/admin/admins", s.guardUser(s.handleAdminsList))
		mux.HandleFunc("POST /v1/admin/admins", s.guardUser(s.handleAdminsAdd))
		mux.HandleFunc("DELETE /v1/admin/admins/{email}", s.guardUser(s.handleAdminsRemove))
	}

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
	// Bootstrap owner: the configured fleet owner has full access to every
	// vehicle without needing a Firestore grant. Everyone else is resolved
	// strictly by their stored role (no access unless explicitly granted).
	if s.OwnerEmail != "" && user.Email != "" && strings.EqualFold(user.Email, s.OwnerEmail) {
		return user, true
	}
	vehicleID := r.PathValue("id")
	// Family sharing: a member's role comes from the same sharing list the
	// owner manages (keyed by verified email), so adding a member actually
	// grants them control.
	if s.Members != nil && user.Email != "" {
		if roleStr, found := s.Members.RoleByEmail(ctx, vehicleID, user.Email); found {
			role := auth.Role(roleStr)
			if auth.ValidRole(role) {
				if !role.Can(action) {
					writeErr(w, http.StatusForbidden, "role '"+string(role)+"' cannot "+string(action))
					return auth.User{}, false
				}
				return user, true
			}
		}
	}
	role, ok := s.Perms.RoleFor(ctx, user.UID, vehicleID)
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

// guardUser authenticates the caller but does not scope to a vehicle — used by
// account-level endpoints (device registration).
func (s *Server) guardUser(next func(http.ResponseWriter, *http.Request, auth.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		user, err := s.Verifier.Verify(ctx, bearerHeader(r))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		next(w, r.WithContext(ctx), user)
	}
}

func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request, user auth.User) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Token == "" {
		writeErr(w, http.StatusBadRequest, "token required")
		return
	}
	if err := s.Devices.Register(r.Context(), user.UID, req.Token); err != nil {
		writeErr(w, http.StatusBadGateway, "could not register device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func (s *Server) handleDeviceTest(w http.ResponseWriter, r *http.Request, user auth.User) {
	tokens, err := s.Devices.ListTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load devices")
		return
	}
	// One sample of each notification category so the user sees the variety.
	samples := []notify.Alert{
		{Title: "VoltDrive", Body: "Bildirishnomalar yoqildi ✓", Data: map[string]string{"type": "test"}},
		{Title: "VoltDrive — Zaryad", Body: "Voyah Free zaryadlandi — 100%", Data: map[string]string{"type": "charge_complete"}},
		{Title: "VoltDrive — Batareya", Body: "Voyah Free batareyasi 15% dan pastga tushdi", Data: map[string]string{"type": "low_battery"}},
		{Title: "VoltDrive — Xavfsizlik ⚠️", Body: "Voyah Free qulflangan holatda harakatlandi", Data: map[string]string{"type": "moved_while_locked"}},
	}
	sent := 0
	for _, t := range tokens {
		for _, a := range samples {
			a.DeviceToken = t
			if _, err := s.FCM.Send(r.Context(), a); err == nil {
				sent++
			}
		}
	}
	audit(r, user, "-", "device-test", nil)
	writeJSON(w, http.StatusOK, map[string]int{"sent": sent})
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

type onReq struct {
	On bool `json:"on"`
}

// aux returns the provider's optional Auxiliary capability, or false if the
// brand doesn't support secondary controls.
func (s *Server) aux(id string) (provider.Auxiliary, bool) {
	a, ok := s.Registry.For(id).(provider.Auxiliary)
	return a, ok
}

func (s *Server) handleLights(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	var req onReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	a, ok := s.aux(id)
	if !ok {
		writeProviderErr(w, provider.ErrUnsupported)
		return
	}
	err := a.SetLights(r.Context(), id, req.On)
	audit(r, user, id, "lights", err)
	if err != nil {
		writeProviderErr(w, err)
		return
	}
	s.replyState(w, r, id)
}

func (s *Server) handleTrunk(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	var req onReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	a, ok := s.aux(id)
	if !ok {
		writeProviderErr(w, provider.ErrUnsupported)
		return
	}
	err := a.SetTrunk(r.Context(), id, req.On)
	audit(r, user, id, "trunk", err)
	if err != nil {
		writeProviderErr(w, err)
		return
	}
	s.replyState(w, r, id)
}

func (s *Server) handleHorn(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	a, ok := s.aux(id)
	if !ok {
		writeProviderErr(w, provider.ErrUnsupported)
		return
	}
	err := a.Honk(r.Context(), id)
	audit(r, user, id, "horn", err)
	if err != nil {
		writeProviderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func (s *Server) handleSeat(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	var seat provider.SeatCmd
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&seat); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if seat.Recline < 0 || seat.Recline > 180 {
		writeErr(w, http.StatusBadRequest, "recline must be between 0 and 180")
		return
	}
	a, ok := s.aux(id)
	if !ok {
		writeProviderErr(w, provider.ErrUnsupported)
		return
	}
	err := a.SetSeat(r.Context(), id, seat)
	audit(r, user, id, "seat", err)
	if err != nil {
		writeProviderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

type memberReq struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (s *Server) handleMembersList(w http.ResponseWriter, r *http.Request, _ auth.User) {
	list, err := s.Members.List(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load members")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": list})
}

func (s *Server) handleMembersPut(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	var req memberReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if !strings.Contains(req.Email, "@") {
		writeErr(w, http.StatusBadRequest, "valid email required")
		return
	}
	role := auth.Role(req.Role)
	if role != auth.RoleDriver && role != auth.RoleGuest && role != auth.RoleOwner {
		writeErr(w, http.StatusBadRequest, "role must be owner, driver or guest")
		return
	}
	err := s.Members.Put(r.Context(), id, members.Member{Email: req.Email, Role: string(role)})
	audit(r, user, id, "member-add:"+req.Email+":"+req.Role, err)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not add member")
		return
	}
	s.handleMembersList(w, r, user)
}

func (s *Server) handleMembersDelete(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	email := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("email")))
	if email == "" {
		writeErr(w, http.StatusBadRequest, "email query param required")
		return
	}
	err := s.Members.Remove(r.Context(), id, email)
	audit(r, user, id, "member-remove:"+email, err)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not remove member")
		return
	}
	s.handleMembersList(w, r, user)
}

func (s *Server) handleScheduleGet(w http.ResponseWriter, r *http.Request, _ auth.User) {
	sc, err := s.Schedules.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load schedule")
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleSchedulePut(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	var sc schedule.Schedule
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&sc); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if sc.Hour < 0 || sc.Hour > 23 || sc.Minute < 0 || sc.Minute > 59 {
		writeErr(w, http.StatusBadRequest, "invalid time")
		return
	}
	if sc.TargetC != 0 && (sc.TargetC < 14 || sc.TargetC > 32) {
		writeErr(w, http.StatusBadRequest, "targetC must be 14-32")
		return
	}
	if err := s.Schedules.Put(r.Context(), id, sc); err != nil {
		writeErr(w, http.StatusBadGateway, "could not save schedule")
		return
	}
	audit(r, user, id, fmt.Sprintf("schedule:%02d:%02d:%v", sc.Hour, sc.Minute, sc.Enabled), nil)
	writeJSON(w, http.StatusOK, sc)
}

// --- Panic alarm ---

func (s *Server) handlePanic(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	a, ok := s.aux(id)
	if !ok {
		writeProviderErr(w, provider.ErrUnsupported)
		return
	}
	_ = a.SetLights(r.Context(), id, true)
	err := a.Honk(r.Context(), id)
	audit(r, user, id, "panic", err)
	if err != nil {
		writeProviderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Guest keys ---

func (s *Server) handleGuestKeysList(w http.ResponseWriter, r *http.Request, _ auth.User) {
	keys, err := s.GuestKeys.ListActive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not list guest keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) handleGuestKeyCreate(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	var req struct {
		Hours float64  `json:"hours"`
		Scope []string `json:"scope"`
		Label string   `json:"label"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Hours <= 0 || req.Hours > 720 {
		writeErr(w, http.StatusBadRequest, "hours must be 1-720")
		return
	}
	scope := filterScope(req.Scope)
	if len(scope) == 0 {
		scope = []string{"unlock", "lock"} // safe default: doors only
	}
	k, err := s.GuestKeys.Create(r.Context(), guestkey.Key{
		VehicleID: id,
		Scope:     scope,
		ExpiresAt: time.Now().Add(time.Duration(req.Hours * float64(time.Hour))).Unix(),
		Label:     req.Label,
	}, user.UID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not create guest key")
		return
	}
	audit(r, user, id, "guestkey:create", nil)
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) handleGuestKeyRevoke(w http.ResponseWriter, r *http.Request, user auth.User) {
	code := r.PathValue("code")
	if err := s.GuestKeys.Revoke(r.Context(), code); err != nil {
		writeErr(w, http.StatusBadGateway, "could not revoke guest key")
		return
	}
	audit(r, user, r.PathValue("id"), "guestkey:revoke", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// isSuperAdmin is the single root admin (OWNER_EMAIL) who manages other admins.
func (s *Server) isSuperAdmin(u auth.User) bool {
	return s.OwnerEmail != "" && u.Email != "" && strings.EqualFold(u.Email, s.OwnerEmail)
}

// isAdmin reports whether the caller may use the admin panel: the super admin,
// or any email added to the admins set.
func (s *Server) isAdmin(ctx context.Context, u auth.User) bool {
	if s.isSuperAdmin(u) {
		return true
	}
	return s.Admins != nil && u.Email != "" && s.Admins.IsAdmin(ctx, u.Email)
}

func (s *Server) handleSubGet(w http.ResponseWriter, r *http.Request, user auth.User) {
	sub, err := s.Subscriptions.Get(r.Context(), user.UID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load subscription")
		return
	}
	writeJSON(w, http.StatusOK, sub.Normalize())
}

func (s *Server) handleSubTrial(w http.ResponseWriter, r *http.Request, user auth.User) {
	sub, err := s.Subscriptions.Get(r.Context(), user.UID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load subscription")
		return
	}
	// One trial per account: block if a trial was already used or already premium.
	if sub.Status == "trial" || sub.Status == "active" || (sub.Status == "expired" && sub.Tier == "trial") {
		writeErr(w, http.StatusConflict, "trial already used")
		return
	}
	now := time.Now()
	sub.UID, sub.Email = user.UID, user.Email
	sub.Plan, sub.Status, sub.Tier = "premium", "trial", "trial"
	sub.StartedAt = now.Unix()
	sub.ExpiresAt = now.Add(14 * 24 * time.Hour).Unix()
	if err := s.Subscriptions.Put(r.Context(), sub); err != nil {
		writeErr(w, http.StatusBadGateway, "could not start trial")
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) handleSubRequest(w http.ResponseWriter, r *http.Request, user auth.User) {
	var req struct {
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Tier != "1m" && req.Tier != "2m" && req.Tier != "1y" {
		writeErr(w, http.StatusBadRequest, "invalid tier")
		return
	}
	sub, err := s.Subscriptions.Get(r.Context(), user.UID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load subscription")
		return
	}
	sub.UID, sub.Email = user.UID, user.Email
	sub.RequestedTier = req.Tier
	sub.RequestedAt = time.Now().Unix()
	if !sub.Active() { // keep trial access if still valid; otherwise mark pending
		sub.Status = "pending"
	}
	if err := s.Subscriptions.Put(r.Context(), sub); err != nil {
		writeErr(w, http.StatusBadGateway, "could not save request")
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) handleAdminSubs(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !s.isAdmin(r.Context(), user) {
		writeErr(w, http.StatusForbidden, "admin only")
		return
	}
	subs, err := s.Subscriptions.All(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not list subscriptions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

func (s *Server) handleAdminActivate(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !s.isAdmin(r.Context(), user) {
		writeErr(w, http.StatusForbidden, "admin only")
		return
	}
	uid := r.PathValue("uid")
	var req struct {
		Tier string `json:"tier"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req)
	sub, err := s.Subscriptions.Get(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load subscription")
		return
	}
	tier := req.Tier
	if tier == "" {
		tier = sub.RequestedTier
	}
	if tier == "" {
		tier = "1m"
	}
	now := time.Now()
	sub.UID = uid
	sub.Plan, sub.Status, sub.Tier = "premium", "active", tier
	sub.StartedAt = now.Unix()
	sub.ExpiresAt = now.Add(subscription.TierDuration(tier)).Unix()
	sub.RequestedTier = ""
	if err := s.Subscriptions.Put(r.Context(), sub); err != nil {
		writeErr(w, http.StatusBadGateway, "could not activate")
		return
	}
	audit(r, user, uid, "sub:activate:"+tier, nil)
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) handleAdminRevoke(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !s.isAdmin(r.Context(), user) {
		writeErr(w, http.StatusForbidden, "admin only")
		return
	}
	uid := r.PathValue("uid")
	sub, err := s.Subscriptions.Get(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load subscription")
		return
	}
	sub.UID = uid
	sub.Plan, sub.Status = "free", "expired"
	sub.ExpiresAt = time.Now().Unix()
	if err := s.Subscriptions.Put(r.Context(), sub); err != nil {
		writeErr(w, http.StatusBadGateway, "could not revoke")
		return
	}
	audit(r, user, uid, "sub:revoke", nil)
	writeJSON(w, http.StatusOK, sub)
}

// handleWhoAmI tells the caller whether they are an admin / super admin, so the
// panel can show or hide the admin-management section.
func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request, user auth.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"email": user.Email,
		"admin": s.isAdmin(r.Context(), user),
		"super": s.isSuperAdmin(user),
	})
}

func (s *Server) handleAdminsList(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !s.isSuperAdmin(user) {
		writeErr(w, http.StatusForbidden, "super admin only")
		return
	}
	list, err := s.Admins.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not list admins")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"super": s.OwnerEmail, "admins": list})
}

func (s *Server) handleAdminsAdd(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !s.isSuperAdmin(user) {
		writeErr(w, http.StatusForbidden, "super admin only")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(email, "@") {
		writeErr(w, http.StatusBadRequest, "invalid email")
		return
	}
	if err := s.Admins.Add(r.Context(), email); err != nil {
		writeErr(w, http.StatusBadGateway, "could not add admin")
		return
	}
	audit(r, user, email, "admin:add", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": email})
}

func (s *Server) handleAdminsRemove(w http.ResponseWriter, r *http.Request, user auth.User) {
	if !s.isSuperAdmin(user) {
		writeErr(w, http.StatusForbidden, "super admin only")
		return
	}
	email, _ := url.PathUnescape(r.PathValue("email"))
	if err := s.Admins.Remove(r.Context(), email); err != nil {
		writeErr(w, http.StatusBadGateway, "could not remove admin")
		return
	}
	audit(r, user, email, "admin:remove", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// maskCode redacts all but the first two characters of a guest code so the
// full credential never lands in logs (e.g. "AB●●●●●●").
func maskCode(code string) string {
	if len(code) <= 2 {
		return "●●"
	}
	return code[:2] + strings.Repeat("●", len(code)-2)
}

// handleGuestRedeem validates a code (no account needed) and returns what the
// guest is allowed to do, plus the vehicle name for display.
func (s *Server) handleGuestRedeem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	k, err := s.GuestKeys.Get(r.Context(), strings.ToUpper(strings.TrimSpace(req.Code)))
	if err != nil || !k.Active() {
		writeErr(w, http.StatusForbidden, "invalid or expired key")
		return
	}
	name := k.VehicleID
	if snap, err := s.Registry.For(k.VehicleID).Snapshot(r.Context(), k.VehicleID); err == nil && snap.Name != "" {
		name = snap.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"vehicleId": k.VehicleID, "vehicleName": name,
		"scope": k.Scope, "expiresAt": k.ExpiresAt, "label": k.Label,
	})
}

// handleGuestCommand executes a single scoped command authenticated by the
// guest code itself.
func (s *Server) handleGuestCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code   string `json:"code"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	k, err := s.GuestKeys.Get(r.Context(), strings.ToUpper(strings.TrimSpace(req.Code)))
	if err != nil || !k.Active() {
		writeErr(w, http.StatusForbidden, "invalid or expired key")
		return
	}
	if !k.Allows(req.Action) {
		writeErr(w, http.StatusForbidden, "action not allowed for this key")
		return
	}
	p := s.Registry.For(k.VehicleID)
	switch req.Action {
	case "unlock":
		err = p.Unlock(r.Context(), k.VehicleID)
	case "lock":
		err = p.Lock(r.Context(), k.VehicleID)
	case "start":
		err = p.RemoteStart(r.Context(), k.VehicleID)
	default:
		writeErr(w, http.StatusBadRequest, "unknown action")
		return
	}
	log.Printf("guest command: %s %s (key %s)", req.Action, k.VehicleID, maskCode(k.Code))
	if err != nil {
		writeProviderErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": req.Action})
}

func filterScope(in []string) []string {
	allowed := map[string]bool{"unlock": true, "lock": true, "start": true}
	var out []string
	for _, s := range in {
		if allowed[s] {
			out = append(out, s)
		}
	}
	return out
}

// --- Geofence ---

func (s *Server) handleGeofenceGet(w http.ResponseWriter, r *http.Request, _ auth.User) {
	z, err := s.Geofences.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not load geofence")
		return
	}
	writeJSON(w, http.StatusOK, z)
}

func (s *Server) handleGeofencePut(w http.ResponseWriter, r *http.Request, user auth.User) {
	id := r.PathValue("id")
	var z geofence.Zone
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&z); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if z.RadiusM < 50 || z.RadiusM > 50000 {
		writeErr(w, http.StatusBadRequest, "radiusM must be 50-50000")
		return
	}
	if err := s.Geofences.Put(r.Context(), id, z); err != nil {
		writeErr(w, http.StatusBadGateway, "could not save geofence")
		return
	}
	audit(r, user, id, fmt.Sprintf("geofence:%v:%dm", z.Enabled, int(z.RadiusM)), nil)
	writeJSON(w, http.StatusOK, z)
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
