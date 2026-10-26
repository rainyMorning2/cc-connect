package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSharedRelayQuestionsAreAnswerableFromSourceChat(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	done := make(chan error, 1)
	go func() {
		response, err := env.e.HandleRelay(context.Background(), "other", "test:user", "RELAY QUESTION")
		if err == nil && response != "RELAY RESULT" {
			err = fmt.Errorf("wrong relay response %q", response)
		}
		done <- err
	}()
	call := awaitSharedCompatCall(t, a)
	call.session.emit(Event{Type: EventPermissionRequest, TurnID: call.turn, RequestID: "relay-question", Questions: []UserQuestion{{ID: "q", Question: "Choose for relay", Options: []UserQuestionOption{{Label: "A"}}}}})
	env.await("Choose for relay")
	env.send(env.button("A"))
	call.session.mu.Lock()
	responses := append([]PermissionResult(nil), call.session.responses...)
	call.session.mu.Unlock()
	if len(responses) != 1 {
		t.Fatalf("source reply did not reach relay: %v", responses)
	}
	call.finish("RELAY RESULT")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay stuck")
	}
	if strings.Contains(env.visible(), "RELAY RESULT") {
		t.Fatal("relay sent duplicate final result instead of returning it")
	}
}

func TestSharedRelayTimeoutReturnsWithoutEventsAndPreservesPendingReader(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := env.e.HandleRelay(ctx, "other", "test:user", "RELAY TIMEOUT"); done <- err }()
	call := awaitSharedCompatCall(t, a)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("timeout: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no-event relay failed to honor cancellation")
	}
	call.session.emit(Event{Type: EventPermissionRequest, TurnID: call.turn, RequestID: "after-timeout", ToolInput: "LATE APPROVAL", Decisions: []string{"cancel"}})
	env.await("LATE APPROVAL")
	env.send(env.button(env.e.i18n.T(MsgSharedCancelDecision)))
	call.finish("LATE RESULT")
}

func TestSharedRelayTimeoutExcludesToolOutput(t *testing.T) {
	for _, partial := range []string{"", "Assistant progress"} {
		t.Run(partial, func(t *testing.T) {
			e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
			t.Cleanup(func() {
				if err := e.Stop(); err != nil {
					t.Error(err)
				}
			})
			session := e.sessions.GetOrCreateActive("relay")
			f := &sharedForeground{events: make(chan Event)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cleaned := make(chan struct{})
			type result struct {
				text string
				err  error
			}
			done := make(chan result, 1)
			go func() {
				text, err := e.collectSharedRelay(ctx, nil, session, e.sessions, f, nil, func() { close(cleaned) })
				done <- result{text, err}
			}()
			f.events <- Event{Type: EventToolResult, ToolName: "Bash", ToolResult: "INTERNAL TOOL OUTPUT"}
			if partial != "" {
				f.events <- Event{Type: EventText, Content: partial}
			}
			cancel()
			got := <-done
			if strings.Contains(got.text, "INTERNAL TOOL OUTPUT") {
				t.Errorf("tool output leaked: %q", got.text)
			}
			if partial == "" {
				if got.text != "" || got.err != context.Canceled {
					t.Errorf("got %q, %v; want cancellation", got.text, got.err)
				}
			} else if got.err != nil || got.text != fmt.Sprintf(e.i18n.T(MsgRelayPartial), partial) {
				t.Errorf("got %q, %v; want localized partial notice", got.text, got.err)
			}
			f.events <- Event{Type: EventResult}
			select {
			case <-cleaned:
			case <-time.After(time.Second):
				t.Fatal("background relay did not finish")
			}
		})
	}
}
