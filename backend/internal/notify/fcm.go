// Package notify sends push notifications via Firebase Cloud Messaging (FCM
// HTTP v1). Used for security/alarm alerts: door forced, vehicle moved while
// locked, low battery, etc.
//
// Auth uses a Google OAuth access token (scope
// https://www.googleapis.com/auth/firebase.messaging) from the Cloud Run
// service account, supplied by TokenSource.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// FCM is a Firebase Cloud Messaging client.
type FCM struct {
	ProjectID   string
	TokenSource func(ctx context.Context) (string, error)
	client      *http.Client
}

// NewFCM builds an FCM client for the given Firebase project.
func NewFCM(projectID string, ts func(context.Context) (string, error)) *FCM {
	return &FCM{ProjectID: projectID, TokenSource: ts, client: &http.Client{Timeout: 10 * time.Second}}
}

// Alert describes a security/status notification for one device.
type Alert struct {
	DeviceToken string            // FCM registration token of the user's phone
	Title       string            // e.g. "Xavfsizlik ogohlantirishi"
	Body        string            // e.g. "Voyah Free — eshik majburan ochildi"
	Data        map[string]string // structured payload (vehicleId, type, ...)
}

// Send delivers a single alert. Returns the FCM message name on success.
func (f *FCM) Send(ctx context.Context, a Alert) (string, error) {
	msg := map[string]any{
		"message": map[string]any{
			"token": a.DeviceToken,
			"notification": map[string]string{
				"title": a.Title,
				"body":  a.Body,
			},
			"data": a.Data,
			"android": map[string]any{
				"priority": "high",
			},
			"apns": map[string]any{
				"headers": map[string]string{"apns-priority": "10"},
			},
		},
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", f.ProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	token, err := f.TokenSource(ctx)
	if err != nil {
		return "", fmt.Errorf("fcm: token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return "", fmt.Errorf("fcm: send failed (%d): %s", resp.StatusCode, e.Error.Message)
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Name, nil
}
