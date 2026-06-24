// Package rtdb writes live vehicle telemetry to Firebase Realtime Database.
//
// Why RTDB (not Firestore) for telemetry: high-frequency snapshots (every
// 1-2s) are cheap and instant on RTDB, and the apps get push updates for
// free via the Firebase client SDKs — no custom WebSocket needed on the
// frontend.
//
// Data layout:
//
//	/vehicles/{vehicleId}   -> latest Snapshot JSON
//
// Auth: writes use the database REST API with a Google OAuth access token
// (scopes: firebase.database + userinfo.email) from the Cloud Run service
// account, supplied by TokenSource.
package rtdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"voltdrive/backend/internal/provider"
)

// Writer pushes snapshots to a Realtime Database instance.
type Writer struct {
	// BaseURL is the RTDB instance, e.g.
	// https://eldi-79bf9-default-rtdb.firebaseio.com
	BaseURL     string
	TokenSource func(ctx context.Context) (string, error)
	client      *http.Client
}

// NewWriter builds an RTDB writer.
func NewWriter(baseURL string, ts func(context.Context) (string, error)) *Writer {
	return &Writer{
		BaseURL:     baseURL,
		TokenSource: ts,
		client:      &http.Client{Timeout: 8 * time.Second},
	}
}

// Publish implements realtime.TelemetrySink: write the snapshot at
// /vehicles/{id}. Uses PUT so the node always holds the latest full state.
func (w *Writer) Publish(ctx context.Context, snap provider.Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/vehicles/%s.json", w.BaseURL, snap.VehicleID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// OAuth token in the Authorization header (not the URL — avoids leaking
	// credentials into logs/proxies).
	if w.TokenSource != nil {
		token, err := w.TokenSource(ctx)
		if err != nil {
			return fmt.Errorf("rtdb: token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("rtdb: write %s failed: %s", snap.VehicleID, resp.Status)
	}
	return nil
}
