// Package devices stores FCM registration tokens in Firestore so the backend
// can push security alerts to a user's phone(s).
//
//	devices/{sha256(token)}  ->  { token, uid, updatedAt }
package devices

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Store reads/writes device tokens via the Firestore REST API.
type Store struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

// NewStore builds a Firestore-backed device-token store.
func NewStore(projectID string, token func(context.Context) (string, error)) *Store {
	return &Store{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *Store) base() string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/devices", s.projectID)
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

// Register stores (or refreshes) a device token for a user.
func (s *Store) Register(ctx context.Context, uid, token string) error {
	sum := sha256.Sum256([]byte(token))
	id := hex.EncodeToString(sum[:])
	body := map[string]any{"fields": map[string]any{
		"token":     map[string]any{"stringValue": token},
		"uid":       map[string]any{"stringValue": uid},
		"updatedAt": map[string]any{"integerValue": fmt.Sprintf("%d", time.Now().Unix())},
	}}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, s.base()+"/"+id, bytes.NewReader(b))
	if err != nil {
		return err
	}
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
		return fmt.Errorf("devices register: status %s", resp.Status)
	}
	return nil
}

// ListTokens returns all registered device tokens. (Single-owner demo: the
// owner is alerted for their vehicles. Multi-user filtering by vehicle access
// is a later refinement.)
func (s *Store) ListTokens(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base()+"?pageSize=300", nil)
	if err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("devices list: status %s", resp.Status)
	}
	var out struct {
		Documents []struct {
			Fields struct {
				Token struct {
					StringValue string `json:"stringValue"`
				} `json:"token"`
			} `json:"fields"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	tokens := make([]string, 0, len(out.Documents))
	for _, d := range out.Documents {
		if d.Fields.Token.StringValue != "" {
			tokens = append(tokens, d.Fields.Token.StringValue)
		}
	}
	return tokens, nil
}
