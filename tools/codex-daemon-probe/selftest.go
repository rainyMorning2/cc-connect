package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
)

type threadSnapshot struct {
	Thread struct {
		ID    string `json:"id"`
		Turns []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turns"`
	} `json:"thread"`
}

func activeTurn(s threadSnapshot) string {
	for _, turn := range s.Thread.Turns {
		if turn.Status == "inProgress" {
			return turn.ID
		}
	}
	return ""
}

func input(text string) []any {
	return []any{map[string]any{"type": "text", "text": text, "text_elements": []any{}}}
}

func waitFor(ctx context.Context, c *client, method string, thread string) (message, error) {
	for {
		m, err := c.next(ctx)
		if err != nil {
			return m, err
		}
		if m.Method != method {
			continue
		}
		var p struct {
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return m, err
		}
		if p.ThreadID == thread {
			return m, nil
		}
	}
}

// A local fake Responses provider makes the real daemon exercise its runtime
// without inference charges or external requests. The command approval is
// declined; no shell command is executed. All control RPCs use ONLY the ID
// returned by this function's thread/start, never an existing thread ID.
func selfTestDaemon(ctx context.Context, info daemonInfo, out *json.Encoder) error {
	ctx, cancel := context.WithCancel(ctx)
	var calls atomic.Int32
	var sawSteer atomic.Bool
	hold := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
		if err != nil {
			http.Error(w, "fixture read failed", 500)
			return
		}
		if strings.Contains(string(body), "cc-connect-steer-marker") {
			sawSteer.Store(true)
		}
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("probe-%d", n)}})
		if n >= 3 {
			select {
			case hold <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
			case <-ctx.Done():
			}
			return
		}
		item := map[string]any{"type": "message", "role": "assistant", "id": "probe-message", "content": []any{map[string]any{"type": "output_text", "text": "cc-connect-probe-complete"}}}
		if n == 1 {
			args, _ := json.Marshal(map[string]any{"cmd": "python3 -c 'print(42)'", "sandbox_permissions": "require_escalated", "justification": "CC Connect isolated probe; decline this request."})
			item = map[string]any{"type": "function_call", "call_id": "probe-approval", "name": "exec_command", "arguments": string(args)}
		}
		emit(map[string]any{"type": "response.output_item.done", "item": item})
		emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("probe-%d", n), "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	defer func() {
		cancel()
		server.Close()
	}()
	a, err := connect(ctx, info.SocketPath)
	if err != nil {
		return err
	}
	defer a.close()
	stopA := context.AfterFunc(ctx, a.close)
	defer stopA()
	cwd, err := os.MkdirTemp("", "cc-connect-daemon-probe-")
	if err != nil {
		return err
	}
	// Keep the empty cwd so the retained diagnostic thread has a valid workspace.
	var started threadSnapshot
	if err := a.request(ctx, "thread/start", map[string]any{
		"cwd": cwd, "model": "gpt-5.4", "modelProvider": "cc_connect_probe",
		"approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "read-only",
		"config": map[string]any{"model_providers.cc_connect_probe": map[string]any{
			"name": "CC Connect local probe fixture", "base_url": server.URL, "wire_api": "responses",
			"requires_openai_auth": false, "supports_websockets": false, "request_max_retries": 0, "stream_max_retries": 0,
		}},
	}, &started); err != nil {
		return err
	}
	tid := started.Thread.ID
	if tid == "" || tid == os.Getenv("CODEX_THREAD_ID") {
		return fmt.Errorf("test did not create a separate thread")
	}
	report := func(check string) error {
		return out.Encode(map[string]any{"check": check, "threadId": tid, "cwd": cwd, "passed": true})
	}
	if err := report("created isolated thread with local fake provider"); err != nil {
		return err
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := a.request(ctx, "turn/start", map[string]any{"threadId": tid, "input": input("cc-connect isolated probe")}, &turn); err != nil {
		return err
	}
	approval, err := waitFor(ctx, a, "item/commandExecution/requestApproval", tid)
	if err != nil {
		return fmt.Errorf("primary approval: %w", err)
	}
	b, err := connect(ctx, info.SocketPath)
	if err != nil {
		return err
	}
	defer b.close()
	stopB := context.AfterFunc(ctx, b.close)
	defer stopB()
	var resumed threadSnapshot
	if err := b.request(ctx, "thread/resume", map[string]any{"threadId": tid}, &resumed); err != nil {
		return err
	}
	if activeTurn(resumed) != turn.Turn.ID || turn.Turn.ID == "" {
		return fmt.Errorf("second connection did not recover active turn from resume")
	}
	replay, err := waitFor(ctx, b, "item/commandExecution/requestApproval", tid)
	if err != nil {
		return err
	}
	if string(replay.ID) != string(approval.ID) {
		return fmt.Errorf("approval replay ID changed")
	}
	if err := report("second client resume: same active turn and pending approval"); err != nil {
		return err
	}
	var steered struct {
		TurnID string `json:"turnId"`
	}
	if err := b.request(ctx, "turn/steer", map[string]any{"threadId": tid, "expectedTurnId": turn.Turn.ID, "input": input("cc-connect-steer-marker")}, &steered); err != nil {
		return err
	}
	if steered.TurnID != turn.Turn.ID {
		return fmt.Errorf("steer returned a different turn")
	}
	if err := report("cross-client steer accepted for original active turn"); err != nil {
		return err
	}
	b.close()
	c, err := connect(ctx, info.SocketPath)
	if err != nil {
		return err
	}
	defer c.close()
	stopC := context.AfterFunc(ctx, c.close)
	defer stopC()
	if err := c.request(ctx, "thread/resume", map[string]any{"threadId": tid}, &resumed); err != nil {
		return err
	}
	if activeTurn(resumed) != turn.Turn.ID {
		return fmt.Errorf("turn did not survive observer disconnection")
	}
	replay, err = waitFor(ctx, c, "item/commandExecution/requestApproval", tid)
	if err != nil {
		return err
	}
	if string(replay.ID) != string(approval.ID) {
		return fmt.Errorf("reconnected approval ID changed")
	}
	if err := report("observer reconnect preserved turn and replayed pending approval"); err != nil {
		return err
	}
	if err := c.write(ctx, map[string]any{"id": replay.ID, "result": map[string]any{"decision": "decline"}}); err != nil {
		return err
	}
	if _, err := waitFor(ctx, a, "serverRequest/resolved", tid); err != nil {
		return fmt.Errorf("approval resolution broadcast: %w", err)
	}
	if err := report("secondary client declined approval; primary received resolution"); err != nil {
		return err
	}
	// A live completed message proves the observer sees output after attachment.
	for {
		m, err := waitFor(ctx, c, "item/completed", tid)
		if err != nil {
			return err
		}
		if strings.Contains(string(m.Params), "cc-connect-probe-complete") {
			break
		}
	}
	completed, err := waitFor(ctx, c, "turn/completed", tid)
	if err != nil {
		return err
	}
	if !strings.Contains(string(completed.Params), `"completed"`) {
		return fmt.Errorf("first turn did not complete: %s", completed.Params)
	}
	if !sawSteer.Load() {
		return fmt.Errorf("steer acknowledged but missing from subsequent model input")
	}
	if err := report("live output and completion; steer marker reached model input"); err != nil {
		return err
	}
	if err := a.request(ctx, "turn/start", map[string]any{"threadId": tid, "input": input("hold for isolated interrupt test")}, &turn); err != nil {
		return err
	}
	select {
	case <-hold:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := c.request(ctx, "turn/interrupt", map[string]any{"threadId": tid, "turnId": turn.Turn.ID}, nil); err != nil {
		return err
	}
	completed, err = waitFor(ctx, c, "turn/completed", tid)
	if err != nil {
		return err
	}
	if !strings.Contains(string(completed.Params), `"interrupted"`) {
		return fmt.Errorf("interrupt did not finish with interrupted status: %s", completed.Params)
	}
	return report("cross-client interrupt on separate test turn")
}
