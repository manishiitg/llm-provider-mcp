package agycli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var agyLegacySweepOnce sync.Once

// agySweepLegacyGlobalMounts removes mounts left by an older process that
// wrote the bridge into the shared AGY home. It runs before the first new
// isolated home is launched. A mount whose owning PID is alive is retained
// so an older backend running alongside this one is not disrupted.
func agySweepLegacyGlobalMounts(home string) error {
	configPath := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	raw, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var config struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return err
	}
	removed := map[string]bool{}
	for name := range config.Servers {
		if agyLegacyMountDead(name) {
			delete(config.Servers, name)
			removed[name] = true
		}
	}
	if len(removed) == 0 {
		return nil
	}
	var document map[string]interface{}
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	servers, _ := document["mcpServers"].(map[string]interface{})
	for name := range removed {
		delete(servers, name)
	}
	if err := agyWriteGlobalJSON(configPath, document); err != nil {
		return err
	}
	settingsPath := agySettingsPath(home)
	settingsRaw, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		return err
	}
	perms, _ := settings["permissions"].(map[string]interface{})
	allows, _ := perms["allow"].([]interface{})
	kept := make([]interface{}, 0, len(allows))
	for _, value := range allows {
		text, ok := value.(string)
		if !ok {
			kept = append(kept, value)
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(text, "mcp("), "/*)")
		if !removed[name] {
			kept = append(kept, value)
		}
	}
	if len(kept) != len(allows) {
		perms["allow"] = kept
		return agyWriteGlobalJSON(settingsPath, settings)
	}
	return nil
}

func agyLegacyMountDead(name string) bool {
	if !strings.HasPrefix(name, "agentworks-") {
		return false
	}
	parts := strings.Split(name, "-")
	if len(parts) < 4 {
		return false
	}
	pid, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	return agyPIDDead(pid)
}

func agyPIDDead(pid int) bool {
	if pid <= 0 || pid == os.Getpid() {
		return false
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

// Private homes from a killed backend hold only that backend's MCP config,
// but they still contain credentials until removed. Reclaim only homes whose
// encoded PID is gone; a second live backend keeps its homes intact.
func agySweepStalePrivateHomes() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "agentworks-agy-") {
			continue
		}
		rest := strings.TrimPrefix(entry.Name(), "agentworks-agy-")
		pidText, suffix, ok := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidText)
		if !ok || err != nil || len(suffix) < 6 || !agyPIDDead(pid) {
			continue
		}
		path := filepath.Join(os.TempDir(), entry.Name())
		info, err := os.Lstat(path)
		if err == nil && info.IsDir() && info.Mode().Perm() == 0o700 {
			_ = os.RemoveAll(path)
		}
	}
}

func agyWriteGlobalJSON(path string, value interface{}) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agentworks-sweep-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
