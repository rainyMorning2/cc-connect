package core

import (
	"reflect"
	"testing"
)

func TestSharedQuestions_MultiSelectWaitsForSubmitAndPreservesAnswers(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	as := env.a.connection("first")
	as.emit(Event{Type: EventPermissionRequest, RequestID: "multi", Questions: []UserQuestion{
		{ID: "q", Question: "SELECT MULTIPLE", MultiSelect: true, IsOther: true, Options: []UserQuestionOption{{Label: "A"}, {Label: "B"}}},
		{ID: "next", Question: "NEXT QUESTION", IsOther: true},
	}})
	env.await("SELECT MULTIPLE")
	oldButton := env.button("A")
	env.send(oldButton)
	env.await("Selected: A")
	env.send(env.button("B"))
	env.await("Selected: A, B")
	env.send(env.button("☑ A"))
	env.send("custom answer")
	env.await("Selected: B, custom answer")
	as.mu.Lock()
	count := len(as.responses)
	as.mu.Unlock()
	if count != 0 {
		t.Fatal("multi-select submitted before confirmation")
	}
	env.send(env.button(env.e.i18n.T(MsgSharedSubmitAnswers)))
	env.await("NEXT QUESTION")
	env.send(oldButton)
	env.await(env.e.i18n.T(MsgSharedStaleRequest))
	env.send("free text")
	env.await(env.e.i18n.T(MsgSharedResponseSent))
	as.mu.Lock()
	defer as.mu.Unlock()
	if len(as.responses) != 1 {
		t.Fatalf("responses=%v", as.responses)
	}
	want := map[string]any{"q": []string{"B", "custom answer"}, "next": []string{"free text"}}
	if !reflect.DeepEqual(as.responses[0].UpdatedInput["answers"], want) {
		t.Fatalf("answers=%v", as.responses[0])
	}
}

func TestSharedQuestions_MultiSelectNumericBatchValidatesAtomically(t *testing.T) {
	e := NewEngine("test", &sharedTestAgent{}, nil, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	p := &pendingPermission{}
	q := UserQuestion{MultiSelect: true, Options: []UserQuestionOption{{Label: "A"}, {Label: "B"}}}
	if ready, err := e.collectSharedMultiAnswer(p, 0, q, "done"); ready || err == nil {
		t.Fatal("empty answer submitted")
	}
	if _, err := e.collectSharedMultiAnswer(p, 0, q, "1,3"); err == nil || len(p.MultiAnswers[0]) != 0 {
		t.Fatal("invalid batch partially applied")
	}
	if ready, err := e.collectSharedMultiAnswer(p, 0, q, "1,2,1"); ready || err != nil {
		t.Fatal(ready, err)
	}
	if !reflect.DeepEqual(p.MultiAnswers[0], []string{"A", "B"}) {
		t.Fatal(p.MultiAnswers)
	}
	if ready, err := e.collectSharedMultiAnswer(p, 0, q, "done"); !ready || err != nil {
		t.Fatal(ready, err)
	}
}

type localizedCallbackPlatform struct {
	stubPlatformEngine
	translator *I18n
}

func (p *localizedCallbackPlatform) SetI18n(i *I18n) { p.translator = i }

func TestPlatformCallbacks_ReceiveLiveEngineLanguage(t *testing.T) {
	env := newSharedTestEnv(t)
	p := &localizedCallbackPlatform{}
	env.e.initPlatformCapabilities(p)
	if p.translator != env.e.i18n {
		t.Fatal("platform callback language not connected to engine")
	}
	env.e.i18n.SetLang(LangJapanese)
	if p.translator.T(MsgActionProcessing) != "⏳ 処理中…" {
		t.Fatal("platform callback language did not update")
	}
}
