package musecli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Muse has no --mcp-config CLI flag (verified against `muse exec --help`):
// MCP servers come from the user-level settings file
// $XDG_CONFIG_HOME/muse/settings.json, {"schema_version": 1,
// "mcpServers": {"<name>": {"url": "<streamable-http>"}}}. There is no
// workspace-level equivalent. Each launch gets an isolated XDG_CONFIG_HOME
// with its exact bridge and policy; shared user settings remain untouched.

// museSettingsPath resolves the user-level muse settings file the way the
// CLI does: $XDG_CONFIG_HOME wins when set, otherwise ~/.config (NOT
// os.UserConfigDir — on darwin that is ~/Library/Application Support, which
// the CLI does not read; proven live 2026-09-10 when a merge written there
// never reached the session). Tests redirect via XDG.
func museSettingsPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "muse", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for muse settings: %w", err)
	}
	return filepath.Join(home, ".config", "muse", "settings.json"), nil
}

// museApplyMCPConfig merges the "mcpServers" entries of configJSON (if any),
// optionally installs a deny-by-default PreToolUse policy for the supplied
// native-tool allowlist, disables native subagent delegation and workflow
// triggering, and forces tui.voice_enabled to false. It returns a restore
// function that puts the previous settings back byte-exact. Voice input has no
// CLI flag or launch argument (verified against `muse --help`/`muse exec
// --help`) -- settings.json's
// "tui":{"voice_enabled":...} is the only control, so every launch through
// this integration overlays it off regardless of whether an MCP config is
// also mounted: AgentWorks turns are driven by prompt injection, and a
// stray Alt+V (or the CLI's own voice-input hint rendering) has no
// legitimate role in an automated session. Anything already present under a
// colliding server name, or any other existing "tui" field, is preserved
// across the run and reinstated by restore. All file errors abort the
// launch: a half-mounted overlay is worse than no run.
func museApplyMCPConfig(configJSON string, toolAllowlist []string) (func(), error) {
	path, err := museSettingsPath()
	if err != nil {
		return nil, err
	}
	return museApplyMCPConfigAtPath(path, configJSON, toolAllowlist)
}

