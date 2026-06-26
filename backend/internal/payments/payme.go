package payments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Payme Merchant API error codes (subset we use).
const (
	pmErrTransport     = -32300
	pmErrAuth          = -32504
	pmErrMethod        = -32601
	pmErrAccount       = -31050 // order not found / invalid account
	pmErrAmount        = -31001
	pmErrCannotPerform = -31008
	pmErrTxNotFound    = -31003
	pmErrCannotCancel  = -31007
)

// Payme transaction states.
const (
	pmStateCreated           = 1
	pmStateDone              = 2
	pmStateCanceled          = -1
	pmStateCanceledAfterDone = -2
)

type pmRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	ID     json.RawMessage `json:"id"`
}

func pmError(code int, msg string) map[string]any {
	return map[string]any{"code": code, "message": map[string]any{"ru": msg, "uz": msg, "en": msg}}
}

// HandlePayme implements the Payme Merchant API JSON-RPC endpoint. Payme
// authenticates with HTTP Basic where the password is the merchant key.
func (s *Service) HandlePayme(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	write := func(id json.RawMessage, result any, errObj map[string]any) {
		resp := map[string]any{"jsonrpc": "2.0", "id": id}
		if errObj != nil {
			resp["error"] = errObj
		} else {
			resp["result"] = result
		}
		_ = json.NewEncoder(w).Encode(resp)
	}

	if !s.paymeAuthOK(r) {
		write(nil, nil, pmError(pmErrAuth, "Insufficient privileges"))
		return
	}
	var req pmRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		write(nil, nil, pmError(pmErrTransport, "Parse error"))
		return
	}
	ctx := r.Context()
	switch req.Method {
	case "CheckPerformTransaction":
		s.pmCheckPerform(ctx, req, write)
	case "CreateTransaction":
		s.pmCreate(ctx, req, write)
	case "PerformTransaction":
		s.pmPerform(ctx, req, write)
	case "CancelTransaction":
		s.pmCancel(ctx, req, write)
	case "CheckTransaction":
		s.pmCheck(ctx, req, write)
	case "GetStatement":
		write(req.ID, map[string]any{"transactions": []any{}}, nil)
	default:
		write(req.ID, nil, pmError(pmErrMethod, "Method not found"))
	}
}

// paymeAuthOK validates the Basic auth password against the merchant key.
func (s *Service) paymeAuthOK(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Basic ") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
	if err != nil {
		return false
	}
	// Format is "Paycom:KEY"; we only check the key part.
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return false
	}
	return constEq(parts[1], s.cfg.PaymeKey)
}

// orderFromAccount loads the order referenced by params.account.order_id and
// validates that its amount matches the requested amount (in tiyin).
func (s *Service) orderFromAccount(ctx context.Context, params json.RawMessage) (Order, map[string]any) {
	var p struct {
		Amount  int64 `json:"amount"`
		Account struct {
			OrderID string `json:"order_id"`
		} `json:"account"`
	}
	_ = json.Unmarshal(params, &p)
	if p.Account.OrderID == "" {
		return Order{}, pmError(pmErrAccount, "Order not specified")
	}
	o, ok, err := s.orders.Get(ctx, p.Account.OrderID)
	if err != nil || !ok {
		return Order{}, pmError(pmErrAccount, "Order not found")
	}
	if p.Amount != 0 && p.Amount != amountTiyin(o) {
		return Order{}, pmError(pmErrAmount, "Incorrect amount")
	}
	return o, nil
}

func (s *Service) pmCheckPerform(ctx context.Context, req pmRequest, write func(json.RawMessage, any, map[string]any)) {
	_, errObj := s.orderFromAccount(ctx, req.Params)
	if errObj != nil {
		write(req.ID, nil, errObj)
		return
	}
	write(req.ID, map[string]any{"allow": true}, nil)
}

