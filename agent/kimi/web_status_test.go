package kimi

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWebStatus_AttachUsesCurrentServerStatusOverPersistedSnapshot(t *testing.T) {
	f := newWebFixture(t)
	f.mu.Lock()
	f.snapshot.Session.AgentConfig.Model = "persisted-model"
	f.snapshot.Session.Usage.ContextTokens = 1000
	f.snapshot.Session.Usage.ContextLimit = 10000
	f.runtimeStatus = map[string]any{
		"model": "current-model", "thinking_level": "high",
		"context_tokens": 2000, "max_context_tokens": 8000,
	}
	f.mu.Unlock()
	as, err := f.agent.AttachSession(context.Background(), "session_test")
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	s := as.(*webSession)
	u := s.GetContextUsage()
	if s.GetModel() != "current-model" || s.GetReasoningEffort() != "high" || u == nil || u.UsedTokens != 2000 || u.ContextWindow != 8000 {
		t.Fatalf("footer did not use current server state: model=%s effort=%s usage=%+v", s.GetModel(), s.GetReasoningEffort(), u)
	}
}

func TestWebStatus_PartialUpdatesPreserveAndResetUsage(t *testing.T) {
	s := &webSession{}
	for _, raw := range []string{
		`{"model":"server-model","thinkingEffort":"high","contextTokens":200,"maxContextTokens":1000,"usage":{"currentTurn":{"inputOther":10,"output":20,"inputCacheRead":30,"inputCacheCreation":40}}}`,
		`{"permission":"manual","phase":{"kind":"idle"}}`,
	} {
		if err := s.updateStatus(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	u := s.GetContextUsage()
	if s.GetModel() != "server-model" || s.GetReasoningEffort() != "high" || u == nil || u.InputTokens != 10 || u.OutputTokens != 20 || u.ContextWindow != 1000 {
		t.Fatalf("partial update erased footer status: %+v", u)
	}
	if err := s.updateStatus(json.RawMessage(`{"thinkingEffort":"","contextTokens":0,"usage":{"currentTurn":{"inputOther":0,"output":0,"inputCacheRead":0,"inputCacheCreation":0}}}`)); err != nil {
		t.Fatal(err)
	}
	u = s.GetContextUsage()
	if s.GetReasoningEffort() != "" || u.UsedTokens != 0 || u.InputTokens != 0 || u.OutputTokens != 0 || u.CachedInputTokens != 0 || u.CacheCreationInputTokens != 0 || u.ContextWindow != 1000 {
		t.Fatalf("explicit reset did not clear old usage: %+v", u)
	}
}

func TestWebStatus_SnapshotRestoresContextWithoutInventingTurnUsage(t *testing.T) {
	var record webRecord
	if err := json.Unmarshal([]byte(`{"agent_config":{"model":"server-model"},"usage":{"input_tokens":100000,"output_tokens":50000,"context_tokens":500,"context_limit":2000}}`), &record); err != nil {
		t.Fatal(err)
	}
	s := &webSession{}
	s.setSnapshotState(webSnapshot{Session: record})
	u := s.GetContextUsage()
	if s.GetModel() != "server-model" || u == nil || u.UsedTokens != 500 || u.ContextWindow != 2000 || u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Fatalf("snapshot context/cumulative usage mixed up: %+v", u)
	}
}
