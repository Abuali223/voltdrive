// Package voice proxies UzbekVoice.ai (Mohir AI) speech APIs so the API key
// stays server-side. STT transcribes Uzbek audio; TTS synthesizes Uzbek speech.
//
// Response shapes are parsed defensively (the transcript is found by locating a
// "text" field anywhere in the JSON; audio is taken as raw bytes or, if the
// response is JSON, fetched from the first URL in it). Raw responses are logged
// to ease format debugging.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const (
	sttURL = "https://uzbekvoice.ai/api/v1/stt"
	ttsURL = "https://uzbekvoice.ai/api/v1/tts"
)

type Client struct {
	key  string
	http *http.Client
}

func NewClient(key string) *Client {
	return &Client{key: key, http: &http.Client{Timeout: 90 * time.Second}}
}

func (c *Client) Enabled() bool { return c != nil && c.key != "" }

// STT transcribes Uzbek audio and returns the recognized text.
func (c *Client) STT(ctx context.Context, audio []byte, filename string) (string, error) {
	if filename == "" {
		filename = "audio.webm"
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(audio); err != nil {
		return "", err
	}
	_ = w.WriteField("language", "uz")
	_ = w.WriteField("model", "general")
	_ = w.WriteField("blocking", "true")
	_ = w.WriteField("return_offsets", "false")
	_ = w.WriteField("run_diarization", "false")
	_ = w.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, sttURL, &buf)
	req.Header.Set("Authorization", c.key)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("stt %s: %s", resp.Status, truncate(body))
	}
	log.Printf("uzbekvoice STT raw: %s", truncate(body))
	return findString(decodeAny(body), "text"), nil
}

// TTS synthesizes Uzbek speech and returns the audio bytes plus content type.
func (c *Client) TTS(ctx context.Context, text, model string) ([]byte, string, error) {
	if model == "" {
		model = "lola"
	}
	reqBody, _ := json.Marshal(map[string]any{"text": text, "model": model, "blocking": "true"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ttsURL, bytes.NewReader(reqBody))
	req.Header.Set("Authorization", c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("tts %s: %s", resp.Status, truncate(data))
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "application/octet-stream") {
		return data, ct, nil // raw audio bytes
	}
	// Otherwise the response is JSON; log it and fetch the first URL (the audio).
	log.Printf("uzbekvoice TTS raw (%s): %s", ct, truncate(data))
	url := findString(decodeAny(data), "")
	if url == "" {
		return nil, "", fmt.Errorf("tts: no audio url in response: %s", truncate(data))
	}
	areq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	aresp, err := c.http.Do(areq)
	if err != nil {
		return nil, "", err
	}
	defer aresp.Body.Close()
	adata, _ := io.ReadAll(aresp.Body)
	act := aresp.Header.Get("Content-Type")
	if act == "" {
		act = "audio/mpeg"
	}
	return adata, act, nil
}

func decodeAny(b []byte) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

// findString returns the first string value under the given key (recursively).
// With key=="" it returns the first http(s) URL found anywhere.
func findString(v any, key string) string {
	switch t := v.(type) {
	case string:
		if key == "" && strings.HasPrefix(t, "http") {
			return t
		}
	case map[string]any:
		if key != "" {
			if s, ok := t[key].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
		for _, val := range t {
			if s := findString(val, key); s != "" {
				return s
			}
		}
	case []any:
		for _, val := range t {
			if s := findString(val, key); s != "" {
				return s
			}
		}
	}
	return ""
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
