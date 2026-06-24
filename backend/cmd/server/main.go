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
	"strings"
	"syscall"
	"time"

	"voltdrive/backend/internal/api"
	"voltdrive/backend/internal/auth"
	"voltdrive/backend/internal/devices"
	"voltdrive/backend/internal/gcp"
	"voltdrive/backend/internal/members"
	"voltdrive/backend/internal/notify"
	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/brands"
	"voltdrive/backend/internal/provider/mock"
	"voltdrive/backend/internal/realtime"
	"voltdrive/backend/internal/rtdb"
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
	if os.Getenv("FCM_ENABLED") == "1" && projectID != "" {
		fcmToken := gcp.NewTokenSource(
			"https://www.googleapis.com/auth/firebase.messaging",
		).Token
		fcm = notify.NewFCM(projectID, fcmToken)
		deviceStore = devices.NewStore(projectID, gcp.NewTokenSource(
			"https://www.googleapis.com/auth/datastore",
		).Token)
		// Alerts fan out to every registered device (single-owner demo).
		alerter := notify.NewFCMAlerter(fcm, func(ctx context.Context, _ string) ([]string, error) {
			return deviceStore.ListTokens(ctx)
		})
		hub.WithAlerter(alerter)
		log.Printf("alerts: FCM enabled (Firestore device registry)")
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
		Registry:       registry,
		Verifier:       verifier,
		Perms:          perms,
		Hub:            hub,
		Members:        memberStore,
		Devices:        deviceStore,
		FCM:            fcm,
		AllowedOrigins: origins,
		RatePerSec:     20,
		RateBurst:      40,
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