func (s *Service) pmCreate(ctx context.Context, req pmRequest, write func(json.RawMessage, any, map[string]any)) {
	var p struct {
		ID   string `json:"id"`
		Time int64  `json:"time"`
	}
	_ = json.Unmarshal(req.Params, &p)
	o, errObj := s.orderFromAccount(ctx, req.Params)
	if errObj != nil {
		write(req.ID, nil, errObj)
		return
	}
	// If a different transaction already owns this order, refuse.
	if o.TxID != "" && o.TxID != p.ID {
		write(req.ID, nil, pmError(pmErrCannotPerform, "Order is busy"))
		return
	}
	if o.Status == StatusPaid {
		write(req.ID, nil, pmError(pmErrCannotPerform, "Order already paid"))
		return
	}
	o.TxID = p.ID
	o.Status = StatusHeld
	if o.CreatedAt == 0 {
		o.CreatedAt = time.Now().Unix()
	}
	if err := s.orders.Put(ctx, o); err != nil {
		write(req.ID, nil, pmError(pmErrTransport, "Storage error"))
		return
	}
	write(req.ID, map[string]any{
		"create_time": nowMillis(),
		"transaction": o.ID,
		"state":       pmStateCreated,
	}, nil)
}

func (s *Service) pmPerform(ctx context.Context, req pmRequest, write func(json.RawMessage, any, map[string]any)) {
	o, errObj := s.txByPaymeID(ctx, req.Params)
	if errObj != nil {
		write(req.ID, nil, errObj)
		return
	}
	if o.Status != StatusPaid {
		if err := s.markPaidAndActivate(ctx, &o); err != nil {
			write(req.ID, nil, pmError(pmErrTransport, "Activation error"))
			return
		}
	}
	write(req.ID, map[string]any{
		"transaction":  o.ID,
		"perform_time": o.PaidAt * 1000,
		"state":        pmStateDone,
	}, nil)
}

func (s *Service) pmCancel(ctx context.Context, req pmRequest, write func(json.RawMessage, any, map[string]any)) {
	o, errObj := s.txByPaymeID(ctx, req.Params)
	if errObj != nil {
		write(req.ID, nil, errObj)
		return
	}
	state := pmStateCanceled
	if o.Status == StatusPaid {
		state = pmStateCanceledAfterDone
	}
	o.Status = StatusCanceled
	_ = s.orders.Put(ctx, o)
	write(req.ID, map[string]any{
		"transaction": o.ID,
		"cancel_time": nowMillis(),
		"state":       state,
	}, nil)
}

func (s *Service) pmCheck(ctx context.Context, req pmRequest, write func(json.RawMessage, any, map[string]any)) {
	o, errObj := s.txByPaymeID(ctx, req.Params)
	if errObj != nil {
		write(req.ID, nil, errObj)
		return
	}
	state := pmStateCreated
	var performTime int64
	switch o.Status {
	case StatusPaid:
		state = pmStateDone
		performTime = o.PaidAt * 1000
	case StatusCanceled:
		state = pmStateCanceled
	}
	write(req.ID, map[string]any{
		"create_time":  o.CreatedAt * 1000,
		"perform_time": performTime,
		"cancel_time":  0,
		"transaction":  o.ID,
		"state":        state,
		"reason":       nil,
	}, nil)
}

// txByPaymeID loads the order owning the Payme transaction id in params.id.
// Payme sends its OWN transaction id here (which we stored on the order's TxID
// in CreateTransaction), so we resolve it with a field query.
func (s *Service) txByPaymeID(ctx context.Context, params json.RawMessage) (Order, map[string]any) {
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(params, &p)
	o, ok, err := s.orders.GetByTxID(ctx, p.ID)
	if err == nil && ok {
		return o, nil
	}
	return Order{}, pmError(pmErrTxNotFound, "Transaction not found")
}

func nowMillis() int64 { return time.Now().UnixNano() / int64(time.Millisecond) }
