package core

import (
	"fmt"
	"strings"
)

// ParseBusyMessageMode reads the common shared-session input policy. Agent
// adapters are responsible for rejecting this option on unsupported backends.
func ParseBusyMessageMode(opts map[string]any) (string, error) {
	value, exists := opts["busy_message_mode"]
	if !exists {
		return "queue", nil
	}
	mode, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("busy_message_mode must be queue or steer")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "queue" && mode != "steer" {
		return "", fmt.Errorf("busy_message_mode must be queue or steer")
	}
	return mode, nil
}
