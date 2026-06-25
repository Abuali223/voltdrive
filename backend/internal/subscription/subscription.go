// Package subscription stores per-user plan/billing state in Firestore and
// powers the freemium model: a 14-day free trial, paid tiers (1m / 2m / 1y)
// requested by the user, and manual activation by an admin once payment lands
// (until an official payment gateway is wired in).
//
//	subscriptions/{uid}
//	  { email, plan, status, tier, startedAt, expiresAt, requestedTier, requestedAt }
package subscription

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

// Sub is a user's subscription state.
type Sub struct {
	UID           string `json:"uid"`
	Email         string `json:"email"`
	Plan          string `json:"plan"`   // free | premium
	Status        string `json:"status"` // none | trial | pending | active | expired
	Tier          string `json:"tier"`   // "" | 1m | 2m | 1y
	StartedAt     int64  `json:"startedAt"`
	ExpiresAt     int64  `json:"expiresAt"`
	RequestedTier string `json:"requestedTier"`
	RequestedAt   int64  `json:"requestedAt"`
}

// Active reports whether the user currently has premium access.
func (s Sub) Active() bool {
	return (s.Status == "trial" || s.Status == "active") && s.ExpiresAt > time.Now().Unix()
}

// Normalize downgrades an expired premium to "expired" so reads are truthful.
func (s Sub) Normalize() Sub {
	if (s.Status == "trial" || s.Status == "active") && s.ExpiresAt > 0 && s.ExpiresAt <= time.Now().Unix() {
		s.Status = "expired"
		s.Plan = "free"
	}
	return s
}

// TierDuration maps a tier code to its length: "1y" = a year, "Nm" = N months
// (1–11), anything else = one month.
func TierDuration(tier string) time.Duration {
	if tier == "1y" {
		return 365 * 24 * time.Hour
	}
	if strings.HasSuffix(tier, "m") {
		if n, err := strconv.Atoi(strings.TrimSuffix(tier, "m")); err == nil && n > 0 {
			return time.Duration(n) * 30 * 24 * time.Hour
		}
	}
	return 30 * 24 * time.Hour
}

// Store reads/writes subscriptions via the Firestore REST API.
type Store struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

func NewStore(projectID string, token func(context.Context) (string, error)) *Store {
	return &Store{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *Store) col() string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/subscriptions", s.projectID)
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

func (s *Store) Get(ctx context.Context, uid string) (Sub, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"/"+uid, nil)
	if err := s.authed(ctx, req); err != nil {
		return Sub{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Sub{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Sub{UID: uid, Plan: "free", Status: "none"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Sub{}, fmt.Errorf("sub get: %s", resp.Status)
	}
	var doc struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return Sub{}, err
	}
	sub := parseFields(doc.Fields)
	sub.UID = uid
	return sub, nil
}

func (s *Store) Put(ctx context.Context, sub Sub) error {
	body := map[string]any{"fields": map[string]any{
		"email":         strVal(sub.Email),
		"plan":          strVal(sub.Plan),
		"status":        strVal(sub.Status),
		"tier":          strVal(sub.Tier),
		"startedAt":     intVal(sub.StartedAt),
		"expiresAt":     intVal(sub.ExpiresAt),
		"requestedTier": strVal(sub.RequestedTier),
		"requestedAt":   intVal(sub.RequestedAt),
	}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.col()+"/"+sub.UID, bytes.NewReader(b))
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
		return fmt.Errorf("sub put: %s", resp.Status)
	}
	return nil
}

// All returns every subscription (admin view).
func (s *Store) All(ctx context.Context) ([]Sub, error) {
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
		return nil, fmt.Errorf("sub all: %s", resp.Status)
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
	subs := make([]Sub, 0, len(out.Documents))
	for _, d := range out.Documents {
		sub := parseFields(d.Fields)
		for i := len(d.Name) - 1; i >= 0; i-- {
			if d.Name[i] == '/' {
				sub.UID = d.Name[i+1:]
				break
			}
		}
		subs = append(subs, sub.Normalize())
	}
	return subs, nil
}

func strVal(v string) map[string]any { return map[string]any{"stringValue": v} }
func intVal(v int64) map[string]any  { return map[string]any{"integerValue": strconv.FormatInt(v, 10)} }

func parseFields(raw json.RawMessage) Sub {
	var f struct {
		Email struct {
			StringValue string `json:"stringValue"`
		} `json:"email"`
		Plan struct {
			StringValue string `json:"stringValue"`
		} `json:"plan"`
		Status struct {
			StringValue string `json:"stringValue"`
		} `json:"status"`
		Tier struct {
			StringValue string `json:"stringValue"`
		} `json:"tier"`
		StartedAt struct {
			IntegerValue string `json:"integerValue"`
		} `json:"startedAt"`
		ExpiresAt struct {
			IntegerValue string `json:"integerValue"`
		} `json:"expiresAt"`
		RequestedTier struct {
			StringValue string `json:"stringValue"`
		} `json:"requestedTier"`
		RequestedAt struct {
			IntegerValue string `json:"integerValue"`
		} `json:"requestedAt"`
	}
	_ = json.Unmarshal(raw, &f)
	atoi := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	return Sub{
		Email: f.Email.StringValue, Plan: f.Plan.StringValue, Status: f.Status.StringValue,
		Tier: f.Tier.StringValue, StartedAt: atoi(f.StartedAt.IntegerValue), ExpiresAt: atoi(f.ExpiresAt.IntegerValue),
		RequestedTier: f.RequestedTier.StringValue, RequestedAt: atoi(f.RequestedAt.IntegerValue),
	}
}
