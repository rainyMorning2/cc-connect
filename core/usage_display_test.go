package core

import (
	"strings"
	"testing"
	"time"
)

func TestFormatUsageReport_ReadableResetTimeAndLayout(t *testing.T) {
	reset := time.Date(2026, 10, 10, 14, 30, 0, 0, time.Local)
	report := &UsageReport{Plan: "plus", Buckets: []UsageBucket{{Windows: []UsageWindow{
		{WindowSeconds: 18000, UsedPercent: 23, ResetAfterSeconds: 6665, ResetAtUnix: reset.Unix()},
		{WindowSeconds: 604800, UsedPercent: 42, ResetAfterSeconds: 512698, ResetAtUnix: reset.Add(5 * 24 * time.Hour).Unix()},
	}}}}
	for _, tt := range []struct {
		lang                   Language
		heading, countdown, at string
	}{
		{LangEnglish, "5h limit · Remaining: 77%", "Resets: in 1h 51m", "At: "},
		{LangChinese, "5小时限额 · 剩余：77%", "重置：1小时 51分钟后", "时间："},
		{LangTraditionalChinese, "5小時限額 · 剩餘：77%", "重置：1小時 51分鐘後", "時間："},
		{LangJapanese, "5時間枠 · 残り: 77%", "リセット: 1時間 51分後", "日時: "},
		{LangSpanish, "Límite 5h · restante: 77%", "Reinicio: en 1h 51m", "Fecha: "},
	} {
		t.Run(string(tt.lang), func(t *testing.T) {
			got := formatUsageReport(report, tt.lang)
			want := tt.heading + "\n" + tt.countdown + "\n" + tt.at + "2026-10-10 14:30 " + reset.Format("(UTC-07:00)")
			if !strings.Contains(got, want) {
				t.Fatalf("usage = %q, want consecutive lines %q", got, want)
			}
			if !strings.Contains(got, "\n\n"+usageWindowLabel(tt.lang, 604800)+" · ") {
				t.Fatalf("missing separation between quota windows: %q", got)
			}
		})
	}
}

func TestFormatUsageResetTime_SoonDueAndUnknown(t *testing.T) {
	for _, tt := range []struct {
		name   string
		window UsageWindow
		want   string
	}{
		{"under a minute", UsageWindow{ResetAfterSeconds: 30}, "Resets: within 1 minute"},
		{"one minute", UsageWindow{ResetAfterSeconds: 60}, "Resets: in 1m"},
		{"unknown", UsageWindow{}, "Resets: -"},
		{"unknown negative", UsageWindow{ResetAfterSeconds: -1}, "Resets: -"},
		{"due", UsageWindow{ResetAtUnix: 1}, "Resets: due now"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUsageBlock(LangEnglish, &tt.window)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("usage = %q, want %q", got, tt.want)
			}
			if tt.window.ResetAtUnix == 0 && strings.Contains(got, "At:") {
				t.Fatalf("invented reset timestamp: %q", got)
			}
		})
	}
}

func TestRenderUsageCard_SeparatesAndEmphasizesQuotaWindows(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	report := &UsageReport{Plan: "plus", Buckets: []UsageBucket{{Windows: []UsageWindow{
		{WindowSeconds: 18000, UsedPercent: 23, ResetAfterSeconds: 6665},
		{WindowSeconds: 604800, UsedPercent: 42, ResetAfterSeconds: 512698},
	}}}}
	card := e.renderUsageCard(report)
	if len(card.Elements) != 6 {
		t.Fatalf("elements = %d, want account, two separated windows, buttons", len(card.Elements))
	}
	for i, want := range map[int]string{2: "**5h limit · Remaining: 77%**\n", 4: "**7d limit · Remaining: 58%**\n"} {
		if _, ok := card.Elements[i-1].(CardDivider); !ok {
			t.Fatalf("element %d = %T, want divider", i-1, card.Elements[i-1])
		}
		md, ok := card.Elements[i].(CardMarkdown)
		if !ok || !strings.HasPrefix(md.Content, want) {
			t.Fatalf("element %d = %+v, want heading %q", i, card.Elements[i], want)
		}
	}
}