func museApplyMCPConfigAtPath(path, configJSON string, toolAllowlist []string) (func(), error) {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	configJSON = strings.TrimSpace(configJSON)
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), &doc); err != nil {
			return nil, fmt.Errorf("muse MCP config is not valid JSON: %w", err)
		}
	}
	previous := map[string]json.RawMessage{}
	var previousRaw []byte
	var settings map[string]json.RawMessage
	if raw, readErr := os.ReadFile(path); readErr == nil {
		previousRaw = append([]byte(nil), raw...)
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, fmt.Errorf("existing muse settings.json is not a JSON object: %w", err)
		}
		if settings == nil {
			settings = map[string]json.RawMessage{}
		}
		if rawServers, ok := settings["mcpServers"]; ok {
			if err := json.Unmarshal(rawServers, &previous); err != nil {
				return nil, fmt.Errorf("existing muse settings.json mcpServers is not an object: %w", err)
			}
		}
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("read muse settings.json: %w", readErr)
	} else {
		settings = map[string]json.RawMessage{}
	}
	merged := make(map[string]json.RawMessage, len(previous)+len(doc.MCPServers))
	for name, entry := range previous {
		merged[name] = entry
	}
	for name, entry := range doc.MCPServers {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("muse MCP config has an empty server name")
		}
		merged[name] = entry
	}
	if len(merged) > 0 {
		mergedRaw, err := json.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("marshal merged muse mcpServers: %w", err)
		}
		settings["mcpServers"] = mergedRaw
	}
	if _, ok := settings["schema_version"]; !ok {
		settings["schema_version"] = json.RawMessage("1")
	}
	tui := map[string]json.RawMessage{}
	if rawTUI, ok := settings["tui"]; ok {
		if err := json.Unmarshal(rawTUI, &tui); err != nil {
			return nil, fmt.Errorf("existing muse settings.json tui is not an object: %w", err)
		}
	}
	if tui == nil {
		tui = map[string]json.RawMessage{}
	}
	tui["voice_enabled"] = json.RawMessage("false")
	tuiRaw, err := json.Marshal(tui)
	if err != nil {
		return nil, fmt.Errorf("marshal muse settings.json tui: %w", err)
	}
	settings["tui"] = tuiRaw
	if toolAllowlist != nil {
		if _, err := exec.LookPath("node"); err != nil {
			return nil, fmt.Errorf("Muse native-tool policy requires Node.js: %w", err)
		}
		run := map[string]json.RawMessage{}
		if rawRun, ok := settings["run"]; ok {
			if err := json.Unmarshal(rawRun, &run); err != nil {
				return nil, fmt.Errorf("existing muse settings.json run is not an object: %w", err)
			}
		}
		if run == nil {
			run = map[string]json.RawMessage{}
		}
		cleaned, err := museCleanToolAllowlist(toolAllowlist)
		if err != nil {
			return nil, err
		}
		// run.toolset cannot express this policy: once it is named, Muse also
		// removes mounted MCP tools, while MCP identifiers themselves are rejected
		// as unknown names. Keep the full discovery surface and enforce the exact
		// native policy at execution time with a PreToolUse hook instead.
		delete(run, "toolset")
		run["subagent_delegation_mode"] = json.RawMessage(`"off"`)
		run["workflow_trigger_mode"] = json.RawMessage(`"off"`)
		runRaw, err := json.Marshal(run)
		if err != nil {
			return nil, fmt.Errorf("marshal muse settings.json run: %w", err)
		}
		settings["run"] = runRaw

		hookPath, err := museWriteToolPolicyHook(cleaned)
		if err != nil {
			return nil, err
		}
		hooks := map[string]json.RawMessage{}
		if rawHooks, ok := settings["hooks"]; ok {
			if err := json.Unmarshal(rawHooks, &hooks); err != nil {
				return nil, fmt.Errorf("existing muse settings.json hooks is not an object: %w", err)
			}
		}
		if hooks == nil {
			hooks = map[string]json.RawMessage{}
		}
		var preToolUse []json.RawMessage
		if rawPreToolUse, ok := hooks["PreToolUse"]; ok {
			if err := json.Unmarshal(rawPreToolUse, &preToolUse); err != nil {
				return nil, fmt.Errorf("existing muse settings.json hooks.PreToolUse is not an array: %w", err)
			}
		}
		policyHook, err := json.Marshal(map[string]interface{}{
			"matcher": "*",
			"hooks": []map[string]interface{}{
				{
					"type":    "command",
					"command": "node '" + strings.ReplaceAll(hookPath, "'", "'\\''") + "'",
					"timeout": 5,
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal muse PreToolUse policy hook: %w", err)
		}
		preToolUse = append(preToolUse, policyHook)
		preToolUseRaw, err := json.Marshal(preToolUse)
		if err != nil {
			return nil, fmt.Errorf("marshal muse settings.json hooks.PreToolUse: %w", err)
		}
		hooks["PreToolUse"] = preToolUseRaw
		hooksRaw, err := json.Marshal(hooks)
		if err != nil {
			return nil, fmt.Errorf("marshal muse settings.json hooks: %w", err)
		}
		settings["hooks"] = hooksRaw
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal muse settings.json: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create muse config dir: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write muse settings.json: %w", err)
	}
	// Restore is byte-exact: the pre-run snapshot goes back verbatim (or the
	// file is removed when we created it), so key order, formatting, and
	// trailing bytes survive the round trip. Deliberately unconditional — a
	// leaked mount impersonates user intent to every later CLI run, which is
	// worse than clobbering a concurrent external edit in this window.
	return func() {
		if previousRaw == nil {
			_ = os.Remove(path)
			return
		}
		_ = os.WriteFile(path, previousRaw, 0o600)
	}, nil
}

// musePrepareIsolatedConfig gives one Muse process/session its own config
// root. Muse has no --mcp-config flag, so writing the shared user settings was
// previously the only way to mount a bridge. That breaks as soon as two runs
// overlap: each snapshots the other's temporary overlay and out-of-order
// restores can leave a dead required MCP server in ~/.config/muse forever.
//
// The isolated root copies only the login/trust material needed by the CLI and
// non-ephemeral user preferences. MCP servers and PreToolUse hooks are rebuilt
// exactly for this AgentWorks launch, so a prior crashed run cannot leak tools
// or a localhost test stub into the next one.
func musePrepareIsolatedConfig(configJSON string, toolAllowlist []string) (string, func(), error) {
	sourceSettings, err := museSettingsPath()
	if err != nil {
		return "", nil, err
	}
	root, err := os.MkdirTemp("", "agentworks-muse-config-*")
	if err != nil {
		return "", nil, fmt.Errorf("create isolated muse config: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	targetDir := filepath.Join(root, "muse")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create isolated muse config directory: %w", err)
	}

	sourceDir := filepath.Dir(sourceSettings)
	for _, name := range []string{"auth.json", "trust.json"} {
		if err := copyMuseConfigFile(filepath.Join(sourceDir, name), filepath.Join(targetDir, name)); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	targetSettings := filepath.Join(targetDir, "settings.json")
	if raw, readErr := os.ReadFile(sourceSettings); readErr == nil {
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(raw, &settings); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("existing muse settings.json is not a JSON object: %w", err)
		}
		if settings == nil {
			settings = map[string]json.RawMessage{}
		}
		delete(settings, "mcpServers")
		if rawHooks, ok := settings["hooks"]; ok {
			var hooks map[string]json.RawMessage
			if err := json.Unmarshal(rawHooks, &hooks); err != nil {
				cleanup()
				return "", nil, fmt.Errorf("existing muse settings.json hooks is not an object: %w", err)
			}
			delete(hooks, "PreToolUse")
			if len(hooks) == 0 {
				delete(settings, "hooks")
			} else {
				hooksRaw, _ := json.Marshal(hooks)
				settings["hooks"] = hooksRaw
			}
		}
		seed, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			cleanup()
			return "", nil, fmt.Errorf("prepare isolated muse settings: %w", err)
		}
		if err := os.WriteFile(targetSettings, append(seed, '\n'), 0o600); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("write isolated muse settings: %w", err)
		}
	} else if !os.IsNotExist(readErr) {
		cleanup()
		return "", nil, fmt.Errorf("read muse settings.json: %w", readErr)
	}

	if _, err := museApplyMCPConfigAtPath(targetSettings, configJSON, toolAllowlist); err != nil {
		cleanup()
		return "", nil, err
	}
	return root, cleanup, nil
}

