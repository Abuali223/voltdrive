// Package schedule stores a per-vehicle departure timer in Firestore and lets
// the backend act on it: at the set local time the server warms the car up
// (remote start + climate) even if no app is open.
//
//	schedules/{vehicleId}  ->  { enabled, hour, minute, targetC }
package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Schedule is a daily warm-up timer (local Asia/Tashkent time, UTC+5).
type Schedule struct {
	Enabled bool `json:"enabled"`
	Hour    int  `json:"hour"`
	Minute  int  `json:"minute"`
	TargetC int  `json:"targetC"`
}

// Store reads/writes schedules via the Firestore REST API.
type Store struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

func NewStore(projectID string, token func(context.Context) (string, error)) *Store {
	return &Store{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *Store) col() string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/schedules", s.projectID)
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

func (s *Store) Get(ctx context.Context, vehicleID string) (Schedule, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"/"+vehicleID, nil)
	if err := s.authed(ctx, req); err != nil {
		return Schedule{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Schedule{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Schedule{TargetC: 24}, nil // default
	}
	if resp.StatusCode != http.StatusOK {
		return Schedule{}, fmt.Errorf("schedule get: %s", resp.Status)
	}
	return parseDoc(resp.Body)
}

func (s *Store) Put(ctx context.Context, vehicleID string, sc Schedule) error {
	body := map[string]any{"fields": map[string]any{
		"enabled": map[string]any{"booleanValue": sc.Enabled},
		"hour":    map[string]any{"integerValue": strconv.Itoa(sc.Hour)},
		"minute":  map[string]any{"integerValue": strconv.Itoa(sc.Minute)},
		"targetC": map[string]any{"integerValue": strconv.Itoa(sc.TargetC)},
	}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.col()+"/"+vehicleID, bytes.NewReader(b))
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
		return fmt.Errorf("schedule put: %s", resp.Status)
	}
	return nil
}

// All returns every stored schedule keyed by vehicleId (for the ticker).
func (s *Store) All(ctx context.Context) (map[string]Schedule, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"?pageSize=300", nil)
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
		return nil, fmt.Errorf("schedule all: %s", resp.Status)
	}
	var out struct {
		Documents []struct {
			Name   string          `json:"name"`
			Fields json.RawMessage `json:"fields"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	res := make(map[string]Schedule, len(out.Documents))
	for _, d := range out.Documents {
		id := d.Name[len(d.Name)-1:]
		if i := lastSlash(d.Name); i >= 0 {
			id = d.Name[i+1:]
		}
		sc, err := parseFields(d.Fields)
		if err == nil {
			res[id] = sc
		}
	}
	return res, nil
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func parseDoc(r interface{ Read([]byte) (int, error) }) (Schedule, error) {
	var doc struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return Schedule{}, err
	}
	return parseFields(doc.Fields)
}

func parseFields(raw json.RawMessage) (Schedule, error) {
	var f struct {
		Enabled struct {
			BooleanValue bool `json:"booleanValue"`
		} `json:"enabled"`
		Hour struct {
			IntegerValue string `json:"integerValue"`
		} `json:"hour"`
		Minute struct {
			IntegerValue string `json:"integerValue"`
		} `json:"minute"`
		TargetC struct {
			IntegerValue string `json:"integerValue"`
		} `json:"targetC"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return Schedule{}, err
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	return Schedule{
		Enabled: f.Enabled.BooleanValue,
		Hour:    atoi(f.Hour.IntegerValue),
		Minute:  atoi(f.Minute.IntegerValue),
		TargetC: atoi(f.TargetC.IntegerValue),
	}, nil
}