func TestCmdUsage_ShowsAdditionalQuotaAndResetExpiry(t *testing.T) {
	reset := time.Date(2026, 10, 11, 2, 30, 0, 0, time.Local)
	expires := reset.AddDate(0, 0, 10)
	report := &UsageReport{
		Plan: "plus",
		Buckets: []UsageBucket{
			{Name: "main", Windows: []UsageWindow{
				{WindowSeconds: 18000, UsedPercent: 8, ResetAfterSeconds: 3600},
				{WindowSeconds: 604800, UsedPercent: 32, ResetAfterSeconds: 86400},
			}},
			{Name: "gpt-reserve", Windows: []UsageWindow{
				{WindowSeconds: 604800, UsedPercent: 36, ResetAfterSeconds: 172800, ResetAtUnix: reset.Unix()},
			}},
		},
		ResetCredits: &UsageResetCredits{AvailableCount: 2, Credits: []UsageResetCredit{
			{ExpiresAtUnix: expires.Unix()}, {ExpiresAtUnix: expires.AddDate(0, 0, 7).Unix()},
		}},
	}
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		for _, card := range []bool{false, true} {
			name := string(lang) + "/text"
			if card {
				name = string(lang) + "/card"
			}
			t.Run(name, func(t *testing.T) {
				plain := &stubPlatformEngine{n: "test"}
				rich := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
				var p Platform = plain
				if card {
					p = rich
				}
				e := NewEngine("test", &stubUsageAgent{report: report}, []Platform{p}, "", lang)
				e.handleCommand(p, &Message{SessionKey: "test:user", ReplyCtx: "ctx"}, "/usage")
				var got string
				if card {
					if len(rich.repliedCards) != 1 {
						t.Fatalf("cards = %d, want 1", len(rich.repliedCards))
					}
					for _, element := range rich.repliedCards[0].Elements {
						if md, ok := element.(CardMarkdown); ok {
							got += md.Content + "\n"
						}
					}
				} else {
					if len(plain.sent) != 1 {
						t.Fatalf("messages = %d, want 1", len(plain.sent))
					}
					got = plain.sent[0]
				}
				for _, want := range []string{
					usageWindowLabel(lang, 18000),
					"gpt-reserve · " + usageWindowLabel(lang, 604800) + " · " + usageRemainingLabel(lang) + usageColon(lang) + "64%",
					formatUsageResetTime(lang, 172800),
					"2026-10-11 02:30 " + reset.Format("(UTC-07:00)"),
					NewI18n(lang).Tf(MsgUsageResetsAvailable, 2),
					"1. " + NewI18n(lang).Tf(MsgUsageResetExpires, "2026-10-21 02:30 "+expires.Format("(UTC-07:00)")),
					"2. " + NewI18n(lang).Tf(MsgUsageResetExpires, "2026-10-28 02:30 "+expires.AddDate(0, 0, 7).Format("(UTC-07:00)")),
				} {
					if !strings.Contains(got, want) {
						t.Fatalf("usage = %q, want %q", got, want)
					}
				}
			})
		}
	}
}

func TestFormatUsageReport_ResetCreditsMissingZeroAndUnknownExpiry(t *testing.T) {
	report := &UsageReport{}
	if got := formatUsageReport(report, LangEnglish); strings.Contains(got, "Available quota resets") {
		t.Fatalf("invented reset credits: %q", got)
	}
	report.ResetCredits = &UsageResetCredits{}
	if got := formatUsageReport(report, LangEnglish); !strings.Contains(got, "Available quota resets: 0") {
		t.Fatalf("missing zero available resets: %q", got)
	}
	report.ResetCredits = &UsageResetCredits{AvailableCount: 1, Credits: []UsageResetCredit{{}}}
	if got := formatUsageReport(report, LangEnglish); !strings.Contains(got, "1. Expires: -") || strings.Contains(got, "1970") {
		t.Fatalf("incorrect unknown expiry: %q", got)
	}
}
