package codexcli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/nativeinput"
)

func codexNativeQuestion(ctx context.Context, method string, raw json.RawMessage, thread, turn string) (any, error) {
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
