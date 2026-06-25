// Package branding stores the white-label theme (name, colours, logo) in
// Firestore so a super admin can rebrand the whole app from the admin panel —
// no config-file edit or redeploy needed.
//
//	appconfig/branding  ->  { name, tagline, accent, accent2, accentSolid, logo }
package branding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Branding struct {
	Name        string `json:"name"`
	Tagline     string `json:"tagline"`
	Accent      string `json:"accent"`
	Accent2     string `json:"accent2"`
	AccentSolid string `json:"accentSolid"`
	Logo        string `json:"logo"`
}

type Store struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

func NewStore(projectID string, token func(context.Context) (string, error)) *Store {
	return &Store{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *Store) doc() string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/appconfig/branding", s.projectID)
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

// Get returns the saved branding (ok=false when none has been set yet).
func (s *Store) Get(ctx context.Context) (Branding, bool, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.doc(), nil)
	if err := s.authed(ctx, req); err != nil {
		return Branding{}, false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Branding{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Branding{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Branding{}, false, fmt.Errorf("branding get: %s", resp.Status)
	}
	var d struct {
		Fields map[string]struct {
			StringValue string `json:"stringValue"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return Branding{}, false, err
	}
	g := func(k string) string { return d.Fields[k].StringValue }
	return Branding{
		Name: g("name"), Tagline: g("tagline"), Accent: g("accent"),
		Accent2: g("accent2"), AccentSolid: g("accentSolid"), Logo: g("logo"),
	}, true, nil
}

func (s *Store) Put(ctx context.Context, b Branding) error {
	sv := func(v string) map[string]any { return map[string]any{"stringValue": v} }
	body := map[string]any{"fields": map[string]any{
		"name": sv(b.Name), "tagline": sv(b.Tagline), "accent": sv(b.Accent),
		"accent2": sv(b.Accent2), "accentSolid": sv(b.AccentSolid), "logo": sv(b.Logo),
	}}
	j, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.doc(), bytes.NewReader(j))
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
		return fmt.Errorf("branding put: %s", resp.Status)
	}
	return nil
}
