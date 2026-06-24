// Package geofence stores a per-vehicle safe zone. The backend watches each
// vehicle's location against its zone and raises an alert when the car leaves
// (handled by the caller's ticker).
//
//	geofences/{vehicleId} -> { enabled, lat, lng, radiusM }
package geofence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Zone is a circular safe area.
type Zone struct {
	Enabled bool    `json:"enabled"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	RadiusM float64 `json:"radiusM"`
}

// Contains reports whether (lat,lng) is inside the zone, using the haversine
// distance in metres.
func (z Zone) Contains(lat, lng float64) bool {
	const R = 6371000.0
	rad := math.Pi / 180
	dLat := (lat - z.Lat) * rad
	dLng := (lng - z.Lng) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(z.Lat*rad)*math.Cos(lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	d := 2 * R * math.Asin(math.Sqrt(a))
	return d <= z.RadiusM
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
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/geofences", s.projectID)
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

func (s *Store) Get(ctx context.Context, vehicleID string) (Zone, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"/"+vehicleID, nil)
	if err := s.authed(ctx, req); err != nil {
		return Zone{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Zone{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Zone{RadiusM: 300}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Zone{}, fmt.Errorf("geofence get: %s", resp.Status)
	}
	var doc struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return Zone{}, err
	}
	return parseFields(doc.Fields), nil
}

func (s *Store) Put(ctx context.Context, vehicleID string, z Zone) error {
	body := map[string]any{"fields": map[string]any{
		"enabled": map[string]any{"booleanValue": z.Enabled},
		"lat":     map[string]any{"doubleValue": z.Lat},
		"lng":     map[string]any{"doubleValue": z.Lng},
		"radiusM": map[string]any{"doubleValue": z.RadiusM},
	}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.col()+"/"+vehicleID, bytes.NewReader(b))
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
		return fmt.Errorf("geofence put: %s", resp.Status)
	}
	return nil
}

// All returns every stored zone keyed by vehicleId (for the watcher).
func (s *Store) All(ctx context.Context) (map[string]Zone, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.col()+"?pageSize=300", nil)
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
		return nil, fmt.Errorf("geofence all: %s", resp.Status)
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
	res := make(map[string]Zone, len(out.Documents))
	for _, d := range out.Documents {
		id := d.Name
		if i := strings.LastIndex(d.Name, "/"); i >= 0 {
			id = d.Name[i+1:]
		}
		res[id] = parseFields(d.Fields)
	}
	return res, nil
}

func parseFields(raw json.RawMessage) Zone {
	var f struct {
		Enabled struct {
			BooleanValue bool `json:"booleanValue"`
		} `json:"enabled"`
		Lat     numField `json:"lat"`
		Lng     numField `json:"lng"`
		RadiusM numField `json:"radiusM"`
	}
	_ = json.Unmarshal(raw, &f)
	return Zone{
		Enabled: f.Enabled.BooleanValue,
		Lat:     f.Lat.val(),
		Lng:     f.Lng.val(),
		RadiusM: f.RadiusM.val(),
	}
}

// numField reads a Firestore number whether stored as double or integer.
type numField struct {
	DoubleValue  float64 `json:"doubleValue"`
	IntegerValue string  `json:"integerValue"`
}

func (n numField) val() float64 {
	if n.IntegerValue != "" {
		v, _ := strconv.ParseFloat(n.IntegerValue, 64)
		return v
	}
	return n.DoubleValue
}
