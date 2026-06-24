package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// FirestorePermissions resolves a user's role for a vehicle from Firestore.
//
// Data model (one document per grant):
//   collection: vehicle_access
//   doc id:     "{vehicleId}_{uid}"
//   fields:     { vehicleId: string, uid: string, role: "owner|driver|guest" }
//
// This lets an owner share a car with family members at different roles —
// satisfying the "multiple users with permissions" requirement.
type FirestorePermissions struct {
	ProjectID string
	// TokenSource returns a Google OAuth access token with the
	// datastore/firestore scope (from the Cloud Run service account).
	TokenSource func(ctx context.Context) (string, error)
	client      *http.Client

	// cacheTTL bounds how long a resolved grant is trusted before re-reading
	// from Firestore. Short by design: a revoked grant stops working within
	// one TTL, while bursts of commands avoid a Firestore read each.
	cacheTTL time.Duration
	mu       sync.Mutex
	cache    map[string]cachedRole
}

type cachedRole struct {
	role Role
	ok   bool
	exp  time.Time
}

// NewFirestorePermissions builds a resolver for the given project.
func NewFirestorePermissions(projectID string, ts func(context.Context) (string, error)) *FirestorePermissions {
	return &FirestorePermissions{
		ProjectID:   projectID,
		TokenSource: ts,
		client:      &http.Client{Timeout: 8 * time.Second},
		cacheTTL:    30 * time.Second,
		cache:       make(map[string]cachedRole),
	}
}

// RoleFor implements the Permissions interface.
func (f *FirestorePermissions) RoleFor(ctx context.Context, uid, vehicleID string) (Role, bool) {
	docID := fmt.Sprintf("%s_%s", vehicleID, uid)

	// Serve from cache when fresh.
	if f.cacheTTL > 0 {
		f.mu.Lock()
		if c, found := f.cache[docID]; found && time.Now().Before(c.exp) {
			f.mu.Unlock()
			return c.role, c.ok
		}
		f.mu.Unlock()
	}

	role, ok := f.fetchRole(ctx, docID)

	if f.cacheTTL > 0 {
		f.mu.Lock()
		f.cache[docID] = cachedRole{role: role, ok: ok, exp: time.Now().Add(f.cacheTTL)}
		f.mu.Unlock()
	}
	return role, ok
}

// fetchRole reads a single grant document from the Firestore REST API.
func (f *FirestorePermissions) fetchRole(ctx context.Context, docID string) (Role, bool) {
	endpoint := fmt.Sprintf(
		"https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/vehicle_access/%s",
		url.PathEscape(f.ProjectID), url.PathEscape(docID),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false
	}
	if f.TokenSource != nil {
		token, err := f.TokenSource(ctx)
		if err != nil {
			return "", false
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false // 404 -> no access grant for this user/vehicle
	}

	// Firestore REST wraps values: { fields: { role: { stringValue: "owner" } } }
	var doc struct {
		Fields struct {
			Role struct {
				StringValue string `json:"stringValue"`
			} `json:"role"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", false
	}
	role := Role(doc.Fields.Role.StringValue)
	if _, ok := permissions[role]; !ok {
		return "", false
	}
	return role, true
}
