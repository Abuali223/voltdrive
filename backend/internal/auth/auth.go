// Package auth handles user identity and per-vehicle permissions (RBAC).
//
// In production:
//   - Identity comes from a Firebase Auth ID token (verified with the
//     Firebase Admin SDK / public certs).
//   - Permissions live in Firestore: who may control which vehicle and
//     which actions they may perform (owner vs shared/family user).
//
// This file defines the model and a verifier interface so the API layer
// is already written against the real shape. A static in-memory verifier
// is provided for local development.
package auth

import (
	"context"
	"errors"
	"strings"
)

// Role determines what a user may do with a vehicle.
type Role string

const (
	RoleOwner  Role = "owner"  // full control + manage other users
	RoleDriver Role = "driver" // full control, no user management
	RoleGuest  Role = "guest"  // limited: lock/unlock + view only
)

// Action is a controllable capability, checked against a role.
type Action string

const (
	ActView    Action = "view"
	ActLock    Action = "lock"
	ActStart   Action = "start"
	ActClimate Action = "climate"
	ActManage  Action = "manage" // add/remove users, change permissions
)

// permissions defines which actions each role is allowed to perform.
var permissions = map[Role]map[Action]bool{
	RoleOwner:  {ActView: true, ActLock: true, ActStart: true, ActClimate: true, ActManage: true},
	RoleDriver: {ActView: true, ActLock: true, ActStart: true, ActClimate: true},
	RoleGuest:  {ActView: true, ActLock: true},
}

// Can reports whether a role may perform an action.
func (r Role) Can(a Action) bool { return permissions[r][a] }

// User is the verified identity extracted from a token.
type User struct {
	UID   string
	Email string
}

// ErrUnauthenticated / ErrForbidden are returned by the API layer.
var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
)

// Verifier validates a bearer token and returns the user.
type Verifier interface {
	Verify(ctx context.Context, token string) (User, error)
}

// Permissions resolves a user's role for a given vehicle.
type Permissions interface {
	RoleFor(ctx context.Context, uid, vehicleID string) (Role, bool)
}

// ---- Local development implementations (no Firebase needed) ----

// DevVerifier accepts any non-empty token and treats it as "uid:email".
// Example header: Authorization: Bearer u-ali:ali@example.com
type DevVerifier struct{}

func (DevVerifier) Verify(_ context.Context, token string) (User, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return User{}, ErrUnauthenticated
	}
	parts := strings.SplitN(token, ":", 2)
	u := User{UID: parts[0]}
	if len(parts) == 2 {
		u.Email = parts[1]
	}
	return u, nil
}

// DevPermissions grants every user the owner role (local dev only).
type DevPermissions struct{}

func (DevPermissions) RoleFor(_ context.Context, _, _ string) (Role, bool) {
	return RoleOwner, true
}
