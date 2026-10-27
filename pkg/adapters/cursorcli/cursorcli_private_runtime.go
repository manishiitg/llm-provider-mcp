package cursorcli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Cursor uses a private plugin for instructions/skills/hooks and its documented
// user config directory for CLI permissions. The ACP session keeps the real cwd.
// Account files stay in the already-selected private account home; links retain
// its authentication and native records without copying credentials to a project.
func prepareCursorPrivateRuntime(opts *llmtypes.CallOptions, env []string) (string, error) {
	runtimeDir := llmtypes.CLIArtifactDirectory(opts, "")
	values := map[string]string{}
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok {
			values[k] = v
		}
	}
	source := values["CURSOR_CONFIG_DIR"]
	if source == "" {
		source = filepath.Join(values["HOME"], ".cursor")
	}
	if values["CURSOR_CONFIG_DIR"] == "" && runtime.GOOS != "darwin" && values["HOME"] == os.Getenv("HOME") && values["XDG_CONFIG_HOME"] != "" {
		source = filepath.Join(values["XDG_CONFIG_HOME"], "cursor")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	// Native records created by Cursor also belong to the selected account.
	// Keep each account's generated config separate, including newly-created
	// stores that have no corresponding link in its account home yet.
	accountKey := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	configDir := filepath.Join(runtimeDir, "cursor-accounts", accountKey)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", err
	}
	config := map[string]any{"version": 1, "editor": map[string]any{"vimMode": false}}
	if raw, err := os.ReadFile(filepath.Join(source, "cli-config.json")); err == nil {
		if err = json.Unmarshal(raw, &config); err != nil {
			return "", fmt.Errorf("read Cursor account configuration: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if config == nil {
		return "", fmt.Errorf("Cursor account configuration must be an object")
	}
	// This directory belongs only to the generated runtime. Clear every old
	// account link before projecting the selected account, including names that
	// are absent from the new account. Remove symlinks, never their targets.
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() == "cli-config.json" {
			continue
		}
		target := filepath.Join(configDir, entry.Name())
		info, err := os.Lstat(target)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if err := os.Remove(target); err != nil {
			return "", err
		}
	}
	if entries, err := os.ReadDir(source); err == nil {
		for _, entry := range entries {
			if entry.Name() == "cli-config.json" || entry.Name() == "hooks.json" {
				continue
			}
			target := filepath.Join(configDir, entry.Name())
			if _, err := os.Lstat(target); os.IsNotExist(err) {
				if err := os.Symlink(filepath.Join(source, entry.Name()), target); err != nil {
					return "", err
				}
			} else if err != nil {
				return "", err
			}
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(runtimeDir, ".cursor", "cli.json"))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read generated Cursor policy: %w", err)
	}
	generated := map[string]any{}
	if err == nil {
		if err = json.Unmarshal(raw, &generated); err != nil {
			return "", err
		}
	}
	permissions, _ := config["permissions"].(map[string]any)
	if permissions == nil {
		permissions = map[string]any{}
	}
	if projected, ok := generated["permissions"].(map[string]any); ok {
		for _, key := range []string{"allow", "deny"} {
			prior, _ := permissions[key].([]any)
			additions, _ := projected[key].([]any)
			permissions[key] = append(prior, additions...)
		}
	}
	config["permissions"] = permissions
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(configDir, "cli-config.json"), encoded, 0600); err != nil {
		return "", err
	}
	manifest := map[string]any{"name": "agentworks-runtime", "rules": ".cursor/rules", "skills": ".cursor/skills"}
	hooksPath := filepath.Join(runtimeDir, ".cursor", "hooks.json")
	if raw, err := os.ReadFile(hooksPath); err == nil {
		// Existing hook commands are project-relative. Plugin hooks must point to
		// their private scripts when the actual session cwd is the person's folder.
		var hooks map[string]any
		if err = json.Unmarshal(raw, &hooks); err != nil {
			return "", err
		}
		if groups, ok := hooks["hooks"].(map[string]any); ok {
			for _, group := range groups {
				if entries, ok := group.([]any); ok {
					for _, entry := range entries {
						if hook, ok := entry.(map[string]any); ok {
							if command, ok := hook["command"].(string); ok && strings.HasPrefix(command, "./.cursor/") {
								hook["command"] = filepath.Join(runtimeDir, strings.TrimPrefix(command, "./"))
							}
						}
					}
				}
			}
		}
		encoded, err = json.Marshal(hooks)
		if err != nil {
			return "", err
		}
		if err = os.WriteFile(hooksPath, encoded, 0600); err != nil {
			return "", err
		}
		manifest["hooks"] = ".cursor/hooks.json"
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err = os.MkdirAll(filepath.Join(runtimeDir, ".cursor-plugin"), 0700); err != nil {
		return "", err
	}
	encoded, err = json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(runtimeDir, ".cursor-plugin", "plugin.json"), encoded, 0600); err != nil {
		return "", err
	}
	// Transcript readers follow the active account, while old records remain
	// in that account's directory and cannot be reused by the next account.
	current := filepath.Join(runtimeDir, "cursor-config")
	if info, err := os.Lstat(current); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("private Cursor config pointer must be a link")
		}
		if err := os.Remove(current); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Symlink(configDir, current); err != nil {
		return "", err
	}
	return configDir, nil
}
