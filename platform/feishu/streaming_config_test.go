package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

func TestStreamingConfigFromTOML(t *testing.T) {
	var config struct {
		Options map[string]any `toml:"options"`
	}
	_, err := toml.Decode(`[options]
app_id = "cli_test"
app_secret = "secret"
print_frequency_ms = 20
print_step = 2
print_strategy = "delay"
`, &config)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"feishu", "lark"} {
		t.Run(name, func(t *testing.T) {
			platform, err := newPlatform(name, "https://example.invalid", config.Options)
			if err != nil {
				t.Fatal(err)
			}
			var card struct {
				Config struct {
					StreamingConfig struct {
						Frequency map[string]int `json:"print_frequency_ms"`
						Step      map[string]int `json:"print_step"`
						Strategy  string         `json:"print_strategy"`
					} `json:"streaming_config"`
				} `json:"config"`
			}
			data := platform.(core.RichCardSupporter).BuildRichCard(core.CardStatusWorking, "", nil, "body", true, "")
			if err := json.Unmarshal([]byte(data), &card); err != nil {
				t.Fatal(err)
			}
			sc := card.Config.StreamingConfig
			if sc.Frequency["default"] != 20 || sc.Step["default"] != 2 || sc.Strategy != "delay" {
				t.Fatalf("TOML options not preserved: %s", data)
			}
		})
	}
}

func TestStreamingConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
		valid bool
	}{
		{"print_frequency_ms", int64(20), true}, {"print_frequency_ms", 1000, true},
		{"print_frequency_ms", 19, false}, {"print_frequency_ms", 1001, false},
		{"print_frequency_ms", 20.5, false}, {"print_frequency_ms", "20", false},
		{"print_step", int64(2), true}, {"print_step", 1000, true},
		{"print_step", 0, false}, {"print_step", 1001, false}, {"print_step", true, false},
		{"print_strategy", " FAST ", true}, {"print_strategy", "delay", true},
		{"print_strategy", "", false}, {"print_strategy", "other", false}, {"print_strategy", 1, false},
	} {
		opts := map[string]any{"app_id": "cli_test", "app_secret": "secret", tc.key: tc.value}
		_, err := New(opts)
		if (err == nil) != tc.valid {
			t.Fatalf("%s=%v: valid=%v, err=%v", tc.key, tc.value, tc.valid, err)
		}
		if err != nil && !strings.Contains(err.Error(), tc.key) {
			t.Fatalf("missing field in error: %v", err)
		}
	}
}

func TestStreamingConfigRendering(t *testing.T) {
	for _, configured := range []bool{false, true} {
		opts := map[string]any{"app_id": "cli_test", "app_secret": "secret"}
		if configured {
			opts["print_frequency_ms"] = int64(20)
			opts["print_step"] = int64(2)
			opts["print_strategy"] = " FAST "
		}
		platform, err := New(opts)
		if err != nil {
			t.Fatal(err)
		}
		p := extractBasePlatform(platform)
		for _, streaming := range []bool{true, false} {
			for _, compact := range []bool{false, true} {
				var steps []core.ToolStep
				if compact {
					for i := 0; i < 80; i++ {
						steps = append(steps, core.ToolStep{Kind: core.ToolStepKindTool, Name: "Bash", Result: strings.Repeat("long output ", 100)})
					}
				}
				data := p.BuildRichCard(core.CardStatusWorking, "", steps, "body", streaming, "")
				var card map[string]any
				if err := json.Unmarshal([]byte(data), &card); err != nil {
					t.Fatal(err)
				}
				cfg := card["config"].(map[string]any)
				actual, exists := cfg["streaming_config"]
				if exists != (configured && streaming) {
					t.Fatalf("unexpected config: %s", data)
				}
				if exists {
					sc := actual.(map[string]any)
					if sc["print_frequency_ms"].(map[string]any)["default"] != float64(20) || sc["print_step"].(map[string]any)["default"] != float64(2) || sc["print_strategy"] != "fast" {
						t.Fatalf("wrong config: %v", sc)
					}
				}
			}
		}
	}
}

func TestStreamingConfigPartialOverride(t *testing.T) {
	platform, err := New(map[string]any{"app_id": "cli_test", "app_secret": "secret", "print_step": int64(3)})
	if err != nil {
		t.Fatal(err)
	}
	p := extractBasePlatform(platform)
	if len(p.streamingConfig) != 1 {
		t.Fatalf("unspecified defaults overridden: %v", p.streamingConfig)
	}
	if _, ok := p.streamingConfig["print_step"]; !ok {
		t.Fatal("print_step missing")
	}
}
