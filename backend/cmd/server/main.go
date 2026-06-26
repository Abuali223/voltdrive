// Command server is the VoltDrive control backend.
//
// It runs end-to-end today using the in-memory mock provider, and is wired
// so real brand adapters (tesla, byd, ...), Firebase auth, Realtime Database
// telemetry and FCM alerts switch on automatically via environment variables
// — without touching the API layer.
//
// Environment:
//
//	PORT                  HTTP port (default 8080)
//	FIREBASE_PROJECT_ID   enables real Firebase Auth + Firestore RBAC
//	RTDB_URL              enables Realtime Database telemetry mirroring
//	FCM_ENABLED=1         enables FCM security alerts (needs FIREBASE_PROJECT_ID)
//
// When FIREBASE_PROJECT_ID is unset the server uses permissive dev auth so it
// runs locally with zero configuration.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"voltdrive/backend/internal/admins"
	"voltdrive/backend/internal/api"
	"voltdrive/backend/internal/assistant"
	"voltdrive/backend/internal/auth"
	"voltdrive/backend/internal/branding"
	"voltdrive/backend/internal/devices"
	"voltdrive/backend/internal/diagnostics"
	"voltdrive/backend/internal/fleet"
	"voltdrive/backend/internal/gcp"
	"voltdrive/backend/internal/geofence"
	"voltdrive/backend/internal/guestkey"
	"voltdrive/backend/internal/members"
	"voltdrive/backend/internal/notify"
	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/brands"
	"voltdrive/backend/internal/provider/mock"
	"voltdrive/backend/internal/realtime"
	"voltdrive/backend/internal/routines"
	"voltdrive/backend/internal/rtdb"
	"voltdrive/backend/internal/schedule"
	"voltdrive/backend/internal/subscription"
	"voltdrive/backend/internal/trips"
	"voltdrive/backend/internal/users"
	"voltdrive/backend/internal/voice"
)

