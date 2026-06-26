package payments

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

func testService() *Service {
	return NewService(Config{
		PaymeMerchantID: "merchant123",
		PaymeKey:        "secretkey",
		ClickServiceID:  "111",
		ClickMerchantID: "222",
		ClickSecretKey:  "clicksecret",
	}, nil, nil)
}

func TestPayURLPayme(t *testing.T) {
	s := testService()
	o := Order{ID: "abc123", Amount: 19000, Provider: "payme"}
	got := s.payURL(o)
	if !strings.HasPrefix(got, "https://checkout.paycom.uz/") {
		t.Fatalf("unexpected payme url: %s", got)
	}
	// 19000 so'm -> 1,900,000 tiyin must appear in the decoded payload.
	if !strings.Contains(decodeTail(t, got), "a=1900000") {
		t.Errorf("payme amount not in tiyin: %s", decodeTail(t, got))
	}
	if !strings.Contains(decodeTail(t, got), "ac.order_id=abc123") {
		t.Errorf("order id missing from payload")
	}
}

func TestPayURLClick(t *testing.T) {
	s := testService()
	o := Order{ID: "ord9", Amount: 35000, Provider: "click"}
	got := s.payURL(o)
	if !strings.Contains(got, "service_id=111") || !strings.Contains(got, "transaction_param=ord9") || !strings.Contains(got, "amount=35000") {
		t.Errorf("click url missing params: %s", got)
	}
}

func TestClickSignOK(t *testing.T) {
	s := testService()
	f := url.Values{}
	f.Set("click_trans_id", "5555")
	f.Set("service_id", "111")
	f.Set("merchant_trans_id", "ord9")
	f.Set("amount", "35000")
	f.Set("action", "0")
	f.Set("sign_time", "2026-06-26 10:00:00")
	// Correct signature for the prepare action.
	raw := "5555" + "111" + "clicksecret" + "ord9" + "35000" + "0" + "2026-06-26 10:00:00"
	sum := md5.Sum([]byte(raw))
	f.Set("sign_string", hex.EncodeToString(sum[:]))
	if !s.clickSignOK(f, "0") {
		t.Error("valid prepare signature rejected")
	}
	// Tamper with the amount -> must fail.
	f.Set("sign_string", "deadbeef")
	if s.clickSignOK(f, "0") {
		t.Error("invalid signature accepted")
	}
}

func TestTierPrice(t *testing.T) {
	for _, tier := range []string{"1m", "2m", "1y"} {
		if TierPriceUZS[tier] <= 0 {
			t.Errorf("tier %s has no price", tier)
		}
	}
}

func TestConstEq(t *testing.T) {
	if !constEq("abc", "abc") {
		t.Error("equal strings reported unequal")
	}
	if constEq("abc", "abd") || constEq("abc", "abcd") {
		t.Error("unequal strings reported equal")
	}
}

func decodeTail(t *testing.T, u string) string {
	t.Helper()
	tail := strings.TrimPrefix(u, "https://checkout.paycom.uz/")
	b, err := base64.StdEncoding.DecodeString(tail)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return string(b)
}
