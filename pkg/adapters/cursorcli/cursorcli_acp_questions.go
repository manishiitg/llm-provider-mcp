package cursorcli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/nativeinput"
	"strings"
)

func cursorNativeQuestion(ctx context.Context, method string, raw json.RawMessage, native string, approveWeb ...bool) (any, error) {
	cancelled := map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	switch method {
	case "cursor/ask_question":
		var req struct {
			SessionID string `json:"sessionId"`
			Questions []struct {
				ID      string `json:"id"`
				Prompt  string `json:"prompt"`
				Options []struct {
					ID    string `json:"id"`
					Label string `json:"label"`
				} `json:"options"`
				AllowMultiple bool `json:"allowMultiple"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if req.SessionID != "" && req.SessionID != native {
			return cancelled, nil
		}
		qs := []nativeinput.Question{}
		for _, q := range req.Questions {
			mapped := nativeinput.Question{ID: q.ID, Question: q.Prompt, MultiSelect: q.AllowMultiple, Options: []nativeinput.Option{}}
			for _, o := range q.Options {
				mapped.Options = append(mapped.Options, nativeinput.Option{Label: o.Label})
			}
			qs = append(qs, mapped)
		}
		answers, err := nativeinput.Ask(ctx, qs)
		if err != nil {
			return cancelled, nil
		}
		out := []any{}
		for i, a := range answers {
			ids := []string{}
			for _, label := range a.SelectedLabels {
				for _, o := range req.Questions[i].Options {
					if label == o.Label {
						ids = append(ids, o.ID)
					}
				}
			}
			out = append(out, map[string]any{"questionId": a.ID, "selectedOptionIds": ids})
		}
		return map[string]any{"outcome": map[string]any{"outcome": "answered", "answers": out}}, nil
	case "cursor/create_plan":
		var req struct {
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
			Overview  string `json:"overview"`
			Plan      string `json:"plan"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if req.SessionID != "" && req.SessionID != native {
			return cancelled, nil
		}
		text := strings.TrimSpace(req.Name+"\n\n"+req.Overview+"\n\n"+req.Plan) + "\n\nProceed with this plan?"
		answers, err := nativeinput.Ask(ctx, []nativeinput.Question{{ID: "plan", Question: text, Options: []nativeinput.Option{{Label: "Approve plan"}, {Label: "Reject plan"}}}})
		if err != nil {
			return cancelled, nil
		}
		outcome := "rejected"
		if len(answers[0].SelectedLabels) == 1 && answers[0].SelectedLabels[0] == "Approve plan" {
			outcome = "accepted"
		}
		return map[string]any{"outcome": map[string]any{"outcome": outcome}}, nil
	case "session/request_permission":
		var req struct {
			SessionID string `json:"sessionId"`
			ToolCall  struct {
				Title      string `json:"title"`
				ToolCallID string `json:"toolCallId"`
				Kind       string `json:"kind"`
				Content    []struct {
					Content struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"content"`
				RawInput json.RawMessage `json:"rawInput"`
			} `json:"toolCall"`
			Options []struct {
				OptionID  string `json:"optionId"`
				SessionID string `json:"sessionId"`
				Name      string `json:"name"`
				Kind      string `json:"kind"`
			} `json:"options"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if req.SessionID != native {
			return cancelled, nil
		}
		// Preserve the explicit SearchWeb API's existing opt-in, confined to
		// native web requests. This does not approve shells, edits or plans.
		web := (req.ToolCall.Kind == "search" && strings.HasPrefix(req.ToolCall.ToolCallID, "web_search_")) || (req.ToolCall.Kind == "fetch" && strings.HasPrefix(req.ToolCall.ToolCallID, "web_fetch_"))
		if len(approveWeb) > 0 && approveWeb[0] && web {
			for _, o := range req.Options {
				if o.Kind == "allow_once" {
					return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}, nil
				}
			}
		}
		q := nativeinput.Question{ID: "permission", Question: "Cursor requests permission: " + req.ToolCall.Title, Options: []nativeinput.Option{}}
		for _, c := range req.ToolCall.Content {
			if c.Content.Type == "text" {
				q.Question += "\n\n" + c.Content.Text
			}
		}
		if len(req.ToolCall.RawInput) > 0 {
			q.Question += "\n\n" + string(req.ToolCall.RawInput)
		}
		// Only this operation's decision is offered. Permanent grants are not made
		// by the adapter; CLI config and existing policy continue to own permissions.
		ids := map[string]string{}
		for _, o := range req.Options {
			if o.Kind == "allow_once" || o.Kind == "reject_once" {
				q.Options = append(q.Options, nativeinput.Option{Label: o.Name})
				ids[o.Name] = o.OptionID
			}
		}
		a, err := nativeinput.Ask(ctx, []nativeinput.Question{q})
		if err != nil {
			return cancelled, nil
		}
		if len(a[0].SelectedLabels) != 1 {
			return cancelled, nil
		}
		id, ok := ids[a[0].SelectedLabels[0]]
		if !ok {
			return cancelled, nil
		}
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": id}}, nil
	default:
		return nil, fmt.Errorf("unsupported Cursor ACP request %s", method)
	}
}
