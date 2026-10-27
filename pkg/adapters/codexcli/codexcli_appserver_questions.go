package codexcli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/nativeinput"
	"strings"
)

func codexNativeQuestion(ctx context.Context, method string, raw json.RawMessage, thread, turn string) (any, error) {
	if method == "item/commandExecution/requestApproval" {
		return codexCommandApproval(ctx, raw, thread, turn)
	}
	if method != "item/tool/requestUserInput" {
		return nil, fmt.Errorf("unsupported interactive request %s; use the chat clarification tool", method)
	}
	var req struct {
		ThreadID  string `json:"threadId"`
		TurnID    string `json:"turnId"`
		Questions []struct {
			ID       string               `json:"id"`
			Question string               `json:"question"`
			IsOther  bool                 `json:"isOther"`
			IsSecret bool                 `json:"isSecret"`
			Options  []nativeinput.Option `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.ThreadID != thread || req.TurnID != turn {
		return nil, fmt.Errorf("question belongs to a different turn")
	}
	qs := make([]nativeinput.Question, 0, len(req.Questions))
	for _, q := range req.Questions {
		if q.IsSecret {
			return nil, fmt.Errorf("use the account or Vault setup to enter secrets")
		}
		qs = append(qs, nativeinput.Question{ID: q.ID, Question: q.Question, Options: q.Options, AllowOther: q.IsOther || len(q.Options) == 0})
	}
	answers, err := nativeinput.Ask(ctx, qs)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, a := range answers {
		out[a.ID] = map[string]any{"answers": nativeinput.Values(a)}
	}
	return map[string]any{"answers": out}, nil
}

// PLAT-872: never promote a single command decision into a session/policy grant.
func codexCommandApproval(ctx context.Context, raw json.RawMessage, thread, turn string) (any, error) {
	var req struct {
		ThreadID               string            `json:"threadId"`
		TurnID                 string            `json:"turnId"`
		ItemID                 string            `json:"itemId"`
		Command                string            `json:"command"`
		Cwd                    string            `json:"cwd"`
		Reason                 string            `json:"reason"`
		AvailableDecisions     []json.RawMessage `json:"availableDecisions"`
		NetworkApprovalContext json.RawMessage   `json:"networkApprovalContext"`
		AdditionalPermissions  json.RawMessage   `json:"additionalPermissions"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.ThreadID != thread || req.TurnID != turn || req.ItemID == "" {
		return nil, fmt.Errorf("command approval belongs to a different turn or missing item")
	}
	deny := map[string]any{"decision": "decline"}
	if len(req.AvailableDecisions) > 0 {
		acceptsOnce := false
		for _, decision := range req.AvailableDecisions {
			if string(decision) == `"accept"` {
				acceptsOnce = true
			}
		}
		if !acceptsOnce {
			return deny, nil
		}
	}
	hasNetwork := len(req.NetworkApprovalContext) > 0 && string(req.NetworkApprovalContext) != "null"
	description := "Allow Codex to run this command?\n\n" + req.Command
	if hasNetwork {
		description = "Allow this command's network access?\n\n" + string(req.NetworkApprovalContext) + "\n\n" + req.Command
	}
	if strings.TrimSpace(req.Command) == "" && !hasNetwork {
		return deny, nil
	}
	if req.Cwd != "" {
		description += "\n\nWorking folder: " + req.Cwd
	}
	if req.Reason != "" {
		description += "\n\nReason: " + req.Reason
	}
	if len(req.AdditionalPermissions) > 0 && string(req.AdditionalPermissions) != "null" {
		description += "\n\nRequested access: " + string(req.AdditionalPermissions)
	}
	allowed, err := nativeinput.ApproveCommand(ctx, description)
	if err != nil || !allowed {
		return deny, nil
	}
	return map[string]any{"decision": "accept"}, nil
}