func copyMuseConfigFile(source, target string) error {
	in, err := os.Open(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read muse config credential: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create isolated muse config credential: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy muse config credential: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close isolated muse config credential: %w", err)
	}
	return nil
}

func museEnvironmentWithConfigHome(configHome string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "XDG_CONFIG_HOME" {
			environment = append(environment, entry)
		}
	}
	return append(environment, "XDG_CONFIG_HOME="+configHome)
}

var museToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func museCleanToolAllowlist(toolAllowlist []string) ([]string, error) {
	cleaned := make([]string, 0, len(toolAllowlist))
	seen := make(map[string]bool, len(toolAllowlist))
	for _, name := range toolAllowlist {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("muse tool allowlist has an empty tool name")
		}
		if !museToolNamePattern.MatchString(name) {
			return nil, fmt.Errorf("muse tool allowlist has invalid tool name %q", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		cleaned = append(cleaned, name)
	}
	return cleaned, nil
}

// museWriteToolPolicyHook writes a content-addressed JavaScript hook. The
// AgentWorks server always installs Node with its coding-agent CLIs, and a real
// JSON parser is important here: text extraction could be bypassed by a nested
// tool_name-like string in model-controlled arguments. Muse's
// named run.toolset suppresses MCP tools as well as native tools, so the hook
// is the supported way to keep MCP routing available while denying native
// execution. MCP tools, Muse's MCP discovery gateway, and the runtime-only
// reminder verdict sink are always admitted; the latter cannot access user
// data or the host, and denying it makes Muse's observer retry indefinitely.
// The caller controls the exact native work-tool allowlist (web_search in
// mcpagent).
func museWriteToolPolicyHook(nativeAllowed []string) (string, error) {
	allowed := append([]string(nil), nativeAllowed...)
	allowed = append(allowed, "tool_search", "submit_reminder_decision")
	allowedJSON, err := json.Marshal(allowed)
	if err != nil {
		return "", fmt.Errorf("marshal muse hook allowlist: %w", err)
	}
	body := "const fs = require('fs');\n" +
		"let payload = {};\n" +
		"try { payload = JSON.parse(fs.readFileSync(0, 'utf8') || '{}'); } catch (_) {}\n" +
		"const name = payload && typeof payload.tool_name === 'string' ? payload.tool_name : '';\n" +
		"const allowed = new Set(" + string(allowedJSON) + ");\n" +
		"if (allowed.has(name) || name.startsWith('mcp__')) process.exit(0);\n" +
		"process.stdout.write(JSON.stringify({hookSpecificOutput:{hookEventName:'PreToolUse',permissionDecision:'deny',permissionDecisionReason:'Muse internal tools are disabled for this session; use web search or an AgentWorks MCP tool.'}}) + '\\n');\n"
	digest := sha256.Sum256([]byte(body))
	dir := filepath.Join(os.TempDir(), "muse-cli-hooks")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create muse hook dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("native-tool-policy-%x.js", digest[:8]))
	// Publish atomically: another launch may be executing this same hook.
	// Truncating it in place would briefly turn the policy into an empty script.
	tmp, err := os.CreateTemp(dir, ".native-tool-policy-*")
	if err != nil {
		return "", fmt.Errorf("create muse native-tool policy hook: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write muse native-tool policy hook: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close muse native-tool policy hook: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("publish muse native-tool policy hook: %w", err)
	}
	return path, nil
}
