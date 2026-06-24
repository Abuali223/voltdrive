// Package realtime streams live vehicle telemetry to connected apps.
//
// Design:
//   - A poller periodically reads each vehicle's Snapshot from the provider
//     registry and broadcasts it to every client subscribed to that vehicle.
//   - Clients subscribe over WebSocket at /v1/vehicles/{id}/stream.
//
// In production the poller is replaced (or supplemented) by push from the
// manufacturer webhook / MQTT bridge, but the Hub fan-out stays the same.
package realtime

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"voltdrive/backend/internal/provider"
)

// client is one subscribed WebSocket connection.
type client struct {
	vehicleID string
	send      chan []byte
}

// Alerter is defined in security.go.

// TelemetrySink receives every fresh snapshot, e.g. to mirror it into
// Firebase Realtime Database so the apps can subscribe directly.
type TelemetrySink interface {
	Publish(ctx context.Context, snap provider.Snapshot) error
}

// Hub fans telemetry out to subscribers, grouped by vehicle id.
type Hub struct {
	registry *provider.Registry
	alerter  Alerter       // optional: security alert delivery (FCM)
	sink     TelemetrySink // optional: mirror snapshots to RTDB

	mu      sync.RWMutex
	clients map[string]map[*client]struct{} // vehicleID -> set of clients
	last    map[string]provider.Snapshot    // vehicleID -> previous snapshot
	mirror  []string                        // vehicleIDs always mirrored to sink
}

// NewHub creates a Hub backed by the provider registry.
func NewHub(reg *provider.Registry) *Hub {
	return &Hub{
		registry: reg,
		clients:  map[string]map[*client]struct{}{},
		last:     map[string]provider.Snapshot{},
	}
}

// WithAlerter attaches a security alert sink (e.g. FCM) and returns the hub.
func (h *Hub) WithAlerter(a Alerter) *Hub {
	h.alerter = a
	return h
}

// WithSink attaches a telemetry sink (e.g. RTDB writer) and returns the hub.
func (h *Hub) WithSink(s TelemetrySink) *Hub {
	h.sink = s
	return h
}

// mustJSON marshals v, returning an empty object on error.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func (h *Hub) subscribe(vehicleID string) *client {
	c := &client{vehicleID: vehicleID, send: make(chan []byte, 8)}
	h.mu.Lock()
	if h.clients[vehicleID] == nil {
		h.clients[vehicleID] = map[*client]struct{}{}
	}
	h.clients[vehicleID][c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *Hub) unsubscribe(c *client) {
	h.mu.Lock()
	if set := h.clients[c.vehicleID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, c.vehicleID)
		}
	}
	h.mu.Unlock()
	close(c.send)
}

// broadcast sends a payload to every subscriber of a vehicle. Slow clients
// (full buffer) are dropped so one stuck app can't block the others.
func (h *Hub) broadcast(vehicleID string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients[vehicleID] {
		select {
		case c.send <- payload:
		default:
		}
	}
}

// SetMirrorVehicles sets vehicle ids that are always polled and mirrored to
// the telemetry sink (RTDB), even when no WebSocket client is connected.
func (h *Hub) SetMirrorVehicles(ids ...string) *Hub {
	h.mu.Lock()
	h.mirror = ids
	h.mu.Unlock()
	return h
}

// tracked returns the union of WebSocket-subscribed and mirrored vehicle ids.
func (h *Hub) tracked() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	set := map[string]struct{}{}
	for id := range h.clients {
		set[id] = struct{}{}
	}
	for _, id := range h.mirror {
		set[id] = struct{}{}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
}

// Run starts the polling loop until ctx is cancelled. interval controls how
// often live snapshots are pushed (e.g. 2s).
func (h *Hub) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, id := range h.tracked() {
				snap, err := h.registry.For(id).Snapshot(ctx, id)
				if err != nil {
					continue
				}
				// Run security checks against the previous snapshot.
				h.mu.Lock()
				prev, had := h.last[id]
				h.last[id] = snap
				h.mu.Unlock()
				if had {
					p := prev
					h.securityWatch(ctx, &p, snap)
				}
				// Push to WebSocket subscribers.
				if payload, err := json.Marshal(snap); err == nil {
					h.broadcast(id, payload)
				}
				// Mirror to RTDB so app clients can subscribe directly.
				if h.sink != nil {
					if err := h.sink.Publish(ctx, snap); err != nil {
						log.Printf("telemetry sink: %v", err)
					}
				}
			}
		}
	}
}
