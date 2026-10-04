package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestRichCardTypewriterPreservesWorkingPanels(t *testing.T) {
	p := &Platform{}
	for _, working := range []bool{true, false} {
		status := core.CardStatusWorking
		if !working {
			status = core.CardStatusDone
		}
		data := p.BuildRichCard(status, "", []core.ToolStep{{Kind: core.ToolStepKindTool, Name: "Bash", Result: "TOOL OUTPUT", Done: !working}}, "BODY INCREMENT", working, "FOOTER")
		var card map[string]any
		if err := json.Unmarshal([]byte(data), &card); err != nil {
			t.Fatal(err)
		}
		if got := card["config"].(map[string]any)["streaming_mode"]; got != working {
			t.Fatalf("wrong animation flag: %s", data)
		}
		elements := card["body"].(map[string]any)["elements"].([]any)
		if elements[0].(map[string]any)["expanded"] != working {
			t.Fatalf("animation changed tool panel state: %s", data)
		}
		if !strings.Contains(data, "BODY INCREMENT") || !strings.Contains(data, "TOOL OUTPUT") || !strings.Contains(data, "FOOTER") {
			t.Fatal("card lost content")
		}
	}
}

func TestRichCardTypewriterSurvivesCardSizeCompaction(t *testing.T) {
	p := &Platform{}
	var steps []core.ToolStep
	for i := 0; i < 80; i++ {
		steps = append(steps, core.ToolStep{Kind: core.ToolStepKindTool, Name: "Bash", Result: strings.Repeat("LONG OUTPUT ", 100)})
	}
	data := p.BuildRichCard(core.CardStatusWorking, "", steps, "BODY", true, "")
	var card map[string]any
	if err := json.Unmarshal([]byte(data), &card); err != nil {
		t.Fatal(err)
	}
	if card["config"].(map[string]any)["streaming_mode"] != true {
		t.Fatal("compaction disabled typewriter")
	}
	if !strings.Contains(data, "BODY") {
		t.Fatal("compaction lost body")
	}
}
