// Package users keeps a lightweight directory of everyone who has signed in,
// so admins can see the app's user base. Each user is touched on sign-in
// (when their subscription is loaded):
//
//	users/{uid}  ->  { email, firstSeen, lastSeen }
package users

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type User struct {
	UID       string `json:"uid"`
	Email     string `json:"email"`
	FirstSeen int64  `json:"firstSeen"`
	LastSeen  int64  `json:"lastSeen"`
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
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/users", s.projectID)
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

// Touch records (or refreshes) a user's directory entry on sign-in.
func (s *Store) Touch(ctx context.Context, uid, email string) {
	if uid == "" {
		return
	}
	now := time.Now().Unix()
	first := now
	// Preserve the original firstSeen if the user already exists.
	if g, _ := s.get(ctx, uid); g.FirstSeen > 0 {
		first = g.FirstSeen
	}
	body := []byte(fmt.Sprintf(`{"fields":{"email":{"stringValue":%q},"firstSeen":{"integerValue":"%d"},"lastSeen":{"integerValue":"%d"}}}`,
		email, first, now))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.col()+"/"+uid, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if err := s.authed(ctx, req); err != nil {
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func (s *Store) get(ctx context.Context, uid string) (User, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"/"+uid, nil)
	if err := s.authed(ctx, req); err != nil {
		return User{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return User{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return User{}, nil
	}
	var doc struct {
		Fields json.RawMessage `json:"fields"`
	}
	json.NewDecoder(resp.Body).Decode(&doc)
	u := parse(doc.Fields)
	u.UID = uid
	return u, nil
}

// List returns all known users, newest sign-in first.
func (s *Store) List(ctx context.Context) ([]User, error) {
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
		return nil, fmt.Errorf("users list: %s", resp.Status)
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
	list := make([]User, 0, len(out.Documents))
	for _, d := range out.Documents {
		u := parse(d.Fields)
		for i := len(d.Name) - 1; i >= 0; i-- {
			if d.Name[i] == '/' {
				u.UID = d.Name[i+1:]
				break
			}
		}
		list = append(list, u)
	}
	return list, nil
}

func parse(raw json.RawMessage) User {
	var f struct {
		Email struct {
			StringValue string `json:"stringValue"`
		} `json:"email"`
		FirstSeen struct {
			IntegerValue string `json:"integerValue"`
		} `json:"firstSeen"`
		LastSeen struct {
			IntegerValue string `json:"integerValue"`
		} `json:"lastSeen"`
	}
	_ = json.Unmarshal(raw, &f)
	atoi := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	return User{Email: f.Email.StringValue, FirstSeen: atoi(f.FirstSeen.IntegerValue), LastSeen: atoi(f.LastSeen.IntegerValue)}
}
