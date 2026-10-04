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

// Uses a new root thread and a loopback model fixture. No shell, file edits,
// real inference or existing-thread control are involved.
func selfTestUserInput(ctx context.Context, info daemonInfo, out *json.Encoder) error {
	server, state := newUserInputFixture()
	defer server.Close()
	primary, err := connect(ctx, info.SocketPath)
	if err != nil {
		return err
	}
	defer primary.close()
	stopPrimary := context.AfterFunc(ctx, primary.close)
	defer stopPrimary()
	cwd, err := os.MkdirTemp("", "cc-connect-userinput-probe-")
	if err != nil {
		return err
	}
	var started threadSnapshot
	if err := primary.request(ctx, "thread/start", map[string]any{
		"cwd": cwd, "model": "gpt-5.4", "modelProvider": "cc_connect_question_probe",
		"approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "read-only",
		"config": map[string]any{"model_providers.cc_connect_question_probe": map[string]any{
			"name": "CC Connect question fixture", "base_url": server.URL, "wire_api": "responses",
			"requires_openai_auth": false, "supports_websockets": false, "request_max_retries": 0, "stream_max_retries": 0,
		}},
	}, &started); err != nil {
		return err
	}
	thread := started.Thread.ID
	if thread == "" || thread == os.Getenv("CODEX_THREAD_ID") {
		return fmt.Errorf("question test did not create a separate thread")
	}
	for _, externalSkip := range []bool{false, true} {
		if err := selfTestQuestionRound(ctx, primary, info.SocketPath, thread, externalSkip, out); err != nil {
			return err
		}
	}
	if !state.sawAnswer.Load() || state.calls.Load() != 4 {
		return fmt.Errorf("question answer did not reach subsequent model input or unexpected inference count: sawAnswer=%v calls=%d", state.sawAnswer.Load(), state.calls.Load())
	}
	return out.Encode(map[string]any{"check": "Other text reached subsequent model input; both turns completed", "passed": true, "threadId": thread, "cwd": cwd})
}

type userInputFixtureState struct {
	calls     atomic.Int32
	sawAnswer atomic.Bool
}

func newUserInputFixture() (*httptest.Server, *userInputFixtureState) {
	state := &userInputFixtureState{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
		if err != nil || r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		n := state.calls.Add(1)
		if n == 2 && strings.Contains(string(body), "PROBE-OTHER-ANSWER") {
			state.sawAnswer.Store(true)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("question-%d", n)}})
		item := map[string]any{"type": "message", "role": "assistant", "id": fmt.Sprintf("question-message-%d", n),
			"content": []any{map[string]any{"type": "output_text", "text": "PROBE-QUESTION-DONE"}}}
		if n%2 == 1 {
			questions := []any{}
			for _, id := range []string{"choice", "detail"} {
				questions = append(questions, map[string]any{"id": id, "header": id, "question": "Choose " + id + "?",
					"options": []any{map[string]any{"label": "A", "description": "First"}, map[string]any{"label": "B", "description": "Second"}}})
			}
			args, _ := json.Marshal(map[string]any{"questions": questions})
			item = map[string]any{"type": "function_call", "name": "request_user_input", "call_id": fmt.Sprintf("question-call-%d", n), "arguments": string(args)}
		}
		emit(map[string]any{"type": "response.output_item.done", "item": item})
		emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("question-%d", n),
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	return server, state
}

