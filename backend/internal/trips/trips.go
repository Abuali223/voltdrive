// Package trips records driving trips per vehicle in Firestore. A 60-second
// watcher samples each vehicle's telemetry; when the engine turns on it opens a
// trip, and when it turns off it closes the trip (distance from the odometer,
// energy used from the battery level, start/end GPS) and prepends it to the
// vehicle's history — so trips appear even when the app was closed.
//
//	trips/{vehicleID}  ->  { items: <JSON array of Trip, newest first> }
package trips

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// maxTrips caps stored history per vehicle.
const maxTrips = 100

// Trip is one completed drive.
type Trip struct {
	ID       string  `json:"id"`
	StartTs  int64   `json:"st"` // unix seconds
	EndTs    int64   `json:"et"` // unix seconds
	StartOdo int     `json:"so"` // km
	EndOdo   int     `json:"eo"` // km
	DistKm   int     `json:"d"`  // km driven
	StartSoc int     `json:"ss"` // battery % at start
	EndSoc   int     `json:"es"` // battery % at end
	StartLat float64 `json:"sla"`
	StartLng float64 `json:"slo"`
	EndLat   float64 `json:"ela"`
	EndLng   float64 `json:"elo"`
}

type Store struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

func NewStore(projectID string, token func(context.Context) (string, error)) *Store {
	return &Store{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *Store) doc(vid string) string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/trips/%s", s.projectID, vid)
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

// Get returns one vehicle's trip history (newest first).
func (s *Store) Get(ctx context.Context, vid string) ([]Trip, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.doc(vid), nil)
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
		return nil, fmt.Errorf("trips get: %s", resp.Status)
	}
	var d struct {
		Fields struct {
			Items struct {
				StringValue string `json:"stringValue"`
			} `json:"items"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return parseItems(d.Fields.Items.StringValue), nil
}

// put replaces one vehicle's trip history.
func (s *Store) put(ctx context.Context, vid string, list []Trip) error {
	j, _ := json.Marshal(list)
	body := map[string]any{"fields": map[string]any{"items": map[string]any{"stringValue": string(j)}}}
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
		return fmt.Errorf("trips put: %s", resp.Status)
	}
	return nil
}

// Add prepends a finished trip to a vehicle's history (newest first, capped).
func (s *Store) Add(ctx context.Context, vid string, t Trip) error {
	list, err := s.Get(ctx, vid)
	if err != nil {
		return err
	}
	list = append([]Trip{t}, list...)
	if len(list) > maxTrips {
		list = list[:maxTrips]
	}
	return s.put(ctx, vid, list)
}

func parseItems(s string) []Trip {
	if s == "" {
		return nil
	}
	var list []Trip
	_ = json.Unmarshal([]byte(s), &list)
	return list
}
