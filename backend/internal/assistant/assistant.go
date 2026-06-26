// Package assistant powers VoltDrive's in-app AI assistant. It calls a Gemini
// model on Vertex AI and returns a structured reply: a natural-language answer
// plus an optional car action the app should perform (lock, unlock, start…).
//
// Auth uses the Cloud Run service account token (cloud-platform scope) — no API
// key. The model returns strict JSON via a response schema, so parsing is safe.
package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client talks to a Gemini model on Vertex AI.
type Client struct {
	project  string
	location string
	model    string
	token    func(ctx context.Context) (string, error)
	http     *http.Client
}

// NewClient builds a client. location/model fall back to sensible defaults.
func NewClient(project, location, model string, token func(context.Context) (string, error)) *Client {
	if location == "" {
		location = "us-central1"
	}
	if model == "" {
		model = "gemini-2.5-flash-lite" // fast + lighter; retried on 429
	}
	return &Client{project: project, location: location, model: model, token: token,
		http: &http.Client{Timeout: 30 * time.Second}}
}

// Turn is one prior message in the conversation.
type Turn struct {
	Role string `json:"role"` // "user" or "model"
	Text string `json:"text"`
}

// Reply is the structured assistant output returned to the app.
type Reply struct {
	Text   string         `json:"reply"`
	Action string         `json:"action"` // none|lock|unlock|start|stop|climate|locate|status
	Params map[string]any `json:"params,omitempty"`
}

const systemTmpl = `You are VoltDrive's friendly in-app AI assistant. ` +
	`Chat naturally and help with anything — general questions, small talk, advice — ` +
	`and also control the user's connected car. ` +
	`Reply in the SAME language as the user (usually Uzbek or Russian). ` +
	`Keep replies SHORT: 1-2 sentences, conversational — they are read aloud. ` +
	`You may trigger these actions: lock, unlock, start (engine), stop (engine), ` +
	`climate (put the Celsius value in params.temp), locate (find/show the car), status. ` +
	`If the user asks for one, set "action" and confirm briefly in "reply". ` +
	`For questions about battery, range, lock state or location, set action "none" and answer ONLY ` +
	`from the car context — never invent car values. ` +
	`For anything else, action "none" and just chat. ` +
	`Current car context (JSON): %s`

// Ask sends the user's message (plus car context and recent history) to Gemini
// and returns the structured reply.
func (c *Client) Ask(ctx context.Context, userMsg, carJSON string, history []Turn) (Reply, error) {
	contents := make([]map[string]any, 0, len(history)+1)
	for _, t := range history {
		role := t.Role
		if role != "model" {
			role = "user"
		}
		contents = append(contents, map[string]any{"role": role, "parts": []map[string]any{{"text": t.Text}}})
	}
	contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{"text": userMsg}}})

	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]any{{"text": fmt.Sprintf(systemTmpl, carJSON)}}},
		"contents":          contents,
		"generationConfig": map[string]any{
			"temperature":      0.5,
			"maxOutputTokens":  300,
			"thinkingConfig":   map[string]any{"thinkingBudget": 0}, // no thinking → faster replies
			"responseMimeType": "application/json",
			"responseSchema": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"reply":  map[string]any{"type": "STRING"},
					"action": map[string]any{"type": "STRING", "enum": []string{"none", "lock", "unlock", "start", "stop", "climate", "locate", "status"}},
					"params": map[string]any{"type": "OBJECT", "properties": map[string]any{"temp": map[string]any{"type": "NUMBER"}}},
				},
				"required": []string{"reply", "action"},
			},
		},
	}
	raw, err := c.call(ctx, body)
	if err != nil {
		return Reply{}, err
	}
	var r Reply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Reply{Text: raw, Action: "none"}, nil // tolerate non-JSON, show as plain text
	}
	if r.Action == "" {
		r.Action = "none"
	}
	return r, nil
}

// call POSTs the request body to the model and returns the candidate text,
// retrying briefly on Vertex's bursty 429/503 (dynamic shared quota).
func (c *Client) call(ctx context.Context, body map[string]any) (string, error) {
	j, _ := json.Marshal(body)
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		c.location, c.project, c.location, c.model)
	tok, err := c.token(ctx)
	if err != nil {
		return "", err
	}
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(j))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err = c.http.Do(req)
		if err != nil {
			return "", err
		}
		if (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable) || attempt >= 3 {
			break
		}
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(300*(attempt+1)) * time.Millisecond):
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return "", fmt.Errorf("gemini %s: %s", resp.Status, b.String())
	}
	var gr struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return "", err
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return "", nil
	}
	return gr.Candidates[0].Content.Parts[0].Text, nil
}

// TripPlan is a structured EV journey plan with charging stops.
type TripPlan struct {
	Feasible   bool         `json:"feasible"`   // reachable on current charge (with stops)
	Summary    string       `json:"summary"`    // one-line overview
	DistanceKm int          `json:"distanceKm"` // approx total distance
	ArrivalSoc int          `json:"arrivalSoc"` // estimated battery % on arrival
	Stops      []ChargeStop `json:"stops"`      // charging stops along the way (may be empty)
	Tips       []string     `json:"tips,omitempty"`
}

