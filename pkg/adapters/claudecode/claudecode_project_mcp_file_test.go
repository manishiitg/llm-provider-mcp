package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteClaudeCodeProjectMCPFileLifecycleNoPriorContent covers the
// fresh-workspace case: no .mcp.json exists, writer creates one with
// the operator-supplied JSON, removeFiles cleans it up because the
// path is NOT registered in the byte-restore map.
func TestWriteClaudeCodeProjectMCPFileLifecycleNoPriorContent(t *testing.T) {
	tmp := t.TempDir()
	mcpJSON := `{"mcpServers":{"api-bridge":{"command":"/usr/local/bin/mcpbridge","env":{"MCP_API_URL":"http://localhost:9000"}}}}`

	path, err := writeClaudeCodeProjectMCPFile(tmp, mcpJSON)
	if err != nil {
		t.Fatalf("writeClaudeCodeProjectMCPFile: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path with non-empty workingDir")
	}
	expected := filepath.Join(tmp, ".mcp.json")
	if path != expected {
		t.Errorf("path %q must equal expected %q", path, expected)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	if string(body) != mcpJSON {
		t.Errorf(".mcp.json must contain the operator-supplied JSON verbatim\n  want: %s\n  got:  %s", mcpJSON, body)
	}

	if _, registered := claudeProjectFileRestores.Load(path); registered {
		t.Error("path must NOT be registered in restore map when no prior file existed; removeFiles should plain-delete it")
	}

	removeFiles([]string{path})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("removeFiles must delete the .mcp.json we created from nothing; stat err=%v", err)
	}
}

// TestWriteClaudeCodeProjectMCPFileRestoresOperatorContent guards the
// promise the option's doc comment makes: if the operator already had
// .mcp.json, removeFiles must restore it byte-for-byte by way of the
// side-channel restore map, instead of just deleting the file.
func TestWriteClaudeCodeProjectMCPFileRestoresOperatorContent(t *testing.T) {
	tmp := t.TempDir()
	operatorContent := []byte(`{"mcpServers":{"legacy":{"command":"/opt/legacy-mcp"}}}`)
	path := filepath.Join(tmp, ".mcp.json")
	if err := os.WriteFile(path, operatorContent, 0o600); err != nil {
		t.Fatalf("seed pre-existing .mcp.json: %v", err)
	}

	sessionJSON := `{"mcpServers":{"session":{"command":"/tmp/session-mcp"}}}`
	if _, err := writeClaudeCodeProjectMCPFile(tmp, sessionJSON); err != nil {
		t.Fatalf("writeClaudeCodeProjectMCPFile with pre-existing .mcp.json: %v", err)
	}

	mid, _ := os.ReadFile(path)
	if string(mid) != sessionJSON {
		t.Errorf("mid-session, .mcp.json must contain the session JSON\n  want: %s\n  got:  %s", sessionJSON, mid)
	}
	if _, registered := claudeProjectFileRestores.Load(path); !registered {
		t.Error("path MUST be registered in restore map when a prior file existed; otherwise removeFiles will plain-delete operator content")
	}

	removeFiles([]string{path})
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("after removeFiles, .mcp.json should exist (restored): %v", err)
	}
	if string(restored) != string(operatorContent) {
		t.Errorf("removeFiles must restore pre-existing .mcp.json byte-for-byte\n  want: %s\n  got:  %s", operatorContent, restored)
	}
	if _, stillRegistered := claudeProjectFileRestores.Load(path); stillRegistered {
		t.Error("restore map entry must be deleted after removeFiles consumes it (single-use)")
	}
}

// TestWriteClaudeCodeProjectMCPFileEmptyInputsNoOp guards against the
// adapter accidentally writing .mcp.json into the orchestrator's own
// cwd (or writing an empty JSON document) when the caller forgot to
// set MetadataKeyWorkingDir or MetadataKeyMCPConfig.
func TestWriteClaudeCodeProjectMCPFileEmptyInputsNoOp(t *testing.T) {
	path, err := writeClaudeCodeProjectMCPFile("", "anything")
	if err != nil {
		t.Errorf("empty workingDir should return nil error; got %v", err)
	}
	if path != "" {
		t.Errorf("empty workingDir should return empty path; got %q", path)
	}
	if _, err := os.Stat(".mcp.json"); err == nil {
		t.Errorf(".mcp.json must NOT be created in process cwd when workingDir is empty")
		_ = os.Remove(".mcp.json")
	}

	tmp := t.TempDir()
	path, err = writeClaudeCodeProjectMCPFile(tmp, "   ")
	if err != nil {
		t.Errorf("blank mcpJSON should return nil error; got %v", err)
	}
	if path != "" {
		t.Errorf("blank mcpJSON should return empty path; got %q", path)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".mcp.json")); err == nil {
		t.Errorf(".mcp.json must NOT be created when mcpJSON is blank")
	}
}
