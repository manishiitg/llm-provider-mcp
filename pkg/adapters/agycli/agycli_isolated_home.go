package agycli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// agyIsolatedHome gives one run its own MCP catalogue and permission list.
// The catalogue contains bridge credentials, so every file is private and
// credentials never appear in agy mcp add argv or another user's settings.
// Conversations are shared with the normal AGY home for durable transcript
// reads and native --conversation resume.
func agyIsolatedHome(servers []agyMCPServer, workingDirs ...string) (string, func(), error) {
	base, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	agyLegacySweepOnce.Do(func() {
		_ = agySweepLegacyGlobalMounts(base)
		agySweepStalePrivateHomes()
	})
	home, err := os.MkdirTemp("", fmt.Sprintf("agentworks-agy-%d-", os.Getpid()))
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(home) }
	if err := os.Chmod(home, 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	baseGemini := filepath.Join(base, ".gemini")
	privateGemini := filepath.Join(home, ".gemini")
	// Copy account metadata and settings, but never the global MCP catalogue.
	// AGY may keep login metadata in either config or antigravity-cli.
	for _, subdir := range []string{"config", "antigravity-cli"} {
		if err := agyCopyHomeTree(filepath.Join(baseGemini, subdir), filepath.Join(privateGemini, subdir), map[string]bool{
			"mcp_config.json": true, "mcp": true, "conversations": true,
			"crashes": true, "updater": true, "bin": true,
		}); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	privateRuntime := filepath.Join(privateGemini, "antigravity-cli")
	if err := os.MkdirAll(privateRuntime, 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	baseConversations := filepath.Join(baseGemini, "antigravity-cli", "conversations")
	if err := os.MkdirAll(baseConversations, 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.Symlink(baseConversations, filepath.Join(privateRuntime, "conversations")); err != nil {
		cleanup()
		return "", nil, err
	}
	settingsPath := filepath.Join(privateRuntime, "settings.json")
	settings := map[string]interface{}{}
	if raw, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("parse agy settings: %w", err)
		}
	}
	if settings == nil {
		settings = map[string]interface{}{}
	}
	// A supplied key opts this private run into Gemini API mode. This changes
	// only the private copy; the user's global login settings stay untouched.
	if os.Getenv("GEMINI_API_KEY") != "" {
		settings["modelProvider"] = "gemini"
	}
	if len(workingDirs) > 0 && workingDirs[0] != "" {
		trusted, _ := settings["trustedWorkspaces"].([]interface{})
		alreadyTrusted := false
		for _, entry := range trusted {
			if entry == workingDirs[0] {
				alreadyTrusted = true
				break
			}
		}
		if !alreadyTrusted {
			settings["trustedWorkspaces"] = append(trusted, workingDirs[0])
		}
	}
	perms, _ := settings["permissions"].(map[string]interface{})
	if perms == nil {
		perms = map[string]interface{}{}
	}
	allows, _ := perms["allow"].([]interface{})
	entries := map[string]interface{}{}
	for _, srv := range servers {
		name := fmt.Sprintf("agentworks-%s-%d-%s", agySanitizeServerName(srv.name), os.Getpid(), agyMountRandomHex())
		allows = append(allows, "mcp("+name+"/*)")
		entries[name] = map[string]interface{}{"command": srv.command, "args": srv.args, "env": srv.env}
	}
	perms["allow"] = allows
	settings["permissions"] = perms
	if err := agyWritePrivateJSON(settingsPath, settings); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := agyWritePrivateJSON(filepath.Join(privateGemini, "config", "mcp_config.json"), map[string]interface{}{"mcpServers": entries}); err != nil {
		cleanup()
		return "", nil, err
	}
	return home, cleanup, nil
}

func agyCopyHomeTree(src, dst string, skip map[string]bool) error {
	entries, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		if skip[entry.Name()] {
			continue
		}
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := agyCopyHomeTree(from, to, skip); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		in, err := os.Open(from)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func agyWritePrivateJSON(path string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}
