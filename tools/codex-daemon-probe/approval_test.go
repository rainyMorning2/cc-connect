package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func approvalMessage(id, thread, turn string) message {
	b, _ := json.Marshal(map[string]any{"threadId": thread, "turnId": turn, "command": "printf APPROVAL-E2E", "availableDecisions": []string{"accept", "decline"}})
	return message{ID: json.RawMessage(id), Method: "item/commandExecution/requestApproval", Params: b}
}

func TestApprovalReplyCannotTargetAnotherThreadOrUnsupportedRequest(t *testing.T) {
	tracker := &approvalTracker{thread: "target", pending: map[string]pendingApproval{}}
	for _, m := range []message{approvalMessage(`1`, "other", "turn"), {ID: json.RawMessage(`2`), Method: "item/tool/call", Params: json.RawMessage(`{"threadId":"target"}`)}} {
		if err := tracker.observe(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{`1`, `2`} {
		err := tracker.reply(approvalControl{RequestID: json.RawMessage(id), Decision: "accept"}, func(any) error { t.Fatal("wrote unauthorized reply"); return nil })
		if err == nil {
			t.Fatal("allowed unrelated request")
		}
	}
}

func TestResolvedApprovalRejectsStaleReplyAndPreservesOtherRequest(t *testing.T) {
	tracker := &approvalTracker{thread: "target", pending: map[string]pendingApproval{}}
	for _, id := range []string{`42`, `"42"`} {
		if err := tracker.observe(approvalMessage(id, "target", "turn")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tracker.observe(message{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"other","requestId":42}`)}); err != nil {
		t.Fatal(err)
	}
	if len(tracker.pending) != 2 {
		t.Fatal("unrelated resolution removed approval")
	}
	if err := tracker.observe(message{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"target","requestId":42}`)}); err != nil {
		t.Fatal(err)
	}
	if len(tracker.pending) != 1 {
		t.Fatal("resolution removed wrong requests")
	}
	if err := tracker.reply(approvalControl{RequestID: json.RawMessage(`42`), Decision: "accept"}, func(any) error { t.Fatal("stale reply written"); return nil }); err == nil {
		t.Fatal("allowed stale reply")
	}
	writes := 0
	for i := 0; i < 2; i++ {
		err := tracker.reply(approvalControl{RequestID: json.RawMessage(`"42"`), Decision: "decline"}, func(any) error { writes++; return nil })
		if (err == nil) != (i == 0) {
			t.Fatalf("reply %d err=%v", i, err)
		}
	}
	if writes != 1 {
		t.Fatalf("writes=%d", writes)
	}
}

func TestApprovalRejectsPersistentAndUnavailableDecisions(t *testing.T) {
	tracker := &approvalTracker{thread: "target", pending: map[string]pendingApproval{}}
	m := approvalMessage(`5`, "target", "turn")
	m.Params = json.RawMessage(`{"threadId":"target","turnId":"turn","availableDecisions":["decline"]}`)
	if err := tracker.observe(m); err != nil {
		t.Fatal(err)
	}
	for _, decision := range []string{"acceptForSession", "accept"} {
		if err := tracker.reply(approvalControl{RequestID: json.RawMessage(`5`), Decision: decision}, func(any) error { t.Fatal("unsupported decision written"); return nil }); err == nil {
			t.Fatal("allowed unsupported decision")
		}
	}
	if len(tracker.pending) != 1 {
		t.Fatal("rejected input removed pending request")
	}
}

func TestCompletedOldTurnDoesNotClearNewTurnApproval(t *testing.T) {
	tracker := &approvalTracker{thread: "target", pending: map[string]pendingApproval{}}
	if err := tracker.observe(approvalMessage(`9`, "target", "new")); err != nil {
		t.Fatal(err)
	}
	if err := tracker.observe(message{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"target","turn":{"id":"old"}}`)}); err != nil {
		t.Fatal(err)
	}
	if len(tracker.pending) != 1 {
		t.Fatal("old completion cleared new approval")
	}
}

func TestInteractiveApprovalWireResponseAndResolvedNotification(t *testing.T) {
	socket := fixture(t, func(ws *websocket.Conn) {
		if err := ws.WriteJSON(approvalMessage(`"approval-1"`, "target", "turn")); err != nil {
			t.Error(err)
			return
		}
		var response message
		if err := ws.ReadJSON(&response); err != nil {
			t.Error(err)
			return
		}
		if string(response.ID) != `"approval-1"` || string(response.Result) != `{"decision":"accept"}` || response.Method != "" {
			t.Errorf("wrong approval response: %+v", response)
		}
		_ = ws.WriteJSON(message{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"target","requestId":"approval-1"}`)})
		if _, _, err := ws.ReadMessage(); err == nil {
			t.Error("duplicate approval response sent")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := connect(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	defer outW.Close()
	lines := make(chan map[string]json.RawMessage, 16)
	go func() {
		s := bufio.NewScanner(outR)
		for s.Scan() {
			var m map[string]json.RawMessage
			if json.Unmarshal(s.Bytes(), &m) == nil {
				lines <- m
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- watchInteractive(ctx, c, "target", json.NewEncoder(outW), inR) }()
	wait := func(key, value string) {
		t.Helper()
		for {
			select {
			case m := <-lines:
				if string(m[key]) == value {
					return
				}
			case <-ctx.Done():
				t.Fatal("interactive test timed out")
			}
		}
	}
	wait("method", `"item/commandExecution/requestApproval"`)
	_, _ = io.WriteString(inW, "{\"action\":\"reply\",\"requestId\":\"approval-1\",\"decision\":\"accept\"}\n")
	wait("probe", `"approvalReplySent"`)
	wait("method", `"serverRequest/resolved"`)
	_, _ = io.WriteString(inW, "{\"action\":\"reply\",\"requestId\":\"approval-1\",\"decision\":\"accept\"}\n")
	wait("probe", `"controlRejected"`)
	_, _ = io.WriteString(inW, "{\"action\":\"detach\"}\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("detach did not stop watcher")
	}
}
