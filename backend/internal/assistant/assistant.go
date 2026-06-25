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
		model = "gemini-2.5-flash"
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

const systemTmpl = `You are VoltDrive's in-app AI assistant for a connected car. ` +
	`Help the driver control their car and answer questions about it. ` +
	`Reply in the SAME language as the user's message (usually Uzbek or Russian). ` +
	`Keep replies short, warm and natural. ` +
	`You may trigger exactly these actions: lock, unlock, start (engine), stop (engine), ` +
	`climate (set cabin temperature — put the Celsius value in params.temp), ` +
	`locate (find/show the car), status. ` +
	`If the user asks to perform one, set "action" accordingly and let "reply" confirm it briefly. ` +
	`For questions about battery, range, lock state or location, set action "none" and answer ONLY ` +
	`from the car context below — never invent values that are not present. ` +
	`For small talk or anything unrelated, action "none". ` +
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
			"temperature":      0.3,
			"maxOutputTokens":  512,
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
	j, _ := json.Marshal(body)
	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		c.location, c.project, c.location, c.model)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(j))
	req.Header.Set("Content-Type", "application/json")
	tok, err := c.token(ctx)
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.http.Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return Reply{}, fmt.Errorf("gemini %s: %s", resp.Status, b.String())
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
		return Reply{}, err
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return Reply{Action: "none"}, nil
	}
	raw := gr.Candidates[0].Content.Parts[0].Text
	var r Reply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Reply{Text: raw, Action: "none"}, nil // tolerate non-JSON, show as plain text
	}
	if r.Action == "" {
		r.Action = "none"
	}
	return r, nil
}
