// Package commands records a per-vehicle history of remote commands (who ran
// lock/unlock/start/…, when, and whether it succeeded) in Firestore, so the app
// can show an audit trail. It complements the log-only audit() with durable,
// user-visible history.
//
//	commands/{vehicleID}  ->  { items: <JSON array of Command, newest first> }
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// maxHistory caps stored commands per vehicle.
const maxHistory = 100

// Command is one issued remote command and its outcome.
type Command struct {
	ID     string `json:"id"`
	Action string `json:"action"` // lock|unlock|start|stop|climate|...
	UID    string `json:"uid"`
	Email  string `json:"email"`
	Ts     int64  `json:"ts"` // unix seconds
	OK     bool   `json:"ok"`
	Err    string `json:"err,omitempty"`
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
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/commands/%s", s.projectID, vid)
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

// Get returns a vehicle's command history (newest first).
func (s *Store) Get(ctx context.Context, vid string) ([]Command, error) {
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
		return nil, fmt.Errorf("commands get: %s", resp.Status)
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

func (s *Store) put(ctx context.Context, vid string, list []Command) error {
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
		return fmt.Errorf("commands put: %s", resp.Status)
	}
	return nil
}

// Add prepends a command to a vehicle's history (newest first, capped).
func (s *Store) Add(ctx context.Context, vid string, c Command) error {
	list, err := s.Get(ctx, vid)
	if err != nil {
		return err
	}
	list = append([]Command{c}, list...)
	if len(list) > maxHistory {
		list = list[:maxHistory]
	}
	return s.put(ctx, vid, list)
}

func parseItems(s string) []Command {
	if s == "" {
		return nil
	}
	var list []Command
	_ = json.Unmarshal([]byte(s), &list)
	return list
}
