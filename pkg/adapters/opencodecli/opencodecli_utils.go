package opencodecli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func opencodeWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if dir, ok := opts.Metadata.Custom[MetadataKeyWorkingDir].(string); ok {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			return filepath.Clean(trimmed)
		}
	}
	return ""
}

func opencodeBinaryPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("OPENCODE_BIN")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("OPENCODE_BIN points to a missing or invalid executable: %s", configured)
	}
	if path, err := exec.LookPath("opencode"); err == nil {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		for _, candidate := range []string{
			filepath.Join(home, ".opencode", "bin", "opencode"),
			filepath.Join(home, ".cache", "opencode", "bin", "opencode"),
		} {
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("opencode not found in PATH. Install OpenCode CLI or set OPENCODE_BIN to the opencode executable")
}

func opencodeRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func writeOpenCodeRestoredFile(path string, content []byte, restorePrior bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create OpenCode config dir: %w", err)
	}
	var previous []byte
	existed := false
	if restorePrior {
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			previous, existed = data, true
		} else if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("failed to read existing OpenCode config %s: %w", path, readErr)
		}
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write OpenCode config %s: %w", path, err)
	}
	return func() {
		if existed {
			_ = os.WriteFile(path, previous, 0o600)
		} else {
			_ = os.Remove(path)
		}
	}, nil
}

// opencodeBuiltinToolsToDeny lists the built-in tools we disable when the
// caller opts into MCP-only routing via WithWriteProjectInstructionFile.
// Per opencode.ai/docs/tools, these are the documented built-ins that
// access the filesystem, shell, or network — i.e. the capabilities we
// want the model to reach via MCP servers instead.
//
// Note "apply_patch" (not "patch") matches opencode's actual tool ID per
// docs: "check input.tool === 'apply_patch' (not 'patch')".
//
// "task" is the (undocumented but functional) subagent-spawn tool. A
// real e2e run against opencode 1.15.4 showed the model routing around
// a denied `read` by spawning a `task` subagent — so without denying
// `task` too, the deny block has a hole. The subagent inherits its
// parent's denied tools so the file still couldn't be read in that
// run, but allowing `task` lets the model burn the response budget on
// futile subagent attempts. Include it in the deny set to keep the
// model from looping.
//
// "skill" loads on-disk skill markdown files — also filesystem access.
var opencodeBuiltinToolsToDeny = []string{
	"read",
	"write",
	"edit",
	"bash",
	"grep",
	"glob",
	"lsp",
	"apply_patch",
	"webfetch",
	"websearch",
	"task",
	"skill",
}

// buildOpenCodeProjectConfigJSON builds the opencode.jsonc body, merging
// optional MCP server config (standard {"mcpServers":{...}} input) and an
// optional tools-deny block. Either input may be empty; the resulting
// JSON contains only the sections that were requested.
//
// The "tools" mechanism replaced an earlier JS-plugin approach: opencode
// supports `{"tools": {"read": false, ...}}` natively per
// opencode.ai/docs/config, which is simpler and more reliable than
// dropping a deny-builtin.js plugin into .opencode/plugins/.
func buildOpenCodeProjectConfigJSON(mcpServersJSON string, denyBuiltins bool) ([]byte, error) {
	output := map[string]interface{}{}

	if strings.TrimSpace(mcpServersJSON) != "" {
		var input map[string]interface{}
		if err := json.Unmarshal([]byte(mcpServersJSON), &input); err != nil {
			return nil, fmt.Errorf("invalid MCP config JSON: %w", err)
		}

		servers, ok := input["mcpServers"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("MCP config JSON missing mcpServers object")
		}

		mcpSection := make(map[string]interface{})
		for name, serverRaw := range servers {
			server, ok := serverRaw.(map[string]interface{})
			if !ok {
				continue
			}
			if _, has := server["type"]; !has {
				server["type"] = "local"
			}
			if _, has := server["enabled"]; !has {
				server["enabled"] = true
			}
			// Schema translation: callers use the canonical MCP shape
			// {"command":"<exe>","args":[<rest>]} (Claude Desktop /
			// cline / mcpServers convention). Opencode 1.15.4 expects
			// {"command":["<exe>",<rest>...]} — a single array. Merge
			// them so the MCP server actually launches. Without this,
			// opencode silently presents an empty MCP tool list to the
			// model and the entire bridge is invisible.
			//
			// Authoritative shape from opencode binary's embedded
			// docs: `"command": ["npx", "-y", "@playwright/mcp"]`.
			if cmdStr, ok := server["command"].(string); ok {
				combined := []interface{}{cmdStr}
				if argsArr, ok := server["args"].([]interface{}); ok {
					combined = append(combined, argsArr...)
				}
				server["command"] = combined
				delete(server, "args")
			}
			mcpSection[name] = server
		}
		output["mcp"] = mcpSection
	}

	if denyBuiltins {
		toolsSection := make(map[string]interface{}, len(opencodeBuiltinToolsToDeny))
		for _, tool := range opencodeBuiltinToolsToDeny {
			toolsSection[tool] = false
		}
		output["tools"] = toolsSection
	}

	return json.MarshalIndent(output, "", "  ")
}
