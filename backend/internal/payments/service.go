package payments

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Activator activates a user's premium subscription for a tier once payment
// is confirmed. Implemented by the API layer over the subscription store.
type Activator func(ctx context.Context, uid, email, tier string) error

// Config holds gateway credentials, all sourced from the environment.
type Config struct {
	// Payme (Merchant API / Checkout).
	PaymeMerchantID string // m=... in the checkout payload
	PaymeKey        string // X-Auth key the webhook authenticates with

	// Click (Merchant API / SHOP-API).
	ClickServiceID  string
	ClickMerchantID string
	ClickSecretKey  string
}

// Enabled reports whether at least one gateway is configured.
func (c Config) Enabled() bool { return c.PaymeReady() || c.ClickReady() }

func (c Config) PaymeReady() bool { return c.PaymeMerchantID != "" && c.PaymeKey != "" }
func (c Config) ClickReady() bool {
	return c.ClickServiceID != "" && c.ClickMerchantID != "" && c.ClickSecretKey != ""
}

// Service builds checkout links and processes gateway webhooks.
type Service struct {
	cfg      Config
	orders   *OrderStore
	activate Activator
}

func NewService(cfg Config, orders *OrderStore, activate Activator) *Service {
	return &Service{cfg: cfg, orders: orders, activate: activate}
}

func (s *Service) Config() Config { return s.cfg }

// Checkout creates a pending order for a tier and returns its id plus the
// provider's hosted-payment URL. amount is taken from the price catalogue.
func (s *Service) Checkout(ctx context.Context, uid, email, tier, provider string) (Order, string, error) {
	amount, ok := TierPriceUZS[tier]
	if !ok {
		return Order{}, "", fmt.Errorf("unknown tier %q", tier)
	}
	o := Order{
		ID:        NewID(),
		UID:       uid,
		Email:     email,
		Tier:      tier,
		Amount:    amount,
		Provider:  provider,
		Status:    StatusPending,
		CreatedAt: time.Now().Unix(),
	}
	if err := s.orders.Put(ctx, o); err != nil {
		return Order{}, "", err
	}
	return o, s.payURL(o), nil
}

// payURL builds the hosted-checkout URL for an order.
func (s *Service) payURL(o Order) string {
	switch o.Provider {
	case "payme":
		// Payme checkout expects base64 of "m=MERCHANT;ac.order_id=ID;a=AMOUNT_IN_TIYIN".
		payload := fmt.Sprintf("m=%s;ac.order_id=%s;a=%d", s.cfg.PaymeMerchantID, o.ID, o.Amount*100)
		return "https://checkout.paycom.uz/" + base64.StdEncoding.EncodeToString([]byte(payload))
	case "click":
		v := url.Values{}
		v.Set("service_id", s.cfg.ClickServiceID)
		v.Set("merchant_id", s.cfg.ClickMerchantID)
		v.Set("amount", fmt.Sprintf("%d", o.Amount))
		v.Set("transaction_param", o.ID)
		return "https://my.click.uz/services/pay?" + v.Encode()
	}
	return ""
}

// markPaidAndActivate flips an order to paid and activates the subscription.
// Idempotent: a second call for an already-paid order is a no-op success.
func (s *Service) markPaidAndActivate(ctx context.Context, o *Order) error {
	if o.Status == StatusPaid {
		return nil
	}
	o.Status = StatusPaid
	o.PaidAt = time.Now().Unix()
	if err := s.orders.Put(ctx, *o); err != nil {
		return err
	}
	if s.activate != nil {
		return s.activate(ctx, o.UID, o.Email, o.Tier)
	}
	return nil
}

// amountTiyin returns the order amount in tiyin (Payme's unit).
func amountTiyin(o Order) int64 { return o.Amount * 100 }

// constEq compares two strings without short-circuiting (defends signature checks).
func constEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0 && !strings.ContainsRune(a, 0)
}