// ChargeStop is one recommended charging stop.
type ChargeStop struct {
	Name string `json:"name"` // place / city to charge at
	AtKm int    `json:"atKm"` // approx distance from start
	Note string `json:"note,omitempty"`
}

const tripTmpl = `You are VoltDrive's EV trip planner for Uzbekistan. Plan a drive from the car's ` +
	`current location to the user's destination on the current battery charge. ` +
	`Reply in %s (default Uzbek). Use realistic Uzbekistan geography, distances and charging locations ` +
	`(Toshkent, Samarqand, Buxoro, Andijon, etc. and major highways). ` +
	`Estimate total distance, whether the destination is reachable, and the battery %% on arrival. ` +
	`If the remaining range is not enough, add charging stops (real cities/areas on the route) so the trip is feasible — ` +
	`assume the driver recharges to ~80%% at each stop. Add 1-3 short practical tips. ` +
	`Be concise and realistic; never invent fake station brands. ` +
	`Car charge context (JSON): %s`

// PlanTrip plans an EV journey to dest given the car's battery context.
func (c *Client) PlanTrip(ctx context.Context, dest, carJSON, lang string) (TripPlan, error) {
	if lang == "" {
		lang = "Uzbek"
	}
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]any{{"text": fmt.Sprintf(tripTmpl, lang, carJSON)}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "Manzil: " + dest}}}},
		"generationConfig": map[string]any{
			"temperature":      0.4,
			"maxOutputTokens":  700,
			"thinkingConfig":   map[string]any{"thinkingBudget": 0},
			"responseMimeType": "application/json",
			"responseSchema": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"feasible":   map[string]any{"type": "BOOLEAN"},
					"summary":    map[string]any{"type": "STRING"},
					"distanceKm": map[string]any{"type": "INTEGER"},
					"arrivalSoc": map[string]any{"type": "INTEGER"},
					"stops": map[string]any{"type": "ARRAY", "items": map[string]any{
						"type": "OBJECT",
						"properties": map[string]any{
							"name": map[string]any{"type": "STRING"},
							"atKm": map[string]any{"type": "INTEGER"},
							"note": map[string]any{"type": "STRING"},
						},
						"required": []string{"name"},
					}},
					"tips": map[string]any{"type": "ARRAY", "items": map[string]any{"type": "STRING"}},
				},
				"required": []string{"feasible", "summary"},
			},
		},
	}
	raw, err := c.call(ctx, body)
	if err != nil {
		return TripPlan{}, err
	}
	var p TripPlan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return TripPlan{Summary: raw}, nil
	}
	return p, nil
}

// DiagReport is a structured car-health assessment.
type DiagReport struct {
	Status  string  `json:"status"` // ok | warning | critical
	Summary string  `json:"summary"`
	Issues  []Issue `json:"issues"`
}

// Issue is a single detected problem or thing to watch.
type Issue struct {
	Severity string `json:"severity"` // info | warning | critical
	Title    string `json:"title"`
	Advice   string `json:"advice,omitempty"`
}

const diagTmpl = `You are VoltDrive's car diagnostics AI. Analyze the car's current telemetry and report its health. ` +
	`Reply in %s (default Uzbek). Be concise and practical. ` +
	`Flag any problems or things to watch: low or fast-draining battery, plugged in but not charging, ` +
	`doors left unlocked, climate left on, abnormal cabin temperature, and (when present) any fault codes. ` +
	`status: "ok" (all good), "warning" (minor issues), or "critical" (urgent). ` +
	`Each issue: a short title and one line of practical advice. ` +
	`If everything looks fine, status "ok", empty issues, and a short reassuring summary. ` +
	`Use ONLY the telemetry below — never invent faults. Telemetry (JSON): %s`

// Diagnose analyzes the car telemetry and returns a structured health report.
func (c *Client) Diagnose(ctx context.Context, carJSON, lang string) (DiagReport, error) {
	if lang == "" {
		lang = "Uzbek"
	}
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]any{{"text": fmt.Sprintf(diagTmpl, lang, carJSON)}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "Mashinani tekshir."}}}},
		"generationConfig": map[string]any{
			"temperature":      0.3,
			"maxOutputTokens":  600,
			"thinkingConfig":   map[string]any{"thinkingBudget": 0},
			"responseMimeType": "application/json",
			"responseSchema": map[string]any{
				"type": "OBJECT",
				"properties": map[string]any{
					"status":  map[string]any{"type": "STRING", "enum": []string{"ok", "warning", "critical"}},
					"summary": map[string]any{"type": "STRING"},
					"issues": map[string]any{"type": "ARRAY", "items": map[string]any{
						"type": "OBJECT",
						"properties": map[string]any{
							"severity": map[string]any{"type": "STRING", "enum": []string{"info", "warning", "critical"}},
							"title":    map[string]any{"type": "STRING"},
							"advice":   map[string]any{"type": "STRING"},
						},
						"required": []string{"severity", "title"},
					}},
				},
				"required": []string{"status", "summary"},
			},
		},
	}
	raw, err := c.call(ctx, body)
	if err != nil {
		return DiagReport{}, err
	}
	var d DiagReport
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return DiagReport{Status: "ok", Summary: raw}, nil
	}
	if d.Status == "" {
		d.Status = "ok"
	}
	return d, nil
}
