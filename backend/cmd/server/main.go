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
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"voltdrive/backend/internal/api"
	"voltdrive/backend/internal/auth"
	"voltdrive/backend/internal/devices"
	"voltdrive/backend/internal/gcp"
	"voltdrive/backend/internal/geofence"
	"voltdrive/backend/internal/guestkey"
	"voltdrive/backend/internal/members"
	"voltdrive/backend/internal/notify"
	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/brands"
	"voltdrive/backend/internal/provider/mock"
	"voltdrive/backend/internal/realtime"
	"voltdrive/backend/internal/rtdb"
	"voltdrive/backend/internal/schedule"
	"voltdrive/backend/internal/subscription"
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
	if projectID != "" {
		dsToken := gcp.NewTokenSource("https://www.googleapis.com/auth/datastore").Token
		scheduleStore = schedule.NewStore(projectID, dsToken)
		guestStore = guestkey.NewStore(projectID, dsToken)
		geoStore = geofence.NewStore(projectID, dsToken)
		subStore = subscription.NewStore(projectID, dsToken)
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
