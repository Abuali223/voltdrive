// Package members manages family/shared access to a vehicle, persisted in
// Firestore. Each member is a document under:
//
//	vehicle_members/{vehicleId}/members/{email}
//	fields: { email, role, addedAt }
//
// This is the owner-facing sharing list. Role enforcement on commands is done
// separately by auth.FirestorePermissions (vehicle_access/{vehicleId}_{uid});
// when RBAC is open (single-owner demo) this list is informational + ready for
// enforcement once uid mapping is enabled.
package members

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// Member is one shared user.
type Member struct {
	Email   string `json:"email"`
	Role    string `json:"role"`
	AddedAt int64  `json:"addedAt"`
}

// Store reads/writes members via the Firestore REST API.
type Store struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

// NewStore builds a Firestore-backed member store.
func NewStore(projectID string, token func(context.Context) (string, error)) *Store {
	return &Store{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *Store) base(vehicleID string) string {
	return fmt.Sprintf(
		"https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/vehicle_members/%s/members",
		url.PathEscape(s.projectID), url.PathEscape(vehicleID),
	)
}

func (s *Store) auth(ctx context.Context, req *http.Request) error {
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

// List returns all members for a vehicle, sorted by email.
func (s *Store) List(ctx context.Context, vehicleID string) ([]Member, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base(vehicleID), nil)
	if err != nil {
		return nil, err
	}
	if err := s.auth(ctx, req); err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return []Member{}, nil // no members collection yet
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("members list: status %s", resp.Status)
	}
	var out struct {
		Documents []struct {
			Fields struct {
				Email   struct{ StringValue string `json:"stringValue"` } `json:"email"`
				Role    struct{ StringValue string `json:"stringValue"` } `json:"role"`
				AddedAt struct{ IntegerValue string `json:"integerValue"` } `json:"addedAt"`
			} `json:"fields"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	members := make([]Member, 0, len(out.Documents))
	for _, d := range out.Documents {
		var at int64
		fmt.Sscan(d.Fields.AddedAt.IntegerValue, &at)
		members = append(members, Member{Email: d.Fields.Email.StringValue, Role: d.Fields.Role.StringValue, AddedAt: at})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].AddedAt < members[j].AddedAt })
	return members, nil
}

// Put adds or updates a member.
func (s *Store) Put(ctx context.Context, vehicleID string, m Member) error {
	if m.AddedAt == 0 {
		m.AddedAt = time.Now().Unix()
	}
	body := map[string]any{"fields": map[string]any{
		"email":   map[string]any{"stringValue": m.Email},
		"role":    map[string]any{"stringValue": m.Role},
		"addedAt": map[string]any{"integerValue": fmt.Sprintf("%d", m.AddedAt)},
	}}
	b, _ := json.Marshal(body)
	urlStr := s.base(vehicleID) + "/" + url.PathEscape(m.Email)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, urlStr, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := s.auth(ctx, req); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("members put: status %s", resp.Status)
	}
	return nil
}

// Remove deletes a member by email.
func (s *Store) Remove(ctx context.Context, vehicleID, email string) error {
	urlStr := s.base(vehicleID) + "/" + url.PathEscape(email)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, urlStr, nil)
	if err != nil {
		return err
	}
	if err := s.auth(ctx, req); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("members remove: status %s", resp.Status)
	}
	return nil
}