func main() {
	port := envOr("PORT", "8080")

	// Default provider: mock fleet (fallback for any unbound vehicle).
	registry := provider.NewRegistry(mock.New())

	// Bind each vehicle to its brand adapter. Every brand is a separate module
	// (provider/voyah, /deepal, ...) wired through the brands factory. They run
	// on the built-in simulator now; once a brand's real API is granted, pass
	// its BaseURL + TokenSource here and the adapter switches to live calls —
	// nothing else changes.
	//
	//   registry.Bind("<vin-or-id>", mustBrand("voyah", brands.Config{
	//       BaseURL: os.Getenv("VOYAH_API_URL"),
	//       TokenSource: gcp.NewTokenSource(".../auth/userinfo.email").Token,
	//   }))
	brandOf := map[string]string{
		"voyah-001":    "voyah",
		"deepal-002":   "deepal",
		"dongfeng-003": "dongfeng",
		"byd-004":      "byd",
	}
	mirrorIDs := make([]string, 0, len(brandOf))
	for vehicleID, brand := range brandOf {
		if adapter, ok := brands.New(brand, brandConfig(brand)); ok {
			registry.Bind(vehicleID, adapter)
			mirrorIDs = append(mirrorIDs, vehicleID)
		}
	}
	log.Printf("brands wired: %v", brands.Supported())

	// --- Auth: real Firebase if configured, dev otherwise ---
	var verifier auth.Verifier = auth.DevVerifier{}
	var perms auth.Permissions = auth.DevPermissions{}
	projectID := os.Getenv("FIREBASE_PROJECT_ID")
	if projectID != "" {
		verifier = auth.NewFirebaseVerifier(projectID)
		if os.Getenv("RBAC_OPEN") == "1" {
			// Any successfully-authenticated Firebase user is treated as owner
			// of the fleet. Good for a single-owner / demo deployment; switch
			// off (Firestore RBAC) once multi-user sharing is configured.
			perms = auth.DevPermissions{}
			log.Printf("auth: Firebase verify + OPEN rbac (authenticated = owner)")
		} else {
			fsToken := gcp.NewTokenSource(
				"https://www.googleapis.com/auth/datastore",
			).Token
			perms = auth.NewFirestorePermissions(projectID, fsToken)
			log.Printf("auth: Firebase + Firestore RBAC (project %s)", projectID)
		}
	} else {
		log.Printf("auth: DEV mode (no FIREBASE_PROJECT_ID) — do not use in production")
	}

	// --- Family / shared-access management (Firestore) ---
	var memberStore *members.Store
	if projectID != "" {
		memberStore = members.NewStore(projectID, gcp.NewTokenSource(
			"https://www.googleapis.com/auth/datastore",
		).Token)
		log.Printf("members: Firestore store enabled (project %s)", projectID)
	}

	// --- Departure timer, guest keys, geofence (Firestore) ---
	var scheduleStore *schedule.Store
	var guestStore *guestkey.Store
	var geoStore *geofence.Store
	var subStore *subscription.Store
	var adminStore *admins.Store
	var userStore *users.Store
	var fleetStore *fleet.Store
	var brandStore *branding.Store
	var assistClient *assistant.Client
	var routinesStore *routines.Store
	var tripsStore *trips.Store
	if projectID != "" {
		dsToken := gcp.NewTokenSource("https://www.googleapis.com/auth/datastore").Token
		scheduleStore = schedule.NewStore(projectID, dsToken)
		guestStore = guestkey.NewStore(projectID, dsToken)
		geoStore = geofence.NewStore(projectID, dsToken)
		subStore = subscription.NewStore(projectID, dsToken)
		adminStore = admins.NewStore(projectID, dsToken)
		userStore = users.NewStore(projectID, dsToken)
		fleetStore = fleet.NewStore(projectID, dsToken)
		brandStore = branding.NewStore(projectID, dsToken)
		routinesStore = routines.NewStore(projectID, dsToken)
		tripsStore = trips.NewStore(projectID, dsToken)
		// AI assistant via Gemini on Vertex AI (cloud-platform scope; no API key).
		assistClient = assistant.NewClient(projectID, os.Getenv("GEMINI_LOCATION"), os.Getenv("GEMINI_MODEL"),
			gcp.NewTokenSource("https://www.googleapis.com/auth/cloud-platform").Token)
		// Note: the scheduler/geofence watcher is started after FCM setup below,
		// so geofence-exit events can be pushed to the owner's devices.
	}

	// --- Real-time telemetry hub ---
	hub := realtime.NewHub(registry)

	// Mirror telemetry to Realtime Database when RTDB_URL is set.
	if rtdbURL := os.Getenv("RTDB_URL"); rtdbURL != "" {
		dbToken := gcp.NewTokenSource(
			"https://www.googleapis.com/auth/firebase.database",
			"https://www.googleapis.com/auth/userinfo.email",
		).Token
		hub.WithSink(rtdb.NewWriter(rtdbURL, dbToken))
		hub.SetMirrorVehicles(mirrorIDs...) // mirror every wired brand vehicle
		log.Printf("telemetry: mirroring to RTDB %s", rtdbURL)
	}

	// FCM push: device-token registry + security-alert sender, when enabled.
	var fcm *notify.FCM
	var deviceStore *devices.Store
	var alerter *notify.FCMAlerter
	if os.Getenv("FCM_ENABLED") == "1" && projectID != "" {
		fcmToken := gcp.NewTokenSource(
			"https://www.googleapis.com/auth/firebase.messaging",
		).Token
		fcm = notify.NewFCM(projectID, fcmToken)
		deviceStore = devices.NewStore(projectID, gcp.NewTokenSource(
			"https://www.googleapis.com/auth/datastore",
		).Token)
		// Alerts fan out to every registered device (single-owner demo).
		alerter = notify.NewFCMAlerter(fcm, func(ctx context.Context, _ string) ([]string, error) {
			return deviceStore.ListTokens(ctx)
		})
		hub.WithAlerter(alerter)
		log.Printf("alerts: FCM enabled (Firestore device registry)")
	}

	// Start the departure-timer + geofence watcher now that the alerter exists,
	// so a car leaving its safe zone pushes a notification (not just a log line).
	if scheduleStore != nil {
		go runScheduler(scheduleStore, geoStore, registry, alerter)
		log.Printf("scheduler: departure timer + geofence watcher enabled")
	}

	// Proactive AI diagnostics: periodically analyze each vehicle's telemetry and
	// push a notification when it newly enters a warning/critical state.
	if alerter != nil {
		go runDiagnostics(context.Background(), registry, alerter,
			[]string{"voyah-001", "deepal-002", "dongfeng-003", "byd-004"})
		log.Printf("diagnostics: proactive rule-based health watcher enabled")
	}

	// Automation: fire time-based routines ("every day at 07:00 warm up").
	if routinesStore != nil {
		go runRoutines(context.Background(), registry, routinesStore)
		log.Printf("routines: automation watcher enabled")
	}

	// Trip history: detect drives from telemetry (engine on→off) and log them.
	if tripsStore != nil {
		go runTrips(context.Background(), registry, tripsStore,
			[]string{"voyah-001", "deepal-002", "dongfeng-003", "byd-004"})
		log.Printf("trips: driving-history watcher enabled")
	}

	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx, 2*time.Second)

	// CORS allowlist: comma-separated origins in CORS_ORIGINS, else the app's
	// own domains. Empty stays permissive for local dev.
	var origins []string
	if v := os.Getenv("CORS_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
	} else if projectID != "" {
		origins = []string{
			"https://" + projectID + ".web.app",
			"https://" + projectID + ".firebaseapp.com",
		}
	}

	srv := &api.Server{
		Registry:         registry,
		Verifier:         verifier,
		Perms:            perms,
		Hub:              hub,
		Members:          memberStore,
		Devices:          deviceStore,
		Schedules:        scheduleStore,
		GuestKeys:        guestStore,
		Geofences:        geoStore,
		Subscriptions:    subStore,
		Admins:           adminStore,
		Users:            userStore,
		Fleets:           fleetStore,
		Branding:         brandStore,
		Assistant:        assistClient,
		Voice:            voice.NewClient(os.Getenv("UZBEKVOICE_API_KEY")),
		Routines:         routinesStore,
		Trips:            tripsStore,
		FCM:              fcm,
		AllowedOrigins:   origins,
		OwnerEmail:       os.Getenv("OWNER_EMAIL"),
		TrustedProxyHops: atoiOr(os.Getenv("TRUSTED_PROXY_HOPS"), 0),
		RatePerSec:       20,
		RateBurst:        40,
	}
	if srv.OwnerEmail != "" {
		log.Printf("auth: bootstrap owner = %s", srv.OwnerEmail)
	}

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Printf("VoltDrive backend listening on :%s", port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

// runScheduler runs once a minute. It (1) warms each car up at its set local
// time (Asia/Tashkent = UTC+5) — remote start + climate — and (2) watches every
// geofenced car, pushing an alert to the owner's devices when one leaves its
// safe zone. Both work server-side, so they fire even when no app is open.
//
// Across multiple Cloud Run instances, each timed action is guarded by a
// Firestore claim (store.Claim), so a warm-up or alert fires exactly once even
// when several instances tick simultaneously.
// runDiagnostics periodically runs FAST rule-based health checks (no AI/cost)
// on each vehicle and pushes a notification only when a NEW warning/critical
// issue appears. The AI (Gemini) is reserved for the user's on-demand
// "Diagnose" tap, so always-on monitoring stays free.
func runDiagnostics(ctx context.Context, registry *provider.Registry, alerter *notify.FCMAlerter, ids []string) {
	last := map[string]string{} // vehicle -> signature of the last alerted issue set
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		for _, id := range ids {
			snap, err := registry.For(id).Snapshot(ctx, id)
			if err != nil {
				continue
			}
			var titles []string
			for _, is := range diagnostics.Check(snap) {
				if is.Severity == "warning" || is.Severity == "critical" {
					titles = append(titles, is.Title)
				}
			}
			if len(titles) == 0 {
				last[id] = ""
				continue
			}
			sig := strings.Join(titles, "|")
			if sig == last[id] {
				continue // already alerted for this exact set of issues
			}
			last[id] = sig
			alerter.VehicleAlert(ctx, id, "diagnostic", strings.Join(titles, " · "))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runRoutines fires time-based automation rules every minute (Uzbekistan time,
// UTC+5), even when the app is closed. De-dupes each rule to one fire per minute.
func runRoutines(ctx context.Context, registry *provider.Registry, store *routines.Store) {
	loc := time.FixedZone("UZT", 5*3600)
	fired := map[string]string{}
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		now := time.Now().In(loc)
		stamp := now.Format("200601021504")
		wd := int(now.Weekday())
		if users, err := store.List(ctx); err == nil {
			for _, u := range users {
				for _, r := range u.Routines {
					if !r.On || r.Hour != now.Hour() || r.Minute != now.Minute() {
						continue
					}
					if len(r.Days) > 0 && !containsInt(r.Days, wd) {
						continue
					}
					if fired[r.ID] == stamp {
						continue
					}
					fired[r.ID] = stamp
					execRoutine(ctx, registry, r)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func execRoutine(ctx context.Context, registry *provider.Registry, r routines.Routine) {
	p := registry.For(r.Vehicle)
	var err error
	switch r.Action {
	case "lock":
		err = p.Lock(ctx, r.Vehicle)
	case "unlock":
		err = p.Unlock(ctx, r.Vehicle)
	case "start":
		err = p.RemoteStart(ctx, r.Vehicle)
	case "stop":
		err = p.RemoteStop(ctx, r.Vehicle)
	case "climate_on":
		err = p.SetClimate(ctx, r.Vehicle, true, float64(r.Temp))
	case "climate_off":
		err = p.SetClimate(ctx, r.Vehicle, false, 0)
	}
	log.Printf("routine fired: %s %s vid=%s err=%v", r.ID, r.Action, r.Vehicle, err)
}

func containsInt(a []int, v int) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

// tripState holds the open (in-progress) trip for one vehicle.
type tripState struct {
	startTs  int64
	startOdo int
	startSoc int
	startLat float64
	startLng float64
}

// runTrips samples each vehicle's telemetry every 60s and records a trip when
// the engine transitions on→off. Distance comes from the odometer, energy from
// the battery level. Trips with no movement (odometer unchanged) are dropped.
func runTrips(ctx context.Context, registry *provider.Registry, store *trips.Store, ids []string) {
	open := map[string]*tripState{} // vehicle -> in-progress trip
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		for _, id := range ids {
			snap, err := registry.For(id).Snapshot(ctx, id)
			if err != nil {
				continue
			}
			if snap.EngineOn {
				if open[id] == nil {
					open[id] = &tripState{
						startTs:  time.Now().Unix(),
						startOdo: snap.Health.OdometerKm,
						startSoc: snap.Energy.BatteryLevel,
						startLat: snap.Location.Lat,
						startLng: snap.Location.Lng,
					}
				}
				continue
			}
			// Engine off: close any open trip.
			st := open[id]
			if st == nil {
				continue
			}
			open[id] = nil
			dist := snap.Health.OdometerKm - st.startOdo
			if dist <= 0 {
				continue // no movement, ignore
			}
			tr := trips.Trip{
				ID:       fmt.Sprintf("%s-%d", id, st.startTs),
				StartTs:  st.startTs,
				EndTs:    time.Now().Unix(),
				StartOdo: st.startOdo,
				EndOdo:   snap.Health.OdometerKm,
				DistKm:   dist,
				StartSoc: st.startSoc,
				EndSoc:   snap.Energy.BatteryLevel,
				StartLat: st.startLat,
				StartLng: st.startLng,
				EndLat:   snap.Location.Lat,
				EndLng:   snap.Location.Lng,
			}
			if err := store.Add(ctx, id, tr); err != nil {
				log.Printf("trips: save %s: %v", id, err)
			} else {
				log.Printf("trips: logged %s %dkm", id, dist)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func runScheduler(store *schedule.Store, geo *geofence.Store, registry *provider.Registry, alerter *notify.FCMAlerter) {
	const offset = 5 * time.Hour // Tashkent, no DST
	inside := map[string]bool{}  // last known in-zone state per vehicle
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		now := time.Now().UTC().Add(offset)
		slot := now.Format("2006-01-02-15-04")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		if all, err := store.All(ctx); err == nil {
			for id, sc := range all {
				if !sc.Enabled || sc.Hour != now.Hour() || sc.Minute != now.Minute() {
					continue
				}
				// Only one instance acts on this (vehicle, minute) warm-up.
				if !store.Claim(ctx, "warmup-"+id+"-"+slot) {
					continue
				}
				p := registry.For(id)
				if err := p.RemoteStart(ctx, id); err != nil {
					log.Printf("scheduler: start %s: %v", id, err)
				}
				tC := sc.TargetC
				if tC == 0 {
					tC = 24
				}
				if err := p.SetClimate(ctx, id, true, float64(tC)); err != nil {
					log.Printf("scheduler: climate %s: %v", id, err)
				}
				log.Printf("scheduler: warmed up %s at %02d:%02d (%d°C)", id, sc.Hour, sc.Minute, tC)
			}
		}

		if geo != nil {
			if zones, err := geo.All(ctx); err == nil {
				for id, z := range zones {
					if !z.Enabled {
						continue
					}
					snap, err := registry.For(id).Snapshot(ctx, id)
					if err != nil {
						continue
					}
					in := z.Contains(snap.Location.Lat, snap.Location.Lng)
					if was, seen := inside[id]; seen && was && !in {
						// Exit detected — one instance per minute pushes the alert.
						if store.Claim(ctx, "geoexit-"+id+"-"+slot) {
							log.Printf("geofence: ALERT %s left safe zone (%.5f,%.5f)", id, snap.Location.Lat, snap.Location.Lng)
							if alerter != nil {
								name := id
								if snap.Name != "" {
									name = snap.Name
								}
								alerter.VehicleAlert(ctx, id, "geofence_exit",
									name+" xavfsiz hududdan chiqdi")
							}
						}
					}
					inside[id] = in
				}
			}
		}

		cancel()
		<-t.C
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// atoiOr parses s as an int, returning def on empty/invalid input.
func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// brandConfig builds a live API config for a brand from environment variables,
// e.g. VOYAH_API_URL + VOYAH_API_TOKEN. When the URL is unset the adapter stays
// on the simulator. The token is sent as `Authorization: Bearer <token>`;
// supply a long-lived fleet token (Secret Manager) or swap the TokenSource for
// an OAuth client-credentials exchange once the OEM grants credentials.
func brandConfig(brand string) brands.Config {
	up := strings.ToUpper(brand)
	url := os.Getenv(up + "_API_URL")
	if url == "" {
		return brands.Config{} // not configured → simulator
	}
	token := os.Getenv(up + "_API_TOKEN")
	if token == "" {
		log.Printf("brand %s: %s_API_URL set but %s_API_TOKEN missing — staying on simulator", brand, up, up)
		return brands.Config{}
	}
	log.Printf("brand %s: LIVE API at %s", brand, url)
	return brands.Config{
		BaseURL:     url,
		TokenSource: func(context.Context) (string, error) { return token, nil },
	}
}
