package core

import (
	"strings"
	"testing"
)

func TestSubagentNotice_LocalizedStatusesAndBoundedSummary(t *testing.T) {
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		e := &Engine{i18n: NewI18n(lang)}
		for status, key := range map[string]MsgKey{"queued": MsgSubagentQueued, "running": MsgSubagentRunning, "suspended": MsgSubagentSuspended, "completed": MsgSubagentCompleted, "failed": MsgSubagentFailed, "cancelled": MsgSubagentCancelled} {
			text := e.subagentNoticeText(SubagentNotice{ID: "id", Name: "reviewer", TaskID: "task", Status: status, Description: "review changes", Detail: strings.Repeat("界", 5000)})
			if !strings.Contains(text, e.i18n.T(key)) || e.i18n.T(key) == string(key) {
				t.Fatalf("missing %s translation in %s: %s", status, lang, text)
			}
			if strings.Contains(text, strings.Repeat("界", 4001)) || !strings.HasSuffix(text, "…") {
				t.Fatal("child summary was not bounded safely")
			}
		}
	}
}
