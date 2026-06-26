package realtime

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// ServeWS upgrades an HTTP request to a WebSocket and streams telemetry for
// the given vehicle until the client disconnects. Authentication/authorization
// must be performed by the caller before invoking this handler.
//
// originPatterns restricts which web origins may open the socket (host
// patterns, e.g. "eldi-79bf9.web.app"). An empty list allows any origin
// (development only).
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, vehicleID string, originPatterns []string) {
	opts := &websocket.AcceptOptions{
		OriginPatterns: originPatterns,
		// The client offers ["voltdrive.v1", "jwt.<token>"]; negotiate the clean
		// marker so the handshake response never echoes the token back.
		Subprotocols: []string{"voltdrive.v1"},
	}
	if len(originPatterns) == 0 {
		opts.InsecureSkipVerify = true // dev: accept any origin
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4096) // clients only send pings/control, never large data

	c := h.subscribe(vehicleID)
	defer h.unsubscribe(c)

	ctx := r.Context()

	// Send an immediate snapshot so the UI paints without waiting a tick.
	if snap, err := h.registry.For(vehicleID).Snapshot(ctx, vehicleID); err == nil {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = conn.Write(wctx, websocket.MessageText, mustJSON(snap))
		cancel()
	}

	// Reader goroutine: detect client disconnect / honour ping-pong.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	// Keepalive ping detects half-open connections so they are cleaned up.
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case payload, ok := <-c.send:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
