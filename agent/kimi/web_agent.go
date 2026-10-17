package kimi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type webAgent struct {
	api        *webAPI
	workDir    string
	attachOnly bool
	mu         sync.Mutex
	sessions   map[*webSession]struct{}
	stopped    bool
}

var _ core.AgentSessionAttacher = (*webAgent)(nil)
var _ core.HistoryProvider = (*webAgent)(nil)

func newWebAgent(opts map[string]any) (core.Agent, error) {
	readString := func(key, fallback string) (string, error) {
		v, ok := opts[key]
		if !ok {
			return fallback, nil
		}
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("kimi web: %s must be a nonempty string", key)
		}
		return s, nil
	}
	base, err := readString("web_url", "http://127.0.0.1:58627")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("kimi web: web_url must be an http(s) origin without credentials, path, query or fragment")
	}
	// Never redirect authenticated requests to another server.
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("kimi web: home directory: %w", err)
	}
	kimiHome := os.Getenv("KIMI_CODE_HOME")
	if kimiHome == "" {
		kimiHome = filepath.Join(home, ".kimi-code")
	}
	token, err := readString("web_token_file", filepath.Join(kimiHome, "server.token"))
	if err != nil {
		return nil, err
	}
	workDir, err := readString("work_dir", ".")
	if err != nil {
		return nil, err
	}
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("kimi web: work_dir: %w", err)
	}
	attachOnly := false
	if v, ok := opts["web_attach_only"]; ok {
		var valid bool
		attachOnly, valid = v.(bool)
		if !valid {
			return nil, fmt.Errorf("kimi web: web_attach_only must be a boolean")
		}
	}
	for _, key := range []string{"env", "model", "mode", "provider", "timeout_mins"} {
		if _, ok := opts[key]; ok {
			return nil, fmt.Errorf("kimi web: configure %s in the Kimi server; attached settings are preserved", key)
		}
	}
	return &webAgent{api: &webAPI{base: strings.TrimRight(base, "/"), tokenFile: token, client: client}, workDir: workDir, attachOnly: attachOnly, sessions: map[*webSession]struct{}{}}, nil
}
func (*webAgent) Name() string                { return "kimi" }
func (*webAgent) AutoSteerBusyMessages() bool { return false }
func (a *webAgent) GetWorkDir() string        { return a.workDir }
func (a *webAgent) BaseOpts() map[string]any {
	return map[string]any{"backend": "web", "web_url": a.api.base, "web_token_file": a.api.tokenFile, "web_attach_only": a.attachOnly}
}
func (a *webAgent) StartSession(ctx context.Context, id string) (core.AgentSession, error) {
	if id != "" && id != core.ContinueSession {
		return a.AttachSession(ctx, id)
	}
	if a.attachOnly {
		return nil, fmt.Errorf("kimi web: select an existing session with /attach or /switch")
	}
	if id == core.ContinueSession {
		return nil, fmt.Errorf("kimi web: select an explicit session ID")
	}
	// Kimi 2.2's create endpoint does not populate agent_config.model from
	// default_model; copy the service's own default only when creating a session.
	var config struct {
		Model string `json:"default_model"`
	}
	if err := a.api.request(ctx, "GET", "/api/v1/config", nil, &config); err != nil {
		return nil, err
	}
	body := map[string]any{"metadata": map[string]string{"cwd": a.workDir}}
	if config.Model != "" {
		body["agent_config"] = map[string]any{"model": config.Model}
	}
	var r webRecord
	if err := a.api.request(ctx, "POST", "/api/v1/sessions", body, &r); err != nil {
		return nil, err
	}
	if r.ID == "" {
		return nil, fmt.Errorf("kimi web: create returned no session ID")
	}
	return a.AttachSession(ctx, r.ID)
}
func (a *webAgent) AttachSession(ctx context.Context, id string) (core.AgentSession, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#") {
		return nil, fmt.Errorf("kimi web: invalid session ID")
	}
	s, err := newWebSession(ctx, a, id)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		s.Close()
		return nil, fmt.Errorf("kimi web: agent stopped")
	}
	a.sessions[s] = struct{}{}
	a.mu.Unlock()
	return s, nil
}
func (a *webAgent) Stop() error {
	a.mu.Lock()
	a.stopped = true
	ss := make([]*webSession, 0, len(a.sessions))
	for s := range a.sessions {
		ss = append(ss, s)
	}
	a.mu.Unlock()
	for _, s := range ss {
		s.Close()
	}
	return nil
}
func (a *webAgent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	var result []core.AgentSessionInfo
	after := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Items   []webRecord `json:"items"`
			HasMore bool        `json:"has_more"`
		}
		path := "/api/v1/sessions?page_size=100&include_archive=false&exclude_empty=false"
		if after != "" {
			path += "&before_id=" + url.QueryEscape(after)
		}
		if err := a.api.request(ctx, "GET", path, nil, &page); err != nil {
			return nil, err
		}
		for _, r := range page.Items {
			if filepath.Clean(r.Metadata.Cwd) == a.workDir {
				result = append(result, core.AgentSessionInfo{ID: r.ID, Summary: r.Title, MessageCount: r.MessageCount, ModifiedAt: webTime(r.UpdatedAt)})
			}
		}
		if !page.HasMore || len(page.Items) == 0 {
			break
		}
		after = page.Items[len(page.Items)-1].ID
		if seen[after] {
			return nil, fmt.Errorf("kimi web: session pagination did not advance")
		}
		seen[after] = true
	}
	return result, nil
}
func (a *webAgent) messages(ctx context.Context, id string, limit int) ([]webMessage, error) {
	var messages []webMessage
	before := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Items   []webMessage `json:"items"`
			HasMore bool         `json:"has_more"`
		}
		path := webSessionPath(id) + "/messages?page_size=100"
		if before != "" {
			path += "&before_id=" + url.QueryEscape(before)
		}
		if err := a.api.request(ctx, "GET", path, nil, &page); err != nil {
			return nil, err
		}
		messages = append(messages, page.Items...)
		if !page.HasMore || len(page.Items) == 0 || (limit > 0 && len(messages) >= limit) {
			break
		}
		before = page.Items[len(page.Items)-1].ID
		if seen[before] {
			return nil, fmt.Errorf("kimi web: history pagination did not advance")
		}
		seen[before] = true
	}
	sort.SliceStable(messages, func(i, j int) bool { return webTime(messages[i].CreatedAt).Before(webTime(messages[j].CreatedAt)) })
	return messages, nil
}
func (a *webAgent) GetSessionHistory(ctx context.Context, id string, limit int) ([]core.HistoryEntry, error) {
	// Fetch all roles before limiting, so tool messages do not consume the user's history limit.
	messages, err := a.messages(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	var entries []core.HistoryEntry
	for _, m := range messages {
		if (m.Role == "user" || m.Role == "assistant") && m.text() != "" {
			entries = append(entries, core.HistoryEntry{Role: m.Role, Content: m.text(), Timestamp: webTime(m.CreatedAt)})
		}
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

func (a *webAgent) WorkspaceAgentOptions() map[string]any { return a.BaseOpts() }
func (*webAgent) DefaultSettingsOnly() bool               { return true }