func selfTestQuestionRound(ctx context.Context, primary *client, socket, thread string, externalSkip bool, out *json.Encoder) error {
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := primary.request(ctx, "turn/start", map[string]any{"threadId": thread, "input": input("isolated question test"),
		"collaborationMode": map[string]any{"mode": "plan", "settings": map[string]any{
			"model": "gpt-5.4", "reasoning_effort": "medium", "developer_instructions": nil,
		}},
	}, &started); err != nil {
		return err
	}
	original, err := waitFor(ctx, primary, "item/tool/requestUserInput", thread)
	if err != nil {
		return err
	}
	// Resume twice: detach only the observer while the original question waits.
	observer, replay, err := resumeQuestion(ctx, socket, thread, started.Turn.ID)
	if err != nil {
		return err
	}
	if string(replay.ID) != string(original.ID) {
		observer.close()
		return fmt.Errorf("question request ID changed on attach")
	}
	observer.close()
	observer, replay, err = resumeQuestion(ctx, socket, thread, started.Turn.ID)
	if err != nil {
		return err
	}
	defer observer.close()
	stopObserver := context.AfterFunc(ctx, observer.close)
	defer stopObserver()
	if string(replay.ID) != string(original.ID) {
		return fmt.Errorf("question request ID changed on reconnect")
	}
	tracker := &userInputTracker{thread: thread, pending: map[string]pendingUserInput{}}
	if err := tracker.observe(replay); err != nil {
		return err
	}
	questions := tracker.list()[0]
	if !questions.IsBlocking || len(questions.Questions) != 2 || !questions.Questions[1].IsOther {
		return fmt.Errorf("expected blocking multi-question request with Other: %s", replay.Params)
	}
	cmd := interactiveControl{Action: "answer", RequestID: replay.ID, Answers: map[string]userInputAnswer{
		"choice": {Answers: []string{"A"}}, "detail": {Answers: []string{"None of the above", "user_note: PROBE-OTHER-ANSWER"}},
	}}
	check := "observer answered replayed questions after reconnect"
	if externalSkip {
		cmd = interactiveControl{Action: "skip", RequestID: original.ID}
		check = "primary skipped question; observer cleared pending and rejected stale answer"
	}
	writer := observer
	sender := tracker
	if externalSkip {
		writer = primary
		sender = &userInputTracker{thread: thread, pending: map[string]pendingUserInput{}}
		if err := sender.observe(original); err != nil {
			return err
		}
	}
	if err := sender.reply(cmd, func(v any) error { return writer.write(ctx, v) }); err != nil {
		return err
	}
	primaryResolved, err := waitFor(ctx, primary, "serverRequest/resolved", thread)
	if err != nil {
		return err
	}
	if err := validateQuestionResolution(primaryResolved, original.ID); err != nil {
		return err
	}
	resolved, completed, err := finishQuestionRound(ctx, observer, tracker, cmd, started.Turn.ID)
	if err != nil {
		return err
	}
	return out.Encode(map[string]any{"check": check, "passed": true, "threadId": thread, "turnId": started.Turn.ID,
		"request": replay, "primaryResolved": primaryResolved, "resolved": resolved, "completion": completed})
}

func finishQuestionRound(ctx context.Context, observer *client, tracker *userInputTracker, cmd interactiveControl, turn string) (message, message, error) {
	resolved, err := waitFor(ctx, observer, "serverRequest/resolved", tracker.thread)
	if err != nil {
		return message{}, message{}, err
	}
	if err := validateQuestionResolution(resolved, cmd.RequestID); err != nil {
		return message{}, message{}, err
	}
	if err := tracker.observe(resolved); err != nil {
		return message{}, message{}, err
	}
	wrote := false
	if err := tracker.reply(cmd, func(any) error { wrote = true; return nil }); err == nil || wrote || len(tracker.pending) != 0 {
		return message{}, message{}, fmt.Errorf("resolved question remained answerable")
	}
	completed, err := waitFor(ctx, observer, "turn/completed", tracker.thread)
	if err != nil {
		return message{}, message{}, err
	}
	var params struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(completed.Params, &params); err != nil {
		return message{}, message{}, err
	}
	if params.Turn.ID != turn || params.Turn.Status != "completed" {
		return message{}, message{}, fmt.Errorf("question reply did not complete original turn: %s", completed.Params)
	}
	return resolved, completed, nil
}

func validateQuestionResolution(resolved message, id json.RawMessage) error {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(resolved.Params, &params); err != nil {
		return err
	}
	if string(params.RequestID) != string(id) {
		return fmt.Errorf("resolved a different question")
	}
	return nil
}

func resumeQuestion(ctx context.Context, socket, thread, turn string) (*client, message, error) {
	c, err := connect(ctx, socket)
	if err != nil {
		return nil, message{}, err
	}
	var snapshot threadSnapshot
	if err := c.request(ctx, "thread/resume", map[string]any{"threadId": thread}, &snapshot); err != nil {
		c.close()
		return nil, message{}, err
	}
	if activeTurn(snapshot) != turn {
		c.close()
		return nil, message{}, fmt.Errorf("question resume did not recover original active turn")
	}
	m, err := waitFor(ctx, c, "item/tool/requestUserInput", thread)
	if err != nil {
		c.close()
		return nil, message{}, err
	}
	return c, m, nil
}
