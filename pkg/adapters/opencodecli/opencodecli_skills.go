package opencodecli

import (
	"path/filepath"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/skillproject"
)

// opencodeSkillsSubdir uses the .agents/ cross-provider convention.
// OpenCode also reads .opencode/skills/ and .claude/skills/ for interop,
// but .agents/skills/ is the universal path that every native skill-aware
// CLI in this codebase honors.
const opencodeSkillsSubdir = ".agents/skills"

// ProjectSkills writes each attached skill into <workdir>/.agents/skills/<name>/
// as Anthropic-format SKILL.md plus any supporting files. OpenCode's
// skill loader picks them up automatically.
//
// Idempotent: safe to call at both session launch and session resume.
func (a *OpenCodeCLIAdapter) ProjectSkills(workdir string, skills []*llmtypes.Skill) error {
	if workdir == "" || len(skills) == 0 {
		return nil
	}
	return skillproject.Write(filepath.Join(workdir, opencodeSkillsSubdir), skills)
}
