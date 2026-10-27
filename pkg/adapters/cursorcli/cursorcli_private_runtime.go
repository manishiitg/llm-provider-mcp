package cursorcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Cursor uses a private plugin for instructions/skills/hooks and its documented
// user config directory for CLI permissions. The ACP session keeps the real cwd.
// Account files stay in the already-selected private account home; links retain
// its authentication and native records without copying credentials to a project.
func prepareCursorPrivateRuntime(opts *llmtypes.CallOptions, env []string) (string, error) {
	runtimeDir := llmtypes.CLIArtifactDirectory(opts, "")
	configDir := filepath.Join(runtimeDir, "cursor-config")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", err
	}
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
	if values["CURSOR_CONFIG_DIR"] == "" && values["XDG_CONFIG_HOME"] != "" {
		source = filepath.Join(values["XDG_CONFIG_HOME"], "cursor")
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
	if entries, err := os.ReadDir(source); err == nil {
		for _, entry := range entries {
			if entry.Name() == "cli-config.json" || entry.Name() == "hooks.json" {
				continue
			}
			target := filepath.Join(configDir, entry.Name())
			if _, err := os.Lstat(target); os.IsNotExist(err) {
				if err = os.Symlink(filepath.Join(source, entry.Name()), target); err != nil {
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
	if err != nil {
		return "", fmt.Errorf("read generated Cursor policy: %w", err)
	}
	var generated map[string]any
	if err = json.Unmarshal(raw, &generated); err != nil {
		return "", err
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
	return configDir, nil
}
