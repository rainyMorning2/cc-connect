package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func questionMessage(id, thread, turn string) message {
	params, _ := json.Marshal(map[string]any{"threadId": thread, "turnId": turn, "itemId": "ask",
		"questions": []any{
			map[string]any{"id": "choice", "header": "Choice", "question": "Choose?", "isOther": true, "options": []any{map[string]any{"label": "A", "description": "First"}}},
			map[string]any{"id": "text", "header": "Text", "question": "Explain?", "isSecret": true, "options": nil},
		}})
	return message{ID: json.RawMessage(id), Method: "item/tool/requestUserInput", Params: params}
}

func TestUserInputAnswersPreserveQuestionIDsOtherTextAndRequestID(t *testing.T) {
	tracker := &userInputTracker{thread: "target", pending: map[string]pendingUserInput{}}
	m := questionMessage(`"42"`, "target", "turn")
	if err := tracker.observe(m); err != nil {
		t.Fatal(err)
	}
	requests := tracker.list()
	if len(requests) != 1 || !requests[0].IsBlocking || !requests[0].Questions[0].IsOther || !requests[0].Questions[1].IsSecret {
		t.Fatalf("lost request metadata: %+v", requests)
	}
	cmd := interactiveControl{Action: "answer", RequestID: m.ID, Answers: map[string]userInputAnswer{
		"choice": {Answers: []string{"None of the above", "user_note: 使用中文替代选项"}},
		"text":   {Answers: []string{"自由文本"}},
	}}
	writeErr := errors.New("connection failed")
	if err := tracker.reply(cmd, func(any) error { return writeErr }); !errors.Is(err, writeErr) {
		t.Fatalf("write error lost: %v", err)
	}
	if len(tracker.pending) != 1 {
		t.Fatal("failed write removed request")
	}
	if err := tracker.reply(cmd, func(v any) error {
		wire, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				Answers map[string]userInputAnswer `json:"answers"`
			} `json:"result"`
		}
		if err := json.Unmarshal(wire, &response); err != nil {
			return err
		}
		if string(response.ID) != `"42"` || !reflect.DeepEqual(response.Result.Answers, cmd.Answers) {
			t.Fatalf("wrong response: %s", wire)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.reply(cmd, func(any) error { t.Fatal("duplicate answer written"); return nil }); err == nil {
		t.Fatal("duplicate answer accepted")
	}
}

func TestUserInputIsolationResolutionAndOldTurnCompletion(t *testing.T) {
	tracker := &userInputTracker{thread: "target", pending: map[string]pendingUserInput{}}
	for _, m := range []message{questionMessage(`1`, "other", "turn"), questionMessage(`42`, "target", "old"), questionMessage(`"42"`, "target", "new")} {
		if err := tracker.observe(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, params := range []string{`{"threadId":"other","requestId":"42"}`, `{"threadId":"target","requestId":42}`} {
		if err := tracker.observe(message{Method: "serverRequest/resolved", Params: json.RawMessage(params)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tracker.observe(message{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"target","turn":{"id":"old"}}`)}); err != nil {
		t.Fatal(err)
	}
	if len(tracker.pending) != 1 {
		t.Fatal("resolution/completion removed wrong question")
	}
	for _, id := range []string{`1`, `42`} {
		if err := tracker.reply(interactiveControl{Action: "skip", RequestID: json.RawMessage(id)}, func(any) error { t.Fatal("stale/unrelated reply written"); return nil }); err == nil {
			t.Fatal("accepted stale/unrelated reply")
		}
	}
	if err := tracker.observe(message{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"target","turn":{"id":"new"}}`)}); err != nil {
		t.Fatal(err)
	}
	if len(tracker.pending) != 0 {
		t.Fatal("completion left pending question")
	}
}

func TestUserInputRejectsMissingUnknownEmptyAnswersAndApprovalDecision(t *testing.T) {
	tracker := &userInputTracker{thread: "target", pending: map[string]pendingUserInput{}}
	if err := tracker.observe(questionMessage(`1`, "target", "turn")); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []interactiveControl{
		{Action: "answer"},
		{Action: "answer", Decision: "other"},
		{Action: "answer", Answers: map[string]userInputAnswer{"choice": {Answers: []string{"A"}}}},
		{Action: "answer", Answers: map[string]userInputAnswer{"unknown": {Answers: []string{"A"}}, "text": {Answers: []string{"B"}}}},
		{Action: "answer", Answers: map[string]userInputAnswer{"choice": {Answers: []string{" "}}, "text": {Answers: []string{"B"}}}},
		{Action: "skip", Answers: map[string]userInputAnswer{"text": {Answers: []string{"B"}}}},
		{Action: "reply", Decision: "accept"},
	} {
		cmd.RequestID = json.RawMessage(`1`)
		if err := tracker.reply(cmd, func(any) error { t.Fatal("invalid reply written"); return nil }); err == nil {
			t.Fatalf("accepted invalid input: %+v", cmd)
		}
	}
	if len(tracker.pending) != 1 {
		t.Fatal("invalid input removed request")
	}
}

func TestUserInputPreservesNonBlockingAndLegacyTimeoutMetadata(t *testing.T) {
	tracker := &userInputTracker{thread: "target", pending: map[string]pendingUserInput{}}
	m := message{ID: json.RawMessage(`9`), Method: "item/tool/requestUserInput",
		Params: json.RawMessage(`{"threadId":"target","turnId":"turn","itemId":"ask","isBlocking":false,"autoResolutionMs":1000,"questions":[{"id":"free","question":"Explain?","options":null}]}`)}
	if err := tracker.observe(m); err != nil {
		t.Fatal(err)
	}
	request := tracker.list()[0]
	if request.IsBlocking || request.AutoResolutionMS == nil || *request.AutoResolutionMS != 1000 || request.Questions[0].Options != nil {
		t.Fatalf("lost non-blocking/free-form metadata: %+v", request)
	}
	if err := tracker.reply(interactiveControl{Action: "answer", RequestID: m.ID, Answers: map[string]userInputAnswer{
		"free": {Answers: []string{"自由文本"}},
	}}, func(any) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveUserInputSkipUsesEmptyAnswersAndRejectsStaleReply(t *testing.T) {
	socket := fixture(t, func(ws *websocket.Conn) {
		if err := ws.WriteJSON(questionMessage(`7`, "target", "turn")); err != nil {
			t.Error(err)
			return
		}
		var response message
		if err := ws.ReadJSON(&response); err != nil {
			t.Error(err)
			return
		}
		if string(response.ID) != "7" || string(response.Result) != `{"answers":{}}` {
			t.Errorf("wrong skip response: %+v", response)
		}
		if err := ws.WriteJSON(message{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"target","requestId":7}`)}); err != nil {
			t.Error(err)
			return
		}
		if _, _, err := ws.ReadMessage(); err == nil {
			t.Error("stale reply sent")
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
	stopClose := context.AfterFunc(ctx, func() { c.close(); _ = outR.Close() })
	defer stopClose()
	done := make(chan error, 1)
	go func() {
		done <- watchInteractive(ctx, c, "target", json.NewEncoder(outW), inR, testTurnSnapshot("target", ""))
	}()
	decoder := json.NewDecoder(outR)
	read := func(key, value string) {
		t.Helper()
		var m map[string]json.RawMessage
		if err := decoder.Decode(&m); err != nil {
			t.Fatal(err)
		}
		if string(m[key]) != value {
			t.Fatalf("unexpected interactive output: %v", m)
		}
	}
	read("method", `"item/tool/requestUserInput"`)
	_, _ = io.WriteString(inW, "{\"action\":\"pending-questions\"}\n")
	read("probe", `"pendingQuestions"`)
	_, _ = io.WriteString(inW, "{\"action\":\"skip\",\"requestId\":7}\n")
	read("probe", `"userInputReplySent"`)
	read("method", `"serverRequest/resolved"`)
	_, _ = io.WriteString(inW, "{\"action\":\"skip\",\"requestId\":7}\n")
	read("probe", `"controlRejected"`)
	_, _ = io.WriteString(inW, "{\"action\":\"detach\"}\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("watcher did not detach")
	}
}
