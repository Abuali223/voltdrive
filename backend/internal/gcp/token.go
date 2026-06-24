// Package gcp provides a Google OAuth access-token source backed by the
// Cloud Run metadata server. No external SDK required: on Cloud Run the
// attached service account's token is available over HTTP, and we cache it
// until shortly before it expires.
package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"

// TokenSource returns cached OAuth access tokens for the given scopes.
type TokenSource struct {
	scopes string
	client *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewTokenSource builds a token source for one or more OAuth scopes, e.g.
// "https://www.googleapis.com/auth/firebase.database",
// "https://www.googleapis.com/auth/userinfo.email".
func NewTokenSource(scopes ...string) *TokenSource {
	return &TokenSource{
		// The metadata server expects comma-separated scopes; a space
		// separator makes a multi-scope request fail with HTTP 500.
		scopes: strings.Join(scopes, ","),
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Token returns a valid access token, refreshing from the metadata server
// when the cached one is missing or about to expire.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expires) {
		return t.token, nil
	}

	u := metadataTokenURL + "?scopes=" + url.QueryEscape(t.scopes)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("metadata token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata token: status %s", resp.Status)
	}

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	t.token = body.AccessToken
	// Refresh 60s before actual expiry.
	t.expires = time.Now().Add(time.Duration(body.ExpiresIn-60) * time.Second)
	return t.token, nil
}
