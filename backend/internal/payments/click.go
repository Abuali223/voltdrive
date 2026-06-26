package payments

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
)

// Click SHOP-API error codes (subset).
const (
	clOK          = 0
	clErrSign     = -1
	clErrAmount   = -2
	clErrAction   = -3
	clAlreadyPaid = -4
	clNotFound    = -5
	clTxCanceled  = -9
)

// Click actions.
const (
	clActionPrepare  = "0"
	clActionComplete = "1"
)

// HandleClick implements Click's Prepare (action=0) and Complete (action=1)
// callbacks. Requests are form-encoded and signed with an MD5 of the agreed
// field order plus the merchant secret key.
func (s *Service) HandleClick(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeClick(w, map[string]any{"error": clErrSign, "error_note": "bad request"})
		return
	}
	f := r.Form
	action := f.Get("action")
	orderID := f.Get("merchant_trans_id")

	if !s.clickSignOK(f, action) {
		writeClick(w, map[string]any{"error": clErrSign, "error_note": "SIGN CHECK FAILED"})
		return
	}

	o, ok, err := s.orders.Get(r.Context(), orderID)
	if err != nil || !ok {
		writeClick(w, map[string]any{"error": clNotFound, "error_note": "Order not found"})
		return
	}

	// Amount must match (Click sends so'm with optional decimals).
	if amt, _ := strconv.ParseFloat(f.Get("amount"), 64); int64(amt) != o.Amount {
		writeClick(w, map[string]any{"error": clErrAmount, "error_note": "Incorrect amount"})
		return
	}

	base := map[string]any{
		"click_trans_id":      f.Get("click_trans_id"),
		"merchant_trans_id":   orderID,
		"merchant_prepare_id": orderID,
	}

	switch action {
	case clActionPrepare:
		if o.Status == StatusPaid {
			writeClick(w, withErr(base, clAlreadyPaid, "Already paid"))
			return
		}
		if o.Status == StatusCanceled {
			writeClick(w, withErr(base, clTxCanceled, "Canceled"))
			return
		}
		o.TxID = f.Get("click_trans_id")
		o.Status = StatusHeld
		_ = s.orders.Put(r.Context(), o)
		writeClick(w, withErr(base, clOK, "Success"))
	case clActionComplete:
		if f.Get("error") != "" && f.Get("error") != "0" {
			o.Status = StatusCanceled
			_ = s.orders.Put(r.Context(), o)
			writeClick(w, withErr(base, clTxCanceled, "Canceled by Click"))
			return
		}
		if o.Status != StatusPaid {
			if err := s.markPaidAndActivate(r.Context(), &o); err != nil {
				writeClick(w, withErr(base, clErrAction, "Activation error"))
				return
			}
		}
		writeClick(w, withErr(base, clOK, "Success"))
	default:
		writeClick(w, withErr(base, clErrAction, "Unknown action"))
	}
}

func withErr(m map[string]any, code int, note string) map[string]any {
	m["error"] = code
	m["error_note"] = note
	return m
}

// clickSignOK verifies the MD5 signature for the given action.
//
//	prepare:  md5(click_trans_id + service_id + SECRET + merchant_trans_id + amount + action + sign_time)
//	complete: md5(click_trans_id + service_id + SECRET + merchant_trans_id + merchant_prepare_id + amount + action + sign_time)
func (s *Service) clickSignOK(f interface{ Get(string) string }, action string) bool {
	var raw string
	if action == clActionComplete {
		raw = f.Get("click_trans_id") + f.Get("service_id") + s.cfg.ClickSecretKey +
			f.Get("merchant_trans_id") + f.Get("merchant_prepare_id") + f.Get("amount") +
			f.Get("action") + f.Get("sign_time")
	} else {
		raw = f.Get("click_trans_id") + f.Get("service_id") + s.cfg.ClickSecretKey +
			f.Get("merchant_trans_id") + f.Get("amount") + f.Get("action") + f.Get("sign_time")
	}
	sum := md5.Sum([]byte(raw))
	return constEq(hex.EncodeToString(sum[:]), f.Get("sign_string"))
}

func writeClick(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
