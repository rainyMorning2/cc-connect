package kimi

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.SessionPermissionSwitcher = (*webSession)(nil)

func (*webSession) CurrentPermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "manual", NameKey: core.MsgPermManual},
		{Key: "yolo", NameKey: core.MsgPermYolo},
		{Key: "auto", NameKey: core.MsgPermAuto},
	}
}

func (s *webSession) SetCurrentPermissionMode(ctx context.Context, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "manual", "auto", "yolo":
	default:
		return fmt.Errorf("kimi web: unsupported permission mode %q", mode)
	}
	// Match Web: only update this session's main-agent permission mode.
	// The server applies it immediately, including while a turn is running.
	return s.updateProfile(ctx, map[string]string{"permission_mode": mode})
}
