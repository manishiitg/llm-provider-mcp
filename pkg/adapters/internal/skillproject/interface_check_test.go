package skillproject_test

// This test exists only to assert at compile time that every CLI
// adapter implements the projection contract. If an adapter is renamed
// or its ProjectSkills signature drifts, this file fails to compile
// instead of silently breaking the launch path at runtime.
//
// The contract is duplicated locally (not imported from mcpagent) to
// avoid pulling mcpagent into a test in this repo, which would create
// the import cycle the type-in-llmtypes split was designed to prevent.

import (
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/agycli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/codexcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/geminicli"
)

type skillProjector interface {
	ProjectSkills(workdir string, skills []*llmtypes.Skill) error
}

var (
	_ skillProjector = (*claudecode.ClaudeCodeAdapter)(nil)
	_ skillProjector = (*cursorcli.CursorCLIAdapter)(nil)
	_ skillProjector = (*agycli.AgyCLIAdapter)(nil)
	_ skillProjector = (*geminicli.GeminiCLIAdapter)(nil)
	_ skillProjector = (*codexcli.CodexCLIAdapter)(nil)
)
