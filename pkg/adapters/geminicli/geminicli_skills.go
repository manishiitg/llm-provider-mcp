package geminicli

import (
	"path/filepath"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/skillproject"
)

// geminiSkillsSubdir uses the .agents/ cross-provider convention.
// Gemini CLI also reads .gemini/skills/ natively; we pick .agents/skills/
// so the same projection works whether the agent later switches to
// another CLI in the same workspace.
const geminiSkillsSubdir = ".agents/skills"

// ProjectSkills writes each attached skill into <workdir>/.agents/skills/<name>/
// as Anthropic-format SKILL.md plus any supporting files. Gemini CLI's
// Agent Skills loader picks them up automatically.
//
// Idempotent: safe to call at both session launch and session resume.
func (a *GeminiCLIAdapter) ProjectSkills(workdir string, skills []*llmtypes.Skill) error {
	if workdir == "" || len(skills) == 0 {
		return nil
	}
	return skillproject.Write(filepath.Join(workdir, geminiSkillsSubdir), skills)
}
