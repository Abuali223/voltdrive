package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FirebaseVerifier validates Firebase Auth ID tokens (RS256 JWTs) using
// Google's rotating public certificates. Pure standard library — no SDK.
//
// It checks the signature, issuer, audience (your Firebase project id) and
// expiry, exactly as the Firebase Admin SDK does.
type FirebaseVerifier struct {
	ProjectID string
	client    *http.Client

	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	expires time.Time
}

const googleCertsURL = "https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com"

// NewFirebaseVerifier creates a verifier for the given Firebase project id
// (e.g. "eldi-79bf9").
func NewFirebaseVerifier(projectID string) *FirebaseVerifier {
	return &FirebaseVerifier{
		ProjectID: projectID,
		client:    &http.Client{Timeout: 10 * time.Second},
		keys:      map[string]*rsa.PublicKey{},
	}
}

// Verify implements the Verifier interface.
func (f *FirebaseVerifier) Verify(ctx context.Context, token string) (User, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return User{}, ErrUnauthenticated
	}

	// Decode header to find the signing key id (kid).
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil || hdr.Alg != "RS256" {
		return User{}, ErrUnauthenticated
	}

	key, err := f.keyForKid(ctx, hdr.Kid)
	if err != nil {
		return User{}, ErrUnauthenticated
	}

	// Verify the RS256 signature over "header.payload".
	signed := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return User{}, ErrUnauthenticated
	}
	sum := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return User{}, ErrUnauthenticated
	}

	// Validate claims.
	var claims struct {
		Iss           string `json:"iss"`
		Aud           string `json:"aud"`
		Sub           string `json:"sub"`
		Exp           int64  `json:"exp"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return User{}, ErrUnauthenticated
	}
	wantIss := "https://securetoken.google.com/" + f.ProjectID
	if claims.Iss != wantIss || claims.Aud != f.ProjectID || claims.Sub == "" {
		return User{}, ErrUnauthenticated
	}
	if time.Now().Unix() >= claims.Exp {
		return User{}, ErrUnauthenticated
	}
	return User{UID: claims.Sub, Email: claims.Email, EmailVerified: claims.EmailVerified}, nil
}

func (f *FirebaseVerifier) keyForKid(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	f.mu.RLock()
	if time.Now().Before(f.expires) {
		if k, ok := f.keys[kid]; ok {
			f.mu.RUnlock()
			return k, nil
		}
	}
	f.mu.RUnlock()

	if err := f.refreshKeys(ctx); err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if k, ok := f.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

// refreshKeys fetches Google's current x509 certs and caches the parsed
// RSA public keys until the Cache-Control max-age expires.
func (f *FirebaseVerifier) refreshKeys(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, googleCertsURL, nil)
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var certs map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&certs); err != nil {
		return err
	}

	keys := map[string]*rsa.PublicKey{}
	for kid, certPEM := range certs {
		block, _ := pem.Decode([]byte(certPEM))
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			keys[kid] = pub
		}
	}

	ttl := parseMaxAge(resp.Header.Get("Cache-Control"))
	f.mu.Lock()
	f.keys = keys
	f.expires = time.Now().Add(ttl)
	f.mu.Unlock()
	return nil
}

// --- small JWT/crypto helpers (stdlib only) ---

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func parseMaxAge(cacheControl string) time.Duration {
	for _, part := range strings.Split(cacheControl, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "max-age=") {
			var secs int64
			fmt.Sscanf(part, "max-age=%d", &secs)
			if secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return time.Hour
}
