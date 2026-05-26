package agycli

import (
	"path/filepath"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/skillproject"
)

// agySkillsSubdir is the cross-provider .agents/ convention. Agy CLI
// reads skills from this location alongside its other .agents/ files
// (mlp-system.md rules, mcp_config.json, hooks.json).
const agySkillsSubdir = ".agents/skills"

// ProjectSkills writes each attached skill into <workdir>/.agents/skills/<name>/
// as Anthropic-format SKILL.md plus any supporting files.
//
// Idempotent: safe to call at both session launch and session resume.
func (a *AgyCLIAdapter) ProjectSkills(workdir string, skills []*llmtypes.Skill) error {
	if workdir == "" || len(skills) == 0 {
		return nil
	}
	return skillproject.Write(filepath.Join(workdir, agySkillsSubdir), skills)
}
