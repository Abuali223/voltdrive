// Package oemrest is a generic OEM cloud REST client. A brand adapter uses it
// as its live backend the moment real credentials (base URL + token source)
// are configured; until then the adapter falls back to the built-in simulator.
//
// It implements the same method set as sim.Engine, so a brand adapter can hold
// either one behind a small interface and switch on configuration alone — no
// API-layer changes.
//
// IMPORTANT: every manufacturer's cloud API differs (endpoints, auth, JSON
// field names). The request/response shapes below are a sensible default OEM
// REST contract; when an official API spec is granted, adjust the paths in
// command()/Snapshot() and the field tags in apiStatus to match the spec.
// Auth stays as `Authorization: Bearer <token>` from the TokenSource (OAuth
// client-credentials or a long-lived fleet token from Secret Manager).
package oemrest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"voltdrive/backend/internal/provider"
)

// Client talks to one OEM cloud API.
type Client struct {
	BaseURL string
	Token   func(ctx context.Context) (string, error)
	http    *http.Client
}

// New builds a client for the given base URL and bearer-token source.
func New(baseURL string, token func(context.Context) (string, error)) *Client {
	return &Client{BaseURL: baseURL, Token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

// apiStatus is the JSON shape returned by GET /vehicles/{id}/status. Adjust the
// field tags to the real OEM spec; the mapping to provider.Snapshot is below.
type apiStatus struct {
	Online    bool `json:"online"`
	Locked    bool `json:"locked"`
	EngineOn  bool `json:"engineOn"`
	LightsOn  bool `json:"lightsOn"`
	TrunkOpen bool `json:"trunkOpen"`
	Battery   int  `json:"battery"`
	RangeKm   int  `json:"rangeKm"`
	Charging  bool `json:"charging"`
	Climate   struct {
		On       bool    `json:"on"`
		TargetC  float64 `json:"targetC"`
		InsideC  float64 `json:"insideC"`
		OutsideC float64 `json:"outsideC"`
	} `json:"climate"`
	Location struct {
		Lat     float64 `json:"lat"`
		Lng     float64 `json:"lng"`
		Heading float64 `json:"heading"`
		Speed   float64 `json:"speed"`
	} `json:"location"`
	OdometerKm       int    `json:"odometerKm"`
	TirePressuresKpa [4]int `json:"tirePressuresKpa"`
}

// do performs an authenticated request and returns the response body on 2xx.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != nil {
		tok, err := c.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("oemrest: token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Network/transport failures map to a provider error the API turns into 502.
		return nil, fmt.Errorf("oemrest: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, provider.ErrNotFound
	}
	if resp.StatusCode == http.StatusNotImplemented {
		return nil, provider.ErrUnsupported
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oemrest: %s %s: status %s", method, path, resp.Status)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return buf.Bytes(), nil
}

// Snapshot fetches and maps the live vehicle state.
func (c *Client) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	b, err := c.do(ctx, http.MethodGet, "/vehicles/"+id+"/status", nil)
	if err != nil {
		return provider.Snapshot{}, err
	}
	var s apiStatus
	if err := json.Unmarshal(b, &s); err != nil {
		return provider.Snapshot{}, fmt.Errorf("oemrest: decode status: %w", err)
	}
	lock := provider.Unlocked
	if s.Locked {
		lock = provider.Locked
	}
	return provider.Snapshot{
		VehicleID: id,
		Online:    s.Online,
		Lock:      lock,
		EngineOn:  s.EngineOn,
		LightsOn:  s.LightsOn,
		TrunkOpen: s.TrunkOpen,
		Energy:    provider.EnergyState{BatteryLevel: s.Battery, RangeKm: s.RangeKm, Charging: s.Charging},
		Climate:   provider.ClimateState{On: s.Climate.On, TargetC: s.Climate.TargetC, InsideC: s.Climate.InsideC, OutsideC: s.Climate.OutsideC},
		Location:  provider.Location{Lat: s.Location.Lat, Lng: s.Location.Lng, Heading: s.Location.Heading, Speed: s.Location.Speed, UpdatedAt: time.Now().Unix()},
		Health:    provider.Health{OdometerKm: s.OdometerKm, TirePressures: s.TirePressuresKpa},
		UpdatedAt: time.Now().Unix(),
	}, nil
}

// command POSTs a control command to /vehicles/{id}/commands.
func (c *Client) command(ctx context.Context, id string, payload map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, "/vehicles/"+id+"/commands", payload)
	return err
}

func (c *Client) Lock(ctx context.Context, id string) error   { return c.command(ctx, id, map[string]any{"command": "lock"}) }
func (c *Client) Unlock(ctx context.Context, id string) error { return c.command(ctx, id, map[string]any{"command": "unlock"}) }
func (c *Client) RemoteStart(ctx context.Context, id string) error {
	return c.command(ctx, id, map[string]any{"command": "engineStart"})
}
func (c *Client) RemoteStop(ctx context.Context, id string) error {
	return c.command(ctx, id, map[string]any{"command": "engineStop"})
}
func (c *Client) SetClimate(ctx context.Context, id string, on bool, targetC float64) error {
	return c.command(ctx, id, map[string]any{"command": "climate", "on": on, "targetC": targetC})
}
func (c *Client) SetLights(ctx context.Context, id string, on bool) error {
	return c.command(ctx, id, map[string]any{"command": "lights", "on": on})
}
func (c *Client) SetTrunk(ctx context.Context, id string, open bool) error {
	return c.command(ctx, id, map[string]any{"command": "trunk", "open": open})
}
func (c *Client) Honk(ctx context.Context, id string) error {
	return c.command(ctx, id, map[string]any{"command": "horn"})
}
func (c *Client) SetSeat(ctx context.Context, id string, seat provider.SeatCmd) error {
	return c.command(ctx, id, map[string]any{"command": "seat", "seat": seat.Seat, "recline": seat.Recline})
}
