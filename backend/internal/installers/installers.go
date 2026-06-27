// Package installers stores the set of installer emails in Firestore. An
// installer may register/test/remove telematics devices, but is not an admin.
// The super admin manages the installer list.
//
//	installers/{emailKey} -> { email }
package installers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

func (s *Store) col() string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/installers", s.projectID)
}

func key(email string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(email)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
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

// IsInstaller reports whether the email is in the installer set.
func (s *Store) IsInstaller(ctx context.Context, email string) bool {
	if email == "" {
		return false
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"/"+key(email), nil)
	if err := s.authed(ctx, req); err != nil {
		return false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// List returns all installer emails.
func (s *Store) List(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"?pageSize=200", nil)
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
		return nil, fmt.Errorf("installers list: %s", resp.Status)
	}
	var out struct {
		Documents []struct {
			Fields struct {
				Email struct {
					StringValue string `json:"stringValue"`
				} `json:"email"`
			} `json:"fields"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	emails := make([]string, 0, len(out.Documents))
	for _, d := range out.Documents {
		if d.Fields.Email.StringValue != "" {
			emails = append(emails, d.Fields.Email.StringValue)
		}
	}
	return emails, nil
}

// Add inserts an installer email.
func (s *Store) Add(ctx context.Context, email string) error {
	body := []byte(fmt.Sprintf(`{"fields":{"email":{"stringValue":%q}}}`, strings.ToLower(strings.TrimSpace(email))))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.col()+"/"+key(email), bytes.NewReader(body))
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
		return fmt.Errorf("installers add: %s", resp.Status)
	}
	return nil
}

// Remove deletes an installer email.
func (s *Store) Remove(ctx context.Context, email string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, s.col()+"/"+key(email), nil)
	if err := s.authed(ctx, req); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("installers remove: %s", resp.Status)
	}
	return nil
}
