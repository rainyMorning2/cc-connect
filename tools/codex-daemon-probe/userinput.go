package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type userInputQuestion struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	IsOther  bool   `json:"isOther"`
	IsSecret bool   `json:"isSecret"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

type pendingUserInput struct {
	RequestID        json.RawMessage     `json:"requestId"`
	ThreadID         string              `json:"threadId"`
	TurnID           string              `json:"turnId"`
	ItemID           string              `json:"itemId"`
	Questions        []userInputQuestion `json:"questions"`
	IsBlocking       bool                `json:"isBlocking"`
	AutoResolutionMS *int64              `json:"autoResolutionMs"`
}

type userInputAnswer struct {
	Answers []string `json:"answers"`
}

// Owned by the interactive loop, like approvalTracker. It never answers or
// skips automatically, including non-blocking requests with a legacy timeout.
type userInputTracker struct {
	thread  string
	pending map[string]pendingUserInput
}

func (t *userInputTracker) observe(m message) error {
	switch m.Method {
	case "item/tool/requestUserInput":
		q := pendingUserInput{IsBlocking: true} // Protocol default for older servers.
		if err := json.Unmarshal(m.Params, &q); err != nil {
			return fmt.Errorf("decode user input request: %w", err)
		}
		if q.ThreadID != t.thread {
			return nil
		}
		key, err := approvalID(m.ID)
		if err != nil {
			return err
		}
		ids := map[string]bool{}
		for _, question := range q.Questions {
			if question.ID == "" || ids[question.ID] {
				return fmt.Errorf("user input questions require unique non-empty IDs")
			}
			ids[question.ID] = true
		}
		q.RequestID = append(json.RawMessage(nil), m.ID...)
		t.pending[key] = q
	case "serverRequest/resolved":
		var p struct {
			ThreadID  string          `json:"threadId"`
			RequestID json.RawMessage `json:"requestId"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.ThreadID == t.thread {
			key, err := approvalID(p.RequestID)
			if err != nil {
				return err
			}
			delete(t.pending, key)
		}
	case "turn/completed":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.ThreadID == t.thread {
			for key, q := range t.pending {
				if q.TurnID == p.Turn.ID {
					delete(t.pending, key)
				}
			}
		}
	}
	return nil
}

func (t *userInputTracker) list() []pendingUserInput {
	keys := make([]string, 0, len(t.pending))
	for key := range t.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	requests := make([]pendingUserInput, 0, len(keys))
	for _, key := range keys {
		requests = append(requests, t.pending[key])
	}
	return requests
}

func (t *userInputTracker) reply(cmd interactiveControl, write func(any) error) error {
	key, err := approvalID(cmd.RequestID)
	if err != nil {
		return err
	}
	q, ok := t.pending[key]
	if !ok {
		return fmt.Errorf("question is no longer pending or belongs to another thread")
	}
	if cmd.Decision != "" {
		return fmt.Errorf("user input uses answers, not an approval decision")
	}
	answers := cmd.Answers
	switch cmd.Action {
	case "skip":
		if len(answers) != 0 {
			return fmt.Errorf("skip cannot include answers")
		}
		answers = map[string]userInputAnswer{}
	case "answer":
		if len(answers) != len(q.Questions) || len(answers) == 0 {
			return fmt.Errorf("answer must include every question ID; use skip to submit no answers")
		}
		for _, question := range q.Questions {
			answer, ok := answers[question.ID]
			if !ok || len(answer.Answers) == 0 {
				return fmt.Errorf("missing answer for question %q", question.ID)
			}
			for _, text := range answer.Answers {
				if strings.TrimSpace(text) == "" {
					return fmt.Errorf("empty answer for question %q", question.ID)
				}
			}
		}
	default:
		return fmt.Errorf("user input requires answer or skip")
	}
	if err := write(map[string]any{"id": q.RequestID, "result": map[string]any{"answers": answers}}); err != nil {
		return fmt.Errorf("reply user input: %w", err)
	}
	delete(t.pending, key)
	return nil
}
