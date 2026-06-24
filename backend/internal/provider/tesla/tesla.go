// Package tesla implements provider.VehicleProvider against the official
// Tesla Fleet API.
//
//	Docs: https://developer.tesla.com/docs/fleet-api
//
// Auth: a partner OAuth token is supplied by TokenSource (loaded from Secret
// Manager and refreshed elsewhere). Commands return Tesla's async result; we
// surface failures as errors so the API layer reports them cleanly.
package tesla

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"voltdrive/backend/internal/provider"
)

// Config holds Fleet API connection settings.
type Config struct {
	// Regional Fleet API base, e.g.
	// https://fleet-api.prd.eu.vn.cloud.tesla.com
	BaseURL string
	// TokenSource returns a valid OAuth access token for the owner.
	TokenSource func(ctx context.Context) (string, error)
}

// Adapter talks to the Tesla Fleet API.
type Adapter struct {
	cfg    Config
	client *http.Client
}

// New returns a configured Tesla adapter.
func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}}
}

func (a *Adapter) Brand() string { return "tesla" }

// --- commands ---

func (a *Adapter) Lock(ctx context.Context, id string) error {
	return a.command(ctx, id, "door_lock", nil)
}
func (a *Adapter) Unlock(ctx context.Context, id string) error {
	return a.command(ctx, id, "door_unlock", nil)
}
func (a *Adapter) RemoteStart(ctx context.Context, id string) error {
	return a.command(ctx, id, "remote_start_drive", nil)
}
func (a *Adapter) RemoteStop(ctx context.Context, id string) error {
	// Tesla has no "engine stop"; HVAC stop is the closest remote-off action.
	return a.command(ctx, id, "auto_conditioning_stop", nil)
}
func (a *Adapter) SetClimate(ctx context.Context, id string, on bool, targetC float64) error {
	cmd := "auto_conditioning_start"
	if !on {
		cmd = "auto_conditioning_stop"
	}
	if err := a.command(ctx, id, cmd, nil); err != nil {
		return err
	}
	if on && targetC > 0 {
		body := map[string]any{"driver_temp": targetC, "passenger_temp": targetC}
		return a.command(ctx, id, "set_temps", body)
	}
	return nil
}

// command POSTs to /api/1/vehicles/{id}/command/{name} and checks the
// {"response":{"result":true}} envelope.
func (a *Adapter) command(ctx context.Context, id, name string, body any) error {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		buf = bytes.NewReader(b)
	}
	path := fmt.Sprintf("/api/1/vehicles/%s/command/%s", id, name)
	resp, err := a.do(ctx, http.MethodPost, path, buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return provider.ErrNotFound
	}
	if resp.StatusCode == http.StatusRequestTimeout {
		return fmt.Errorf("tesla: vehicle asleep/unreachable")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("tesla: command %s failed: %s", name, resp.Status)
	}
	var env struct {
		Response struct {
			Result bool   `json:"result"`
			Reason string `json:"reason"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if !env.Response.Result {
		return fmt.Errorf("tesla: command %s rejected: %s", name, env.Response.Reason)
	}
	return nil
}

// --- state ---

// Snapshot maps GET /api/1/vehicles/{id}/vehicle_data into our model.
func (a *Adapter) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	resp, err := a.do(ctx, http.MethodGet, "/api/1/vehicles/"+id+"/vehicle_data", nil)
	if err != nil {
		return provider.Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return provider.Snapshot{}, provider.ErrNotFound
	}
	if resp.StatusCode >= 400 {
		return provider.Snapshot{}, fmt.Errorf("tesla: vehicle_data %s", resp.Status)
	}

	var vd struct {
		Response struct {
			DisplayName string `json:"display_name"`
			State       string `json:"state"`
			ChargeState struct {
				BatteryLevel    int     `json:"battery_level"`
				BatteryRange    float64 `json:"battery_range"` // miles
				ChargingState   string  `json:"charging_state"`
				ChargerPower    float64 `json:"charger_power"`
			} `json:"charge_state"`
			ClimateState struct {
				IsClimateOn   bool    `json:"is_climate_on"`
				InsideTemp    float64 `json:"inside_temp"`
				OutsideTemp   float64 `json:"outside_temp"`
				DriverTemp    float64 `json:"driver_temp_setting"`
			} `json:"climate_state"`
			DriveState struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
				Heading   float64 `json:"heading"`
				Speed     float64 `json:"speed"` // mph (nullable)
			} `json:"drive_state"`
			VehicleState struct {
				Locked   bool    `json:"locked"`
				Odometer float64 `json:"odometer"` // miles
			} `json:"vehicle_state"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&vd); err != nil {
		return provider.Snapshot{}, err
	}
	r := vd.Response

	lock := provider.Unlocked
	if r.VehicleState.Locked {
		lock = provider.Locked
	}
	const mi2km = 1.609344
	return provider.Snapshot{
		VehicleID: id,
		Name:      r.DisplayName,
		Online:    r.State == "online",
		Lock:      lock,
		EngineOn:  r.DriveState.Speed > 0 || r.ClimateState.IsClimateOn,
		Energy: provider.EnergyState{
			BatteryLevel: r.ChargeState.BatteryLevel,
			RangeKm:      int(r.ChargeState.BatteryRange * mi2km),
			Charging:     r.ChargeState.ChargingState == "Charging",
			ChargeRateKw: r.ChargeState.ChargerPower,
		},
		Climate: provider.ClimateState{
			On:       r.ClimateState.IsClimateOn,
			TargetC:  r.ClimateState.DriverTemp,
			InsideC:  r.ClimateState.InsideTemp,
			OutsideC: r.ClimateState.OutsideTemp,
		},
		Location: provider.Location{
			Lat:       r.DriveState.Latitude,
			Lng:       r.DriveState.Longitude,
			Heading:   r.DriveState.Heading,
			Speed:     r.DriveState.Speed * mi2km,
			UpdatedAt: time.Now().Unix(),
		},
		Health:    provider.Health{OdometerKm: int(r.VehicleState.Odometer * mi2km)},
		UpdatedAt: time.Now().Unix(),
	}, nil
}

// do performs an authenticated request against the Fleet API.
func (a *Adapter) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	token, err := a.cfg.TokenSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("tesla: token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return a.client.Do(req)
}
