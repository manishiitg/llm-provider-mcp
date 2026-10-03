package musecli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/slotfs"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// museShortSocketLimit is the longest runtime folder that still leaves room for
// Muse's `muse/ms-<id>.sock.lease` inside the 104-byte unix socket path limit.
const museShortSocketLimit = 60

// museRuntimeDirFor is the XDG_RUNTIME_DIR a Landlock-confined Muse gets: a
// "run" folder in its private home (the one place it may write). Without it
// Muse falls back to /tmp/tbh-<uid>-rt, which a confined CLI cannot use, and
// every start printed "local session messaging unavailable: registry_io ...
// Permission denied" (Excellence, 2026-10-03). A private home deep in a
// runtime folder is far past the socket path limit, so Muse is handed a short
// symlink to it instead, one per private home, reused across turns.
func museRuntimeDirFor(privateHome string) (string, error) {
	privateHome = strings.TrimSpace(privateHome)
	if privateHome == "" {
		return "", nil
	}
	run := filepath.Join(privateHome, "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		return "", err
	}
	// The user's own Linux account runs Muse on a slot: it must own what it writes.
	if err := slotfs.ShareTree(privateHome, run); err != nil {
		return "", err
	}
	if len(run) <= museShortSocketLimit {
		return run, nil
	}
	sum := sha256.Sum256([]byte(run))
	link := filepath.Join(slotfs.TempDir(privateHome), "mrt-"+hex.EncodeToString(sum[:])[:10])
	if target, err := os.Readlink(link); err == nil && target == run {
		return link, nil
	}
	_ = os.Remove(link)
	if err := os.Symlink(run, link); err != nil {
		// Never fall back to a path Muse cannot use; no env is better than a wrong one.
		return "", err
	}
	return link, nil
}

// museRuntimeEnv is the XDG_RUNTIME_DIR entry for a Landlock-confined launch
// ("" elsewhere: on a Mac or an unconfined run Muse's own default works).
func museRuntimeEnv(opts *llmtypes.CallOptions) []string {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return nil
	}
	dir, err := museRuntimeDirFor(opts.CLISecurity.PrivateHome)
	if err != nil || dir == "" {
		return nil
	}
	return []string{"XDG_RUNTIME_DIR=" + dir}
}

// museProbePrefix is the folder Muse creates in its temp folder at every start
// to test its workspace (and never removes): 572 of them piled up on Excellence
// and 1,286 in a developer's Mac temp folder.
const museProbePrefix = "muse-workspace-probe-"

var museProbeSweeps sync.Map // temp folder -> time of last sweep

// museSweepWorkspaceProbes removes Muse's old workspace-probe folders from the
// temp folder it will use. A probe younger than two minutes may belong to a
// start in progress and stays; the sweep runs at most once a minute per folder.
func museSweepWorkspaceProbes(opts *llmtypes.CallOptions) {
	dir := llmtypes.CLIHomeEnvironment(opts)["TMPDIR"]
	if strings.TrimSpace(dir) == "" {
		dir = os.TempDir()
	}
	now := time.Now()
	if last, ok := museProbeSweeps.Load(dir); ok && now.Sub(last.(time.Time)) < time.Minute {
		return
	}
	museProbeSweeps.Store(dir, now)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), museProbePrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < 2*time.Minute {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}
