package musecli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Muse has no --mcp-config CLI flag (verified against `muse exec --help`):
// MCP servers come from the user-level settings file
// $XDG_CONFIG_HOME/muse/settings.json, {"schema_version": 1,
// "mcpServers": {"<name>": {"url": "<streamable-http>"}}}. There is no
// workspace-level equivalent, so the exec lane merges the caller's bridge
// servers into that file for the duration of one run and restores the
// previous content afterwards (merge, don't clobber).

// museSettingsPath resolves the user-level muse settings file the way the
// CLI does: $XDG_CONFIG_HOME wins when set (os.UserConfigDir ignores it on
// darwin), otherwise the platform config dir. Tests redirect via XDG.
func museSettingsPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "muse", "settings.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir for muse settings: %w", err)
	}
	return filepath.Join(dir, "muse", "settings.json"), nil
}

// museApplyMCPConfig merges the "mcpServers" entries of configJSON into the
// user-level muse settings.json and returns a restore function that puts the
// previous settings back. Empty documents are a no-op returning a nil restore.
// Anything already present under a colliding server name is preserved across
// the run and reinstated by restore. All file errors abort the launch: a
// half-mounted bridge is worse than no run.
func museApplyMCPConfig(configJSON string) (func(), error) {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(configJSON), &doc); err != nil {
		return nil, fmt.Errorf("muse MCP config is not valid JSON: %w", err)
	}
	if len(doc.MCPServers) == 0 {
		return nil, nil
	}
	path, err := museSettingsPath()
	if err != nil {
		return nil, err
	}
	previous := map[string]json.RawMessage{}
	existed := false
	hadServersKey := false
	var settings map[string]json.RawMessage
	if raw, readErr := os.ReadFile(path); readErr == nil {
		existed = true
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, fmt.Errorf("existing muse settings.json is not a JSON object: %w", err)
		}
		if settings == nil {
			settings = map[string]json.RawMessage{}
		}
		if rawServers, ok := settings["mcpServers"]; ok {
			hadServersKey = true
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
	mergedRaw, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal merged muse mcpServers: %w", err)
	}
	settings["mcpServers"] = mergedRaw
	if _, ok := settings["schema_version"]; !ok {
		settings["schema_version"] = json.RawMessage("1")
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
	return func() {
		if !existed {
			// We created the file: remove it. If the run itself added other
			// keys, they go with it — the file did not exist before us.
			_ = os.Remove(path)
			return
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return
		}
		var current map[string]json.RawMessage
		if err := json.Unmarshal(raw, &current); err != nil || current == nil {
			// Someone else rewrote the file mid-run or it is corrupt now;
			// restoring blindly would clobber that, so leave it alone.
			return
		}
		if !hadServersKey {
			delete(current, "mcpServers")
		} else {
			restored, err := json.Marshal(previous)
			if err != nil {
				return
			}
			current["mcpServers"] = restored
		}
		restoredOut, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			return
		}
		_ = os.WriteFile(path, append(restoredOut, '\n'), 0o600)
	}, nil
}
