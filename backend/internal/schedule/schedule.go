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
	"strings"
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

// Claim atomically reserves a one-shot token so that, across multiple server
// instances, only the first caller acts on a given event (e.g. a warm-up slot
// or a geofence-exit alert). It uses a Firestore create-if-absent write:
// success (2xx) means this caller won; 409 ALREADY_EXISTS means another
// instance already handled it. Any other error returns true (fail-open) so a
// transient Firestore hiccup never silently skips a scheduled action.
func (s *Store) Claim(ctx context.Context, token string) bool {
	token = sanitizeToken(token)
	body := map[string]any{"fields": map[string]any{
		"ts": map[string]any{"integerValue": strconv.FormatInt(time.Now().Unix(), 10)},
	}}
	b, _ := json.Marshal(body)
	base := fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/claims", s.projectID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"?documentId="+token, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if err := s.authed(ctx, req); err != nil {
		return true
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return true // fail-open: better to risk a rare duplicate than skip entirely
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return false // another instance already claimed this token
	}
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// sanitizeToken makes an arbitrary string safe as a Firestore document ID
// (no '/', no reserved values; keep it short and URL-clean).
func sanitizeToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
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
