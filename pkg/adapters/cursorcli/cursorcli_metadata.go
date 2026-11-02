package cursorcli

import (
	"fmt"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func cursorMetaString(opts *llmtypes.CallOptions, key string) string {
	if opts.Metadata == nil {
		return ""
	}
	v, _ := opts.Metadata.Custom[key].(string)
	return strings.TrimSpace(v)
}

func cursorMetaBool(opts *llmtypes.CallOptions, key string) bool {
	if opts.Metadata == nil {
		return false
	}
	v, _ := opts.Metadata.Custom[key].(bool)
	return v
}

func cursorPlanFailure(text, model string) error {
	// Cursor emits this standalone account refusal as a normal message chunk.
	// Do not classify ordinary explanations or quoted discussions of plans.
	if strings.TrimSpace(text) != "Upgrade your plan to continue" {
		return nil
	}
	return &llmerrors.Error{Kind: llmerrors.KindBilling, Provider: "cursor-cli", Model: model,
		Err: fmt.Errorf("This Cursor account needs a plan upgrade to continue. Check its plan or select another Cursor account.")}
}
