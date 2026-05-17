package geminicli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func appendGeminiPolicyArgs(args *[]string, opts *llmtypes.CallOptions) {
	if args == nil || opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return
	}
	if policyPath, ok := opts.Metadata.Custom[MetadataKeyPolicyPath].(string); ok && strings.TrimSpace(policyPath) != "" {
		*args = append(*args, "--policy", strings.TrimSpace(policyPath))
	}
	if adminPolicyPath, ok := opts.Metadata.Custom[MetadataKeyAdminPolicyPath].(string); ok && strings.TrimSpace(adminPolicyPath) != "" {
		*args = append(*args, "--admin-policy", strings.TrimSpace(adminPolicyPath))
	}
}
