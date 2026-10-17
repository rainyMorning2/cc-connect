package core

import "testing"

func TestParseBusyMessageMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts map[string]any
		want string
		fail bool
	}{
		{"default", nil, "queue", false},
		{"queue", map[string]any{"busy_message_mode": "queue"}, "queue", false},
		{"steer", map[string]any{"busy_message_mode": "steer"}, "steer", false},
		{"normalized", map[string]any{"busy_message_mode": " STEER "}, "steer", false},
		{"empty", map[string]any{"busy_message_mode": ""}, "", true},
		{"unknown", map[string]any{"busy_message_mode": "auto"}, "", true},
		{"wrong_type", map[string]any{"busy_message_mode": true}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseBusyMessageMode(tc.opts)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("got %q, %v; want %q, error=%t", got, err, tc.want, tc.fail)
			}
		})
	}
}
