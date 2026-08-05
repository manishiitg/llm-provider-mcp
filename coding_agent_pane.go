package llmproviders

import (
	"strings"

	claudecodeadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/codexcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/picli"
)

// CodingAgentPaneReady reports whether a coding-agent TUI screen is at its
// idle input composer. The provider adapters remain the canonical owners of
// prompt, approval, queued-input, and active-status recognition; host runtimes
// should not duplicate those provider-specific rules.
func CodingAgentPaneReady(provider Provider, captured string) bool {
	provider = Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	switch provider {
	case ProviderClaudeCode:
		return claudecodeadapter.PaneReadyForInput(captured)
	case ProviderCodexCLI:
		return codexcli.PaneReadyForInput(captured)
	case ProviderCursorCLI:
		return cursorcli.PaneReadyForInput(captured)
	case ProviderPiCLI:
		return picli.PaneReadyForInput(captured)
	default:
		return false
	}
}
