package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// webAPI speaks the Kimi Code 2.2 REST API. It never owns the server process.
type webAPI struct {
	base, tokenFile string
	client          *http.Client
}
type webAPIError struct {
	Code    int
	Message string
}

func (e *webAPIError) Error() string { return fmt.Sprintf("kimi web: API %d: %s", e.Code, e.Message) }
func (a *webAPI) token() (string, error) {
	b, err := os.ReadFile(a.tokenFile)
	if err != nil {
		return "", fmt.Errorf("kimi web: read token file: %w", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("kimi web: token file is empty")
	}
	return t, nil
}
func (a *webAPI) request(ctx context.Context, method, path string, body, out any) error {
	var err error
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("kimi web: encode request: %w", err)
		}
	}
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	return a.requestBody(ctx, method, path, contentType, bytes.NewReader(data), out)
}

func (a *webAPI) requestBody(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	token, err := a.token()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return fmt.Errorf("kimi web: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("kimi web: request failed: %s", core.RedactToken(err.Error(), token))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kimi web: HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Code *int            `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("kimi web: decode response: %w", err)
	}
	if envelope.Code == nil {
		return fmt.Errorf("kimi web: missing API response code")
	}
	if *envelope.Code != 0 {
		return &webAPIError{*envelope.Code, core.RedactToken(envelope.Msg, token)}
	}
	if out != nil {
		if err = json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("kimi web: decode data: %w", err)
		}
	}
	return nil
}
func webSessionPath(id string) string { return "/api/v1/sessions/" + url.PathEscape(id) }

type webRecord struct {
	AgentConfig struct {
		Model    string `json:"model"`
		Thinking string `json:"thinking"`
	} `json:"agent_config"`
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	UpdatedAt      json.RawMessage `json:"updated_at"`
	MessageCount   int             `json:"message_count"`
	CurrentPrompt  string          `json:"current_prompt_id"`
	MainTurnActive bool            `json:"main_turn_active"`
	Usage          struct {
		ContextTokens int `json:"context_tokens"`
		ContextLimit  int `json:"context_limit"`
	} `json:"usage"`
	Metadata struct {
		Cwd string `json:"cwd"`
	} `json:"metadata"`
}
type webMessage struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	PromptID string `json:"prompt_id"`
	Content  []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	CreatedAt json.RawMessage `json:"created_at"`
}

func (m webMessage) text() string {
	var b strings.Builder
	for _, p := range m.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
func webTime(raw json.RawMessage) time.Time {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return time.UnixMilli(n)
	}
	return time.Time{}
}
