package skillproject

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestWriteBasicSkill(t *testing.T) {
	dir := t.TempDir()
	err := Write(dir, []*llmtypes.Skill{{
		Name:        "agent-browser",
		Description: "Drive a browser",
		Content:     "# Agent Browser\n\nUse browser_* tools.\n",
	}})
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "agent-browser", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"---\n",
		"name: agent-browser\n",
		"description: Drive a browser\n",
		"# Agent Browser",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestWriteWithSupportingFiles(t *testing.T) {
	dir := t.TempDir()
	err := Write(dir, []*llmtypes.Skill{{
		Name:    "pdf-extract",
		Content: "body",
		SupportingFiles: []llmtypes.SkillFile{
			{RelPath: "scripts/extract.py", Content: []byte("print('hi')\n")},
			{RelPath: "references/api.md", Content: []byte("# API\n")},
		},
	}})
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	for _, rel := range []string{"scripts/extract.py", "references/api.md"} {
		if _, err := os.Stat(filepath.Join(dir, "pdf-extract", rel)); err != nil {
			t.Errorf("missing supporting file %s: %v", rel, err)
		}
	}
}

func TestWriteIdempotent(t *testing.T) {
	dir := t.TempDir()
	skill := &llmtypes.Skill{Name: "idem", Content: "x"}
	for i := 0; i < 3; i++ {
		if err := Write(dir, []*llmtypes.Skill{skill}); err != nil {
			t.Fatalf("Write iteration %d: %v", i, err)
		}
	}
	body, _ := os.ReadFile(filepath.Join(dir, "idem", "SKILL.md"))
	if !strings.Contains(string(body), "name: idem") {
		t.Errorf("expected name: idem, got %q", string(body))
	}
}

func TestWriteRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	err := Write(dir, []*llmtypes.Skill{{
		Name:    "evil",
		Content: "x",
		SupportingFiles: []llmtypes.SkillFile{
			{RelPath: "../../etc/passwd", Content: []byte("nope")},
			{RelPath: "/abs/path", Content: []byte("nope")},
		},
	}})
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	// SKILL.md should exist, traversal targets should not.
	if _, err := os.Stat(filepath.Join(dir, "evil", "SKILL.md")); err != nil {
		t.Errorf("expected SKILL.md to be written: %v", err)
	}
	if _, err := os.Stat("/etc/passwd-skillproject-test-leak"); !os.IsNotExist(err) {
		t.Errorf("unexpected leak outside tempdir")
	}
}

func TestWriteSanitizesFolderName(t *testing.T) {
	dir := t.TempDir()
	err := Write(dir, []*llmtypes.Skill{{
		Name:    "My Skill/With Spaces",
		Content: "x",
	}})
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	// Sanitization lowercases and replaces spaces/slashes with hyphens.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 folder, got %d", len(entries))
	}
	got := entries[0].Name()
	if got != "my-skill-with-spaces" {
		t.Errorf("expected my-skill-with-spaces, got %q", got)
	}
}

func TestWriteSkipsEmpty(t *testing.T) {
	if err := Write(t.TempDir(), nil); err != nil {
		t.Errorf("Write(nil): %v", err)
	}
	if err := Write(t.TempDir(), []*llmtypes.Skill{}); err != nil {
		t.Errorf("Write(empty): %v", err)
	}
	if err := Write(t.TempDir(), []*llmtypes.Skill{nil, {Name: ""}}); err != nil {
		t.Errorf("Write(nil entries): %v", err)
	}
}
