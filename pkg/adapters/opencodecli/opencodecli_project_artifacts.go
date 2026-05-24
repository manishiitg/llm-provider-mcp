package opencodecli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeOpenCodeDenyBuiltinPlugin installs an opencode plugin that
// blocks the built-in tools (read, write, edit, bash, etc.) by
// throwing in the tool.execute.before hook, forcing the model to
// route through MCP servers instead.
//
// Per opencode.ai/docs/plugins, hooks cannot be declared from
// configuration alone — they require a JS/TS module under
// .opencode/plugins/ (or ~/.config/opencode/plugins/) that exports a
// plugin function. opencode auto-loads anything in those directories,
// so dropping the file is enough — no opencode.jsonc reference is
// needed.
//
// The returned cleanup byte-restores any pre-existing operator plugin
// at the same path, or removes the file we created. The .opencode and
// .opencode/plugins directories are removed only if we created them
// AND they're empty at cleanup time (best-effort).
//
// Risk caveat: deny-builtin.js is a single-file convention. If the
// orchestrator process crashes between write and cleanup AND the
// operator had their own deny-builtin.js, theirs is destroyed.
// Off-by-default (gated on WithWriteProjectInstructionFile) keeps the
// blast radius bounded.
func writeOpenCodeDenyBuiltinPlugin(workingDir string) (func(), error) {
	noop := func() {}
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return noop, nil
	}

	opencodeDir := filepath.Join(workingDir, ".opencode")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		return noop, fmt.Errorf("create .opencode dir: %w", err)
	}
	opencodeDirCreatedByUs := opencodeDirIsEmptyOrJustCreated(opencodeDir)

	pluginsDir := filepath.Join(opencodeDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return noop, fmt.Errorf("create .opencode/plugins dir: %w", err)
	}
	pluginsDirCreatedByUs := opencodeDirIsEmptyOrJustCreated(pluginsDir)

	pluginPath := filepath.Join(pluginsDir, "deny-builtin.js")
	priorPlugin, pluginExisted, err := opencodeReadPriorFileForRestore(pluginPath)
	if err != nil {
		return noop, fmt.Errorf("read pre-existing deny-builtin.js: %w", err)
	}

	pluginBody := opencodeDenyBuiltinPluginSource()
	if err := os.WriteFile(pluginPath, []byte(pluginBody), 0o600); err != nil {
		return noop, fmt.Errorf("write deny-builtin.js: %w", err)
	}

	return func() {
		if pluginExisted {
			_ = os.WriteFile(pluginPath, priorPlugin, 0o600)
		} else {
			_ = os.Remove(pluginPath)
		}
		if pluginsDirCreatedByUs {
			_ = os.Remove(pluginsDir)
		}
		if opencodeDirCreatedByUs {
			_ = os.Remove(opencodeDir)
		}
	}, nil
}

// opencodeDenyBuiltinPluginSource emits the JS plugin body. Per
// opencode.ai/docs/plugins the plugin module exports a function that
// returns a record of hook implementations; tool.execute.before is the
// canonical event for pre-execution interception, and throwing an
// Error from it causes opencode to abort the tool call (matching the
// `.env` example in the docs).
//
// The denied tool list covers opencode's documented built-in tool
// names (read, write, edit, bash, grep, glob, list, patch, webfetch,
// task). MCP server tools have provider-prefixed names
// (e.g. "mcp__api-bridge__fetch") that don't appear in this list, so
// they execute normally.
func opencodeDenyBuiltinPluginSource() string {
	return `// mlp-session: orchestrator-generated opencode plugin that denies
// built-in tool calls (read, write, edit, bash, etc.), forcing the
// model to route through MCP servers instead. Auto-removed at session
// cleanup; any pre-existing operator deny-builtin.js is byte-restored.
//
// Plugin shape follows opencode.ai/docs/plugins: a default export
// returning a record of hook implementations. The tool.execute.before
// hook throws on built-in tool names; MCP tools have provider-prefixed
// names (mcp__<server>__<tool>) and are not matched.

const BUILTIN_TOOLS = new Set([
  "read",
  "write",
  "edit",
  "bash",
  "grep",
  "glob",
  "list",
  "patch",
  "webfetch",
  "task",
]);

export default async function denyBuiltinPlugin() {
  return {
    "tool.execute.before": async (input) => {
      const tool = (input && input.tool) || "";
      if (BUILTIN_TOOLS.has(tool)) {
        throw new Error(
          "Built-in tool '" + tool + "' is disabled by orchestrator policy; use MCP servers instead.",
        );
      }
    },
  };
}
`
}

func opencodeReadPriorFileForRestore(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func opencodeDirIsEmptyOrJustCreated(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) == 0
}
