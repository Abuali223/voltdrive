// Package guestkey issues time-limited, scope-limited "guest keys" for a
// vehicle. The key code itself is the bearer credential: a guest with no
// account can redeem it and send only the allowed commands until it expires
// or the owner revokes it.
//
//	guestkeys/{code} -> { vehicleId, scope, expiresAt, label, createdBy, revoked }
package guestkey

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Key is a single guest grant.
type Key struct {
	Code      string   `json:"code"`
	VehicleID string   `json:"vehicleId"`
	Scope     []string `json:"scope"` // subset of: unlock, lock, start
	ExpiresAt int64    `json:"expiresAt"`
	Label     string   `json:"label"`
	Revoked   bool     `json:"revoked"`
}

// Active reports whether the key may still be used right now.
func (k Key) Active() bool {
	return !k.Revoked && k.ExpiresAt > time.Now().Unix()
}

// Allows reports whether action is within the key's scope.
func (k Key) Allows(action string) bool {
	for _, s := range k.Scope {
		if s == action {
			return true
		}
	}
	return false
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
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/guestkeys", s.projectID)
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

// newCode returns an 8-char unambiguous code (no 0/O/1/I) for easy sharing.
func newCode() string {
	const charset = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

func (s *Store) Create(ctx context.Context, k Key, createdBy string) (Key, error) {
	k.Code = newCode()
	body := map[string]any{"fields": map[string]any{
		"vehicleId": map[string]any{"stringValue": k.VehicleID},
		"scope":     map[string]any{"stringValue": strings.Join(k.Scope, ",")},
		"expiresAt": map[string]any{"integerValue": strconv.FormatInt(k.ExpiresAt, 10)},
		"label":     map[string]any{"stringValue": k.Label},
		"createdBy": map[string]any{"stringValue": createdBy},
		"revoked":   map[string]any{"booleanValue": false},
	}}
	b, _ := json.Marshal(body)
	url := s.col() + "?documentId=" + k.Code
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if err := s.authed(ctx, req); err != nil {
		return Key{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Key{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Key{}, fmt.Errorf("guestkey create: %s", resp.Status)
	}
	return k, nil
}

func (s *Store) Get(ctx context.Context, code string) (Key, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"/"+code, nil)
	if err := s.authed(ctx, req); err != nil {
		return Key{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Key{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Key{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Key{}, fmt.Errorf("guestkey get: %s", resp.Status)
	}
	var doc struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return Key{}, err
	}
	k := parseFields(doc.Fields)
	k.Code = code
	return k, nil
}

func (s *Store) Revoke(ctx context.Context, code string) error {
	body := map[string]any{"fields": map[string]any{"revoked": map[string]any{"booleanValue": true}}}
	b, _ := json.Marshal(body)
	url := s.col() + "/" + code + "?updateMask.fieldPaths=revoked"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
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
		return fmt.Errorf("guestkey revoke: %s", resp.Status)
	}
	return nil
}

// ListActive returns the still-valid keys for a vehicle.
func (s *Store) ListActive(ctx context.Context, vehicleID string) ([]Key, error) {
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
		return nil, fmt.Errorf("guestkey list: %s", resp.Status)
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
	var keys []Key
	for _, d := range out.Documents {
		k := parseFields(d.Fields)
		if i := strings.LastIndex(d.Name, "/"); i >= 0 {
			k.Code = d.Name[i+1:]
		}
		if k.VehicleID == vehicleID && k.Active() {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func parseFields(raw json.RawMessage) Key {
	var f struct {
		VehicleID struct {
			StringValue string `json:"stringValue"`
		} `json:"vehicleId"`
		Scope struct {
			StringValue string `json:"stringValue"`
		} `json:"scope"`
		ExpiresAt struct {
			IntegerValue string `json:"integerValue"`
		} `json:"expiresAt"`
		Label struct {
			StringValue string `json:"stringValue"`
		} `json:"label"`
		Revoked struct {
			BooleanValue bool `json:"booleanValue"`
		} `json:"revoked"`
	}
	_ = json.Unmarshal(raw, &f)
	exp, _ := strconv.ParseInt(f.ExpiresAt.IntegerValue, 10, 64)
	var scope []string
	if f.Scope.StringValue != "" {
		scope = strings.Split(f.Scope.StringValue, ",")
	}
	return Key{
		VehicleID: f.VehicleID.StringValue,
		Scope:     scope,
		ExpiresAt: exp,
		Label:     f.Label.StringValue,
		Revoked:   f.Revoked.BooleanValue,
	}
}

// ErrNotFound is returned when a code does not exist.
var ErrNotFound = fmt.Errorf("guest key not found")
