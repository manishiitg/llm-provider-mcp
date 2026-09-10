package musecli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/codingready"
)

// Persistent tmux sessions for muse, mirroring the other coding providers:
// one live TUI per owner session id, reused across turns, killed
// explicitly. This is what the terminal tab attaches to and what mid-turn
// steering injects into. Bounded turns (the default) keep launching and
// tearing down one TUI per turn and never touch this pool.

// musePersistentSession is one pooled live TUI. restoreMCP is the retained
// settings-merge undo for the mount applied at launch; it runs on kill, so
// a persistent mount never leaks into the user's settings.json.
type musePersistentSession struct {
	tmuxName   string
	workdir    string
	mcpJSON    string
	restoreMCP func()
}

var musePersistentPool = struct {
	sync.Mutex
	m map[string]*musePersistentSession
}{m: make(map[string]*musePersistentSession)}

// musePersistentKey requires an owner: pooling without one would let two
// conversations share (and overhear) a TUI. Fail fast on caller bug.
func musePersistentKey(owner string) (string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", fmt.Errorf("muse-cli persistent tmux requires an owner session id (WithMuseInteractiveSessionID)")
	}
	return owner, nil
}

// musePersistentTmuxName derives a stable, tmux-safe session name from the
// owner so the terminal tab and diagnostics can find the pane
// deterministically.
func musePersistentTmuxName(owner string) string {
	var b strings.Builder
	b.WriteString("mlp-muse-")
	for _, r := range strings.ToLower(owner) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > 48 {
		name = name[:48]
	}
	return strings.Trim(name, "-")
}

// museKillPersistentLocked kills the entry's tmux session and runs its
// retained settings restore. Caller holds the pool lock.
func museKillPersistentLocked(ctx context.Context, entry *musePersistentSession) {
	if entry == nil {
		return
	}
	_ = exec.CommandContext(ctx, "tmux", "kill-session", "-t", entry.tmuxName).Run()
	if entry.restoreMCP != nil {
		entry.restoreMCP()
	}
}

// KillMusePersistentSession tears down one pooled TUI and restores the
// user's settings.json. The orchestrator calls this when the conversation
// ends; unknown owners are a no-op, never an error.
func KillMusePersistentSession(owner string) {
	key, err := musePersistentKey(owner)
	if err != nil {
		return
	}
	musePersistentPool.Lock()
	defer musePersistentPool.Unlock()
	entry := musePersistentPool.m[key]
	delete(musePersistentPool.m, key)
	museKillPersistentLocked(context.Background(), entry)
}

// museAcquirePersistentSession returns the live pooled TUI for owner,
// launching it when absent or dead. created reports a fresh launch (the
// caller still waits for settle + MCP readiness on it). A retained entry
// whose workdir moved is relaunched; a retained mount that differs from
// the requested one fails fast rather than running the turn with the
// wrong tools mounted.
func museAcquirePersistentSession(ctx context.Context, owner, workdir, provider, modelID, mcpJSON, readyFile string) (*musePersistentSession, bool, error) {
	key, err := musePersistentKey(owner)
	if err != nil {
		return nil, false, err
	}
	musePersistentPool.Lock()
	entry := musePersistentPool.m[key]
	if entry != nil {
		if entry.workdir != workdir {
			museKillPersistentLocked(ctx, entry)
			delete(musePersistentPool.m, key)
			entry = nil
		} else if !museTmuxSessionAlive(ctx, entry.tmuxName) {
			museKillPersistentLocked(ctx, entry)
			delete(musePersistentPool.m, key)
			entry = nil
		} else if mcpJSON != "" && entry.mcpJSON != "" && mcpJSON != entry.mcpJSON {
			musePersistentPool.Unlock()
			return nil, false, fmt.Errorf("muse-cli persistent session %q already mounted a different MCP config; kill it first", entry.tmuxName)
		}
	}
	if entry != nil {
		musePersistentPool.Unlock()
		return entry, false, nil
	}
	musePersistentPool.Unlock()

	tmuxName := musePersistentTmuxName(owner)
	restore, err := museLaunchPersistentTUI(ctx, tmuxName, workdir, provider, modelID, mcpJSON, "")
	if err != nil {
		return nil, false, err
	}
	entry = &musePersistentSession{tmuxName: tmuxName, workdir: workdir, mcpJSON: mcpJSON, restoreMCP: restore}
	if _, err := museWaitSettled(ctx, tmuxName, 90*time.Second); err != nil {
		restore()
		_ = exec.CommandContext(ctx, "tmux", "kill-session", "-t", tmuxName).Run()
		return nil, false, err
	}
	if readyFile != "" {
		codingready.WaitForMCPReadyFile(ctx, readyFile, codingready.MCPReadyWait())
	}
	musePersistentPool.Lock()
	musePersistentPool.m[key] = entry
	musePersistentPool.Unlock()
	return entry, true, nil
}

// museLaunchPersistentTUI boots one TUI for pool ownership and returns the
// retained settings-merge undo (nil when unmounted). resumeNativeID
// relaunches into a previous native session after a backend restart
// (`muse resume <uuid>`); empty starts fresh.
func museLaunchPersistentTUI(ctx context.Context, tmuxName, workdir, provider, modelID, mcpJSON, resumeNativeID string) (func(), error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; muse-cli tmux mode requires tmux: %w", err)
	}
	if _, err := exec.LookPath("muse"); err != nil {
		return nil, fmt.Errorf("muse CLI not in PATH: %w", err)
	}
	var restore func()
	argv := []string{"--trust-workspace", "--provider", provider}
	if strings.TrimSpace(modelID) != "" {
		argv = append(argv, "--model", strings.TrimSpace(modelID))
	}
	if strings.TrimSpace(mcpJSON) != "" {
		var err error
		if restore, err = museApplyMCPConfig(strings.TrimSpace(mcpJSON)); err != nil {
			return nil, err
		}
		// MCP-server tools gate on approval while built-in shell tools do
		// not; a mounted persistent TUI with approvals on stalls on its
		// first bridge-tool call with nobody to click approve.
		argv = append(argv, "--disable-approval")
	}
	cli := append([]string{"muse"}, argv...)
	if id := strings.TrimSpace(resumeNativeID); id != "" {
		cli = []string{"muse", "resume", id}
		cli = append(cli, argv...)
	}
	launch := exec.CommandContext(ctx, "tmux", append([]string{"new-session", "-d", "-s", tmuxName, "-x", "200", "-y", "50", "-c", workdir}, cli...)...)
	if out, err := launch.CombinedOutput(); err != nil {
		if restore != nil {
			restore()
		}
		return nil, fmt.Errorf("tmux new-session: %w\n%s", err, out)
	}
	return restore, nil
}

// musePersistentReadyFile extracts the MCP readiness-marker path the same
// way the bounded lane does.
func musePersistentReadyFile(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil {
		return ""
	}
	return codingready.MCPReadyFileFromMetadata(opts.Metadata.Custom)
}
