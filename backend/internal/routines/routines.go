// Package routines stores per-user automation rules ("every day at 07:00 warm
// up the car") in Firestore and is read by a 1-minute watcher that fires the
// due actions server-side — so they run even when the app is closed.
//
//	routines/{uid}  ->  { items: <JSON array of Routine> }
package routines

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Routine is one automation rule.
type Routine struct {
	ID      string `json:"id"`
	Hour    int    `json:"h"`
	Minute  int    `json:"m"`
	Days    []int  `json:"days"`   // 0=Sun .. 6=Sat; empty = every day
	Action  string `json:"action"` // lock|unlock|start|stop|climate_on|climate_off
	Temp    int    `json:"temp"`
	Vehicle string `json:"vid"`
	On      bool   `json:"on"`
}

// UserRoutines pairs a user id with their routines (for the watcher).
type UserRoutines struct {
	UID      string
	Routines []Routine
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
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/routines", s.projectID)
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

// Get returns one user's routines.
func (s *Store) Get(ctx context.Context, uid string) ([]Routine, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"/"+uid, nil)
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
		return nil, fmt.Errorf("routines get: %s", resp.Status)
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

// Put replaces one user's routines.
func (s *Store) Put(ctx context.Context, uid string, list []Routine) error {
	j, _ := json.Marshal(list)
	body := map[string]any{"fields": map[string]any{"items": map[string]any{"stringValue": string(j)}}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.col()+"/"+uid, bytes.NewReader(b))
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
		return fmt.Errorf("routines put: %s", resp.Status)
	}
	return nil
}

// List returns every user's routines (for the watcher).
func (s *Store) List(ctx context.Context) ([]UserRoutines, error) {
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
		return nil, fmt.Errorf("routines list: %s", resp.Status)
	}
	var out struct {
		Documents []struct {
			Name   string `json:"name"`
			Fields struct {
				Items struct {
					StringValue string `json:"stringValue"`
				} `json:"items"`
			} `json:"fields"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	res := make([]UserRoutines, 0, len(out.Documents))
	for _, d := range out.Documents {
		uid := d.Name
		for i := len(uid) - 1; i >= 0; i-- {
			if uid[i] == '/' {
				uid = uid[i+1:]
				break
			}
		}
		res = append(res, UserRoutines{UID: uid, Routines: parseItems(d.Fields.Items.StringValue)})
	}
	return res, nil
}

func parseItems(s string) []Routine {
	if s == "" {
		return nil
	}
	var list []Routine
	_ = json.Unmarshal([]byte(s), &list)
	return list
}
