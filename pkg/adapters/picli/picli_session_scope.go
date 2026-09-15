package picli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const piMCPConfigModeExclusive = "exclusive"

// piSessionRuntimeDirs returns Pi's private agent and transcript directories
// for one native session. In AgentWorks, workingDir is already scoped by user,
// workflow and conversation; including the native Pi session keeps separate Pi
// processes isolated even if a caller intentionally reuses a workspace.
func piSessionRuntimeDirs(workingDir, nativeSessionID string) (agentDir, sessionDir string) {
	base := strings.TrimSpace(workingDir)
	if base == "" {
		base = filepath.Join(os.TempDir(), "multi-llm-provider-go", "pi")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(nativeSessionID)))
	scope := "session-" + hex.EncodeToString(sum[:12])
	agentDir = filepath.Join(base, ".pi", "agentworks", scope, "agent")
	return agentDir, filepath.Join(agentDir, "sessions")
}

// preparePiExclusiveMCPConfig writes the complete MCP snapshot to the only
// location pi-mcp-adapter reads in PI_MCP_CONFIG_MODE=exclusive. The directory
// is owned by this adapter, so cleanup removes the credential-bearing config
// rather than restoring a stale token from an earlier process.
func preparePiExclusiveMCPConfig(workingDir, nativeSessionID string, opts *llmtypes.CallOptions) (agentDir, sessionDir string, cleanup func(), err error) {
	agentDir, sessionDir = piSessionRuntimeDirs(workingDir, nativeSessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return "", "", nil, fmt.Errorf("failed to create Pi session runtime dir %s: %w", sessionDir, err)
	}
	linkSharedPiExtensionCache(agentDir)

	mcpConfig := strings.TrimSpace(piMCPConfigFromOptions(opts))
	if mcpConfig == "" {
		return agentDir, sessionDir, nil, nil
	}
	normalized, err := normalizePiMCPConfig(mcpConfig)
	if err != nil {
		return "", "", nil, err
	}
	mcpPath := filepath.Join(agentDir, "mcp.json")
	if err := writePiPrivateFileAtomically(mcpPath, normalized); err != nil {
		return "", "", nil, fmt.Errorf("failed to write exclusive Pi MCP config %s: %w", mcpPath, err)
	}
	return agentDir, sessionDir, func() { _ = os.Remove(mcpPath) }, nil
}

func writePiPrivateFileAtomically(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func piExclusiveMCPEnv(agentDir, sessionDir string) []string {
	return []string{
		"PI_MCP_CONFIG_MODE=" + piMCPConfigModeExclusive,
		"PI_CODING_AGENT_DIR=" + agentDir,
		"PI_CODING_AGENT_SESSION_DIR=" + sessionDir,
	}
}

// Keep npm extension installation shared while isolating every source of
// configuration, auth and transcripts. Exclusive mode never reads package MCP
// declarations, so sharing this code cache cannot reintroduce MCP inheritance.
func linkSharedPiExtensionCache(agentDir string) {
	sharedAgentDir := piAmbientAgentDir()
	if sharedAgentDir == "" || filepath.Clean(sharedAgentDir) == filepath.Clean(agentDir) {
		return
	}
	source := filepath.Join(sharedAgentDir, "tmp", "extensions")
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		return
	}
	targetParent := filepath.Join(agentDir, "tmp")
	if err := os.MkdirAll(targetParent, 0o700); err != nil {
		return
	}
	target := filepath.Join(targetParent, "extensions")
	if _, err := os.Lstat(target); err == nil {
		return
	}
	_ = os.Symlink(source, target)
}

func piAmbientAgentDir() string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return filepath.Clean(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}
