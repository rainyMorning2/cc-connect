package core

import (
	"strings"
	"testing"
	"time"
)

// Agent timestamps may retain the UTC zone from an API response. All list
// surfaces must render the deployment's local time, including date rollover.
func TestSessionList_ConvertsUTCTimestampsToLocalTime(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("UTC+8", 8*60*60)
	defer func() { time.Local = previous }()

	sessions := []AgentSessionInfo{{
		ID: "session-utc", Summary: "UTC session", MessageCount: 3,
		ModifiedAt: time.Date(2026, 10, 19, 18, 23, 0, 0, time.UTC),
	}}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubListAgent{sessions: sessions}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	const key = "test:user"
	e.cmdList(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, nil)
	card, err := e.renderListCard(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	deleteCard := e.renderDeleteModeSelectCard(key, e.sessions, &deleteModeState{page: 1}, sessions)
	for surface, text := range map[string]string{
		"plain list":       strings.Join(p.getSent(), "\n"),
		"list card":        card.RenderText(),
		"delete selection": deleteCard.RenderText(),
	} {
		t.Run(surface, func(t *testing.T) {
			if !strings.Contains(text, "10-20 02:23") {
				t.Fatalf("expected UTC+8 timestamp with date rollover, got: %s", text)
			}
		})
	}
}
