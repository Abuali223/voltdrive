// Package payments wires Uzbek payment gateways (Payme and Click) into the
// freemium subscription. The flow is:
//
//  1. App calls POST /v1/pay/checkout {tier, provider} → we create a pending
//     Order in Firestore and return the provider's hosted-checkout URL.
//  2. The user pays on Payme/Click.
//  3. The gateway calls our server-to-server webhook (Payme JSON-RPC, Click
//     Prepare/Complete). We verify the signature, mark the order paid and
//     activate the user's premium subscription for the purchased tier.
//
// All gateway credentials come from the environment; with none set the whole
// feature is inert (routes are not registered), so the code is safe to ship
// before merchant onboarding is complete.
//
//	pay_orders/{orderID}  ->  { uid, email, tier, amount, provider, status, txid, createdAt, paidAt }
package payments

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Status values for an order's lifecycle.
const (
	StatusPending  = "pending"  // created, awaiting payment
	StatusHeld     = "held"     // gateway created a transaction (funds reserved)
	StatusPaid     = "paid"     // payment completed → subscription activated
	StatusCanceled = "canceled" // canceled/refunded
)

// Order is one purchase attempt for a subscription tier.
type Order struct {
	ID        string `json:"id"`
	UID       string `json:"uid"`
	Email     string `json:"email"`
	Tier      string `json:"tier"`     // 1m | 2m | 1y
	Amount    int64  `json:"amount"`   // in so'm (UZS), not tiyin
	Provider  string `json:"provider"` // payme | click
	Status    string `json:"status"`
	TxID      string `json:"txid"` // gateway transaction id
	CreatedAt int64  `json:"createdAt"`
	PaidAt    int64  `json:"paidAt"`
}

// TierPriceUZS is the catalogue price (in so'm) for each subscription tier.
var TierPriceUZS = map[string]int64{
	"1m": 19000,
	"2m": 35000,
	"1y": 180000,
}

// OrderStore persists orders in Firestore via the REST API.
type OrderStore struct {
	projectID string
	token     func(ctx context.Context) (string, error)
	client    *http.Client
}

func NewOrderStore(projectID string, token func(context.Context) (string, error)) *OrderStore {
	return &OrderStore{projectID: projectID, token: token, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *OrderStore) doc(id string) string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/pay_orders/%s", s.projectID, id)
}

func (s *OrderStore) authed(ctx context.Context, req *http.Request) error {
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

// NewID returns a random, URL-safe order id.
func NewID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Put writes (creates or replaces) an order.
func (s *OrderStore) Put(ctx context.Context, o Order) error {
	fields := map[string]any{
		"uid":       map[string]any{"stringValue": o.UID},
		"email":     map[string]any{"stringValue": o.Email},
		"tier":      map[string]any{"stringValue": o.Tier},
		"amount":    map[string]any{"integerValue": fmt.Sprintf("%d", o.Amount)},
		"provider":  map[string]any{"stringValue": o.Provider},
		"status":    map[string]any{"stringValue": o.Status},
		"txid":      map[string]any{"stringValue": o.TxID},
		"createdAt": map[string]any{"integerValue": fmt.Sprintf("%d", o.CreatedAt)},
		"paidAt":    map[string]any{"integerValue": fmt.Sprintf("%d", o.PaidAt)},
	}
	b, _ := json.Marshal(map[string]any{"fields": fields})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, s.doc(o.ID), bytes.NewReader(b))
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
		return fmt.Errorf("order put: %s", resp.Status)
	}
	return nil
}

// GetByTxID finds the order whose gateway transaction id matches txid, using a
// Firestore structured query. Returns (Order{}, false, nil) when none matches.
func (s *OrderStore) GetByTxID(ctx context.Context, txid string) (Order, bool, error) {
	if txid == "" {
		return Order{}, false, nil
	}
	body := map[string]any{
		"structuredQuery": map[string]any{
			"from":  []any{map[string]any{"collectionId": "pay_orders"}},
			"limit": 1,
			"where": map[string]any{
				"fieldFilter": map[string]any{
					"field": map[string]any{"fieldPath": "txid"},
					"op":    "EQUAL",
					"value": map[string]any{"stringValue": txid},
				},
			},
		},
	}
	b, _ := json.Marshal(body)
	url := fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents:runQuery", s.projectID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if err := s.authed(ctx, req); err != nil {
		return Order{}, false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Order{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Order{}, false, fmt.Errorf("order query: %s", resp.Status)
	}
	var rows []struct {
		Document struct {
			Name string `json:"name"`
		} `json:"document"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return Order{}, false, err
	}
	for _, row := range rows {
		if row.Document.Name == "" {
			continue
		}
		id := row.Document.Name
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		return s.Get(ctx, id)
	}
	return Order{}, false, nil
}

// Get loads an order by id; returns (Order{}, false, nil) if it does not exist.
func (s *OrderStore) Get(ctx context.Context, id string) (Order, bool, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.doc(id), nil)
	if err := s.authed(ctx, req); err != nil {
		return Order{}, false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Order{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Order{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Order{}, false, fmt.Errorf("order get: %s", resp.Status)
	}
	var d struct {
		Fields map[string]struct {
			StringValue  string `json:"stringValue"`
			IntegerValue string `json:"integerValue"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return Order{}, false, err
	}
	atoi := func(s string) int64 { var n int64; _, _ = fmt.Sscan(s, &n); return n }
	o := Order{
		ID:        id,
		UID:       d.Fields["uid"].StringValue,
		Email:     d.Fields["email"].StringValue,
		Tier:      d.Fields["tier"].StringValue,
		Amount:    atoi(d.Fields["amount"].IntegerValue),
		Provider:  d.Fields["provider"].StringValue,
		Status:    d.Fields["status"].StringValue,
		TxID:      d.Fields["txid"].StringValue,
		CreatedAt: atoi(d.Fields["createdAt"].IntegerValue),
		PaidAt:    atoi(d.Fields["paidAt"].IntegerValue),
	}
	return o, true, nil
}
