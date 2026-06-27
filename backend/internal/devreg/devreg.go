// Package devreg is the registry of installed telematics devices (e.g. a
// StarLine unit fitted to a customer's car). An installer registers a device
// here; the API then binds it into the live provider registry so the app can
// control that real vehicle. Persisted in Firestore so bindings survive restarts.
//
//	vehicle_devices/{deviceID} -> { provider, model, plate, installer, installedAt }
package devreg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Device is one installed telematics unit. DeviceID is the provider's id
// (e.g. the StarLine device id) and doubles as the vehicle id in the registry.
type Device struct {
	DeviceID    string `json:"deviceId"`
	Provider    string `json:"provider"` // starline | ...
	Model       string `json:"model"`
	Plate       string `json:"plate"`
	Installer   string `json:"installer"` // email of the installer
	InstalledAt int64  `json:"installedAt"`
}

type Store struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

func NewStore(projectID string, token func(context.Context) (string, error)) *Store {
	return &Store{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *Store) col() string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/vehicle_devices", s.projectID)
}

// docID sanitizes a device id into a Firestore document id.
func docID(id string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(id) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (s *Store) authed(ctx context.Context, req *http.Request) error {
	if s.token == nil {
		return nil
	}
	t, err := s.token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t)
	return nil
}

func fields(d Device) map[string]any {
	return map[string]any{"fields": map[string]any{
		"deviceId":    map[string]any{"stringValue": d.DeviceID},
		"provider":    map[string]any{"stringValue": d.Provider},
		"model":       map[string]any{"stringValue": d.Model},
		"plate":       map[string]any{"stringValue": d.Plate},
		"installer":   map[string]any{"stringValue": d.Installer},
		"installedAt": map[string]any{"integerValue": fmt.Sprintf("%d", d.InstalledAt)},
	}}
}

func parse(f map[string]struct {
	StringValue  string `json:"stringValue"`
	IntegerValue string `json:"integerValue"`
}) Device {
	var at int64
	fmt.Sscan(f["installedAt"].IntegerValue, &at)
	return Device{
		DeviceID: f["deviceId"].StringValue, Provider: f["provider"].StringValue,
		Model: f["model"].StringValue, Plate: f["plate"].StringValue,
		Installer: f["installer"].StringValue, InstalledAt: at,
	}
}

// Put creates or updates a device record.
func (s *Store) Put(ctx context.Context, d Device) error {
	b, _ := json.Marshal(fields(d))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.col()+"/"+docID(d.DeviceID), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if err := s.authed(ctx, req); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("devreg put: %s", resp.Status)
	}
	return nil
}

// Delete removes a device record.
func (s *Store) Delete(ctx context.Context, deviceID string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, s.col()+"/"+docID(deviceID), nil)
	if err := s.authed(ctx, req); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("devreg delete: %s", resp.Status)
	}
	return nil
}

// List returns every registered device.
func (s *Store) List(ctx context.Context) ([]Device, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"?pageSize=500", nil)
	if err := s.authed(ctx, req); err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devreg list: %s", resp.Status)
	}
	var out struct {
		Documents []struct {
			Fields map[string]struct {
				StringValue  string `json:"stringValue"`
				IntegerValue string `json:"integerValue"`
			} `json:"fields"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	res := make([]Device, 0, len(out.Documents))
	for _, d := range out.Documents {
		res = append(res, parse(d.Fields))
	}
	return res, nil
}
