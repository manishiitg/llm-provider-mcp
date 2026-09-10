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
// restoreAgents/agentsContent/agentsProjected are the same retention for a
// projected AGENTS.md system prompt: the file must exist before the TUI
// boots (muse reads project rules at startup), so projection happens on the
// fresh-launch path and the undo runs on kill.
type musePersistentSession struct {
	tmuxName        string
	workdir         string
	mcpJSON         string
	restoreMCP      func()
	agentsContent   string
	restoreAgents   func()
	agentsProjected bool
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
	if entry.restoreAgents != nil {
		entry.restoreAgents()
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

// CloseMuseCLIInteractiveSessionForOwner is KillMusePersistentSession under
// the naming convention the other tmux-backed providers use
// (Close<Provider>InteractiveSessionForOwner), so the root package's
// provider-agnostic re-export layer can call it the same way it calls
// pi-cli/cursor-cli/codex-cli/claude-code's. reason is accepted for
// interface parity; muse's teardown (tmux kill-session + settings/AGENTS.md
// restore) doesn't vary by reason the way claude's exit-sequence choice does.
func CloseMuseCLIInteractiveSessionForOwner(owner, reason string) {
	_ = reason
	KillMusePersistentSession(owner)
}

// CloseMuseCLIInteractiveSessionByTmux tears down a persistent muse session
// by its tmux session name rather than owner key -- a teardown backstop when
// the owning session ID is unknown or has drifted (e.g. workflow sub-agents
// registered under a step-execution owner the caller can't reconstruct).
// Falls back to a raw kill-session when no pooled entry matches the name, so
// the tmux session never lingers regardless of whether the pool still knows
// about it.
func CloseMuseCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	_ = reason
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if tmuxSessionName == "" {
		return
	}
	musePersistentPool.Lock()
	var entry *musePersistentSession
	for owner, candidate := range musePersistentPool.m {
		if candidate != nil && candidate.tmuxName == tmuxSessionName {
			entry = candidate
			delete(musePersistentPool.m, owner)
			break
		}
	}
	musePersistentPool.Unlock()
	if entry != nil {
		museKillPersistentLocked(context.Background(), entry)
		return
	}
	_ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", tmuxSessionName).Run()
}

// museAcquirePersistentSession returns the live pooled TUI for owner,
// launching it when absent or dead. created reports a fresh launch (the
// caller still waits for settle + MCP readiness on it). A retained entry
// whose workdir moved is relaunched; a retained mount that differs from
// the requested one fails fast rather than running the turn with the
// wrong tools mounted. A retained entry whose projected system prompt
// differs is relaunched too: a running TUI may not re-read AGENTS.md, so
// the file is projected fresh before every boot and never rewritten under
// a live session.
//
// systemPrompt carries the file-only system text when the caller opted into
// project-instruction-only mode; empty disables projection. projectAgents
// reports whether AGENTS.md was projected for THIS turn — only then may the
// caller skip typing the preamble inline. A projection failure is
// best-effort (the turn falls back to inline), never a session-killer.
func museAcquirePersistentSession(ctx context.Context, owner, workdir, provider, modelID, mcpJSON, readyFile, systemPrompt string, projectAgents, restoreAgentsFile bool, resumeNativeID string) (*musePersistentSession, bool, error) {
	key, err := musePersistentKey(owner)
	if err != nil {
		return nil, false, err
	}
	wantAgents := projectAgents && strings.TrimSpace(systemPrompt) != ""
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
		} else if wantAgents && entry.agentsContent != strings.TrimSpace(systemPrompt) {
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

	// Project AGENTS.md BEFORE boot: muse reads project rules at startup,
	// so a file written after launch would miss the first turn.
	var restoreAgents func()
	projected := false
	if wantAgents {
		if restore, perr := writeMuseProjectAgentsFile(workdir, strings.TrimSpace(systemPrompt), restoreAgentsFile); perr != nil {
			// Best-effort: fall back to inline typing below.
		} else {
			restoreAgents, projected = restore, true
		}
	}
	tmuxName := musePersistentTmuxName(owner)
	// A dead entry relaunches here (reuse returned early above), so a
	// caller-supplied native id resumes the conversation instead of
	// starting cold — this is the continuity-after-loss path.
	restore, err := museLaunchPersistentTUI(ctx, tmuxName, workdir, provider, modelID, mcpJSON, strings.TrimSpace(resumeNativeID))
	if err != nil {
		if restoreAgents != nil {
			restoreAgents()
		}
		return nil, false, err
	}
	entry = &musePersistentSession{tmuxName: tmuxName, workdir: workdir, mcpJSON: mcpJSON, restoreMCP: restore}
	if projected {
		entry.restoreAgents, entry.agentsContent, entry.agentsProjected =
			restoreAgents, strings.TrimSpace(systemPrompt), true
	}
	if _, err := museWaitSettled(ctx, tmuxName, 90*time.Second); err != nil {
		restore()
		if restoreAgents != nil {
			restoreAgents()
		}
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
