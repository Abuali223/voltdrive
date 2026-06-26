// Package immobilizer stores a per-vehicle anti-theft "engine disabled" flag in
// Firestore. When a vehicle is immobilized the API refuses remote-start commands
// (owner, guest and automation), so a stolen car can be locked down remotely.
//
//	immobilizer/{vehicleID}  ->  { on: <bool> }
package immobilizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Store struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

func NewStore(projectID string, token func(context.Context) (string, error)) *Store {
	return &Store{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *Store) doc(vid string) string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/immobilizer/%s", s.projectID, vid)
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

// Get reports whether the vehicle is currently immobilized.
func (s *Store) Get(ctx context.Context, vid string) (bool, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.doc(vid), nil)
	if err := s.authed(ctx, req); err != nil {
		return false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("immobilizer get: %s", resp.Status)
	}
	var d struct {
		Fields struct {
			On struct {
				BooleanValue bool `json:"booleanValue"`
			} `json:"on"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return false, err
	}
	return d.Fields.On.BooleanValue, nil
}

// Set turns the immobilizer on or off for a vehicle.
func (s *Store) Set(ctx context.Context, vid string, on bool) error {
	body := map[string]any{"fields": map[string]any{"on": map[string]any{"booleanValue": on}}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.doc(vid), bytes.NewReader(b))
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
		return fmt.Errorf("immobilizer set: %s", resp.Status)
	}
	return nil
}
