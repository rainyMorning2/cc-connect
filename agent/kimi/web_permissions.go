package kimi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type webApproval struct {
	ID       string          `json:"approval_id"`
	TurnID   int64           `json:"turn_id"`
	ToolID   string          `json:"tool_call_id"`
	ToolName string          `json:"tool_name"`
	Action   string          `json:"action"`
	Display  json.RawMessage `json:"tool_input_display"`
}
type webQuestionItem struct {
	ID         string `json:"id"`
	Question   string `json:"question"`
	Header     string `json:"header"`
	Multi      bool   `json:"multi_select"`
	AllowOther bool   `json:"allow_other"`
	Options    []struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}
type webQuestion struct {
	ID        string            `json:"question_id"`
	TurnID    int64             `json:"turn_id"`
	Questions []webQuestionItem `json:"questions"`
}
type webPending struct {
	Approval *webApproval
	Question *webQuestion
}

func (s *webSession) addApproval(a webApproval) {
	if a.ID == "" {
		return
	}
	id := "approval/" + a.ID
	s.mu.Lock()
	_, exists := s.pending[id]
	s.pending[id] = webPending{Approval: &a}
	s.mu.Unlock()
	if exists {
		return
	}
	s.emit(core.Event{Type: core.EventPermissionRequest, TurnID: s.turnKeys[a.TurnID], ItemID: a.ToolID, RequestID: id, ToolName: a.ToolName, ToolInput: webDisplay(a.Display), Decisions: []string{"allow", "allow_session", "deny", "cancel"}})
}
func (s *webSession) addQuestion(q webQuestion) {
	if q.ID == "" {
		return
	}
	id := "question/" + q.ID
	s.mu.Lock()
	_, exists := s.pending[id]
	s.pending[id] = webPending{Question: &q}
	s.mu.Unlock()
	if exists {
		return
	}
	questions := make([]core.UserQuestion, 0, len(q.Questions))
	for _, item := range q.Questions {
		cq := core.UserQuestion{ID: item.ID, Question: item.Question, Header: item.Header, MultiSelect: item.Multi, IsOther: item.AllowOther}
		for _, o := range item.Options {
			cq.Options = append(cq.Options, core.UserQuestionOption{Label: o.Label, Description: o.Description})
		}
		questions = append(questions, cq)
	}
	s.emit(core.Event{Type: core.EventPermissionRequest, TurnID: s.turnKeys[q.TurnID], RequestID: id, ToolName: "AskUserQuestion", Questions: questions})
}
func (s *webSession) resolvePending(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
	s.emit(core.Event{Type: core.EventPermissionResolved, RequestID: id})
}
func (s *webSession) RespondPermission(id string, result core.PermissionResult) error {
	s.mu.Lock()
	p, ok := s.pending[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("kimi web: interaction already resolved")
	}
	var path string
	var body any
	if p.Approval != nil {
		path = webSessionPath(s.id) + "/approvals/" + url.PathEscape(p.Approval.ID)
		decision := ""
		switch result.Behavior {
		case "allow", "allow_session":
			decision = "approved"
		case "deny":
			decision = "rejected"
		case "cancel":
			decision = "cancelled"
		default:
			return fmt.Errorf("kimi web: unsupported approval decision")
		}
		b := map[string]any{"decision": decision}
		if result.Behavior == "allow_session" {
			b["scope"] = "session"
		}
		if result.Message != "" {
			b["feedback"] = result.Message
		}
		body = b
	} else {
		path = webSessionPath(s.id) + "/questions/" + url.PathEscape(p.Question.ID)
		if result.Behavior != "allow" {
			path += ":dismiss"
		} else {
			answers, err := webAnswers(*p.Question, result.UpdatedInput)
			if err != nil {
				return err
			}
			body = map[string]any{"answers": answers, "method": "click"}
		}
	}
	err := s.agent.api.request(s.ctx, "POST", path, body, nil)
	// Another client may have answered while our card was still visible. The
	// service is authoritative; never retry or let stale input become a prompt.
	var apiErr *webAPIError
	if errors.As(err, &apiErr) && (apiErr.Code == 40902 || apiErr.Code == 40404 || apiErr.Code == 41001 || apiErr.Code == 41002) {
		return nil
	}
	return err
}
func webAnswers(q webQuestion, input map[string]any) (map[string]any, error) {
	raw, ok := input["answers"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("kimi web: missing question answers")
	}
	answers := map[string]any{}
	for _, item := range q.Questions {
		var labels []string
		switch v := raw[item.ID].(type) {
		case []string:
			labels = v
		case []any:
			for _, x := range v {
				if label, ok := x.(string); ok {
					labels = append(labels, label)
				}
			}
		case string:
			labels = []string{v}
		}
		if len(labels) == 0 {
			return nil, fmt.Errorf("kimi web: missing answer for %s", item.ID)
		}
		var ids, other []string
		for _, label := range labels {
			found := ""
			for _, o := range item.Options {
				if o.Label == label || o.ID == label {
					found = o.ID
					break
				}
			}
			if found != "" {
				ids = append(ids, found)
			} else {
				other = append(other, label)
			}
		}
		if len(other) > 0 && !item.AllowOther {
			return nil, fmt.Errorf("kimi web: question %s requires a listed option", item.ID)
		}
		switch {
		case item.Multi && len(other) > 0:
			answers[item.ID] = map[string]any{"kind": "multi_with_other", "option_ids": ids, "other_text": strings.Join(other, "\n")}
		case item.Multi:
			answers[item.ID] = map[string]any{"kind": "multi", "option_ids": ids}
		case len(ids) == 1 && len(other) == 0:
			answers[item.ID] = map[string]any{"kind": "single", "option_id": ids[0]}
		case len(ids) == 0 && len(other) == 1:
			answers[item.ID] = map[string]any{"kind": "other", "text": other[0]}
		default:
			return nil, fmt.Errorf("kimi web: question %s expects a single answer", item.ID)
		}
	}
	return answers, nil
}
