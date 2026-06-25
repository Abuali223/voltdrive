// Package fleet stores a per-operator fleet (a named set of vehicle IDs plus a
// slot allowance) in Firestore, powering the Fleet dashboard.
//
//	fleets/{uid}  ->  { name, slots, vehicleIds[] }
package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type Fleet struct {
	Name       string   `json:"name"`
	Slots      int      `json:"slots"`
	VehicleIDs []string `json:"vehicleIds"`
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
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/fleets", s.projectID)
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

func (s *Store) Get(ctx context.Context, uid string) (Fleet, bool, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"/"+uid, nil)
	if err := s.authed(ctx, req); err != nil {
		return Fleet{}, false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Fleet{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Fleet{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Fleet{}, false, fmt.Errorf("fleet get: %s", resp.Status)
	}
	var doc struct {
		Fields struct {
			Name struct {
				StringValue string `json:"stringValue"`
			} `json:"name"`
			Slots struct {
				IntegerValue string `json:"integerValue"`
			} `json:"slots"`
			IDs struct {
				ArrayValue struct {
					Values []struct {
						StringValue string `json:"stringValue"`
					} `json:"values"`
				} `json:"arrayValue"`
			} `json:"vehicleIds"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return Fleet{}, false, err
	}
	f := Fleet{Name: doc.Fields.Name.StringValue}
	f.Slots, _ = strconv.Atoi(doc.Fields.Slots.IntegerValue)
	for _, v := range doc.Fields.IDs.ArrayValue.Values {
		if v.StringValue != "" {
			f.VehicleIDs = append(f.VehicleIDs, v.StringValue)
		}
	}
	return f, true, nil
}

func (s *Store) Put(ctx context.Context, uid string, f Fleet) error {
	vals := make([]map[string]any, 0, len(f.VehicleIDs))
	for _, id := range f.VehicleIDs {
		vals = append(vals, map[string]any{"stringValue": id})
	}
	body := map[string]any{"fields": map[string]any{
		"name":       map[string]any{"stringValue": f.Name},
		"slots":      map[string]any{"integerValue": strconv.Itoa(f.Slots)},
		"vehicleIds": map[string]any{"arrayValue": map[string]any{"values": vals}},
	}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.col()+"/"+uid, bytes.NewReader(b))
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
		return fmt.Errorf("fleet put: %s", resp.Status)
	}
	return nil
}
