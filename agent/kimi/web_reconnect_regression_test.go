package kimi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestWebReconnect_RejectedSubscriptionClosesWithoutPanic(t *testing.T) {
	f := newWebFixture(t)
	as, err := f.agent.AttachSession(context.Background(), "session_test")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rejectSubscribe = true
	for c := range f.peers {
		_ = c.Close()
	}
	f.mu.Unlock()
	timeout := time.NewTimer(12 * time.Second)
	defer timeout.Stop()
	exhausted := false
	for {
		select {
		case e, ok := <-as.Events():
			if !ok {
				if !exhausted || as.Alive() {
					t.Fatal("observer did not report terminal reconnect failure")
				}
				return
			}
			if e.Type == core.EventError && strings.Contains(e.Error.Error(), "reconnect exhausted") {
				exhausted = true
			}
		case <-timeout.C:
			t.Fatal("observer did not close")
		}
	}
}

func TestWebQuestion_PreservesOtherAnswerAndMultiSelect(t *testing.T) {
	s := &webSession{ctx: context.Background(), events: make(chan core.Event, 1), pending: map[string]webPending{}}
	q := webQuestion{ID: "q", Questions: []webQuestionItem{{ID: "a", AllowOther: true, Multi: true}}}
	s.addQuestion(q)
	e := <-s.events
	if !e.Questions[0].IsOther || !e.Questions[0].MultiSelect {
		t.Fatalf("lost question capabilities: %+v", e.Questions)
	}
	answers, err := webAnswers(q, map[string]any{"answers": map[string]any{"a": []string{"custom answer"}}})
	if err != nil || answers["a"].(map[string]any)["kind"] != "multi_with_other" {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
}
