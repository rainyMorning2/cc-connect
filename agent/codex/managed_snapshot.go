package codex

import (
	"context"
	"fmt"
)

// Resume only metadata, then hydrate bounded pages. A full resume response can
// exceed the transport limit even when every individual turn fits. Keep the
// chronological order used by active-turn and subagent reconciliation.
func (s *managedSession) resumeSnapshot(rpc *daemonRPC, id string) (managedSnapshot, error) {
	var snapshot managedSnapshot
	if err := rpc.request(s.ctx, "thread/resume", map[string]any{"threadId": id, "excludeTurns": true}, &snapshot); err != nil {
		return snapshot, err
	}
	if err := s.validateSnapshot(snapshot, id); err != nil {
		return snapshot, err
	}
	// Tolerate older servers which still populate turns despite excludeTurns.
	if len(snapshot.Thread.Turns) == 0 {
		turns, err := readManagedSnapshotTurns(s.ctx, rpc, id, snapshot.TurnsBackwardsCursor)
		if err != nil {
			return snapshot, err
		}
		snapshot.Thread.Turns = turns
	}
	return snapshot, nil
}

func (s *managedSession) validateSnapshot(snapshot managedSnapshot, expected string) error {
	cwd := snapshot.Cwd
	if cwd == "" {
		cwd = snapshot.Thread.Cwd
	}
	if snapshot.Thread.ID == "" || (expected != "" && snapshot.Thread.ID != expected) {
		return fmt.Errorf("snapshot returned a different or empty thread ID")
	}
	if !sameManagedCwd(s.agent.GetWorkDir(), cwd) {
		return fmt.Errorf("thread is outside configured work_dir")
	}
	if managedSubagent(snapshot.Thread.Source) {
		return fmt.Errorf("only root threads can be attached")
	}
	return nil
}

func readManagedSnapshotTurns(ctx context.Context, rpc *daemonRPC, id, cursor string) ([]managedTurn, error) {
	var turns []managedTurn
	seen := map[string]bool{}
	for {
		var page struct {
			Data       []managedTurn `json:"data"`
			NextCursor *string       `json:"nextCursor"`
		}
		// One turn per page avoids aggregating many large tool outputs in a frame.
		// A single oversized turn still obeys the transport's documented read limit.
		params := map[string]any{"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "full"}
		if cursor != "" {
			params["cursor"] = cursor
			seen[cursor] = true
		}
		if err := rpc.request(ctx, "thread/turns/list", params, &page); err != nil {
			return nil, fmt.Errorf("codex managed snapshot page: %w", err)
		}
		turns = append(turns, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("codex managed snapshot pagination repeated cursor")
		}
	}
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	return turns, nil
}
