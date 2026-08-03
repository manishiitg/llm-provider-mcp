package claudecode

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func resetClaudeTranscriptResolverForTest(t *testing.T) {
	t.Helper()
	claudeTranscriptPathCache.Lock()
	claudeTranscriptPathCache.paths = make(map[string]string)
	claudeTranscriptPathCache.Unlock()
	originalGlob := claudeTranscriptGlob
	originalOpen := claudeTranscriptOpen
	t.Cleanup(func() {
		claudeTranscriptPathCache.Lock()
		claudeTranscriptPathCache.paths = make(map[string]string)
		claudeTranscriptPathCache.Unlock()
		claudeTranscriptGlob = originalGlob
		claudeTranscriptOpen = originalOpen
	})
}

func transcriptPathForWorkingDir(t *testing.T, home, workingDir, sessionID string) string {
	t.Helper()
	paths := claudeTranscriptWorkingDirCandidates(home, workingDir, sessionID)
	if len(paths) == 0 {
		t.Fatalf("no transcript candidate for working dir %q", workingDir)
	}
	if err := os.MkdirAll(filepath.Dir(paths[0]), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return paths[0]
}

func TestResolveClaudeTranscriptPathUsesWorkingDirectoryWithoutGlobalScan(t *testing.T) {
	resetClaudeTranscriptResolverForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	const sessionID = "11111111-2222-3333-4444-555566667777"
	workingDir := filepath.Join(t.TempDir(), "project.with_underlines")
	path := transcriptPathForWorkingDir(t, home, workingDir, sessionID)
	appendLine(t, path, "{}\n")

	var globCalls atomic.Int32
	claudeTranscriptGlob = func(pattern string) ([]string, error) {
		globCalls.Add(1)
		return filepath.Glob(pattern)
	}

	for i := 0; i < 2; i++ {
		got, err := resolveClaudeTranscriptPath(sessionID, workingDir, true)
		if err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
		if got != path {
			t.Fatalf("resolve %d = %q, want %q", i, got, path)
		}
	}
	if got := globCalls.Load(); got != 0 {
		t.Fatalf("global glob calls = %d, want 0 for known working directory", got)
	}
}

func TestClaudeTranscriptTailerDiscoversAndOpensOnlyOnceAcrossPolls(t *testing.T) {
	resetClaudeTranscriptResolverForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	const sessionID = "22222222-3333-4444-5555-666677778888"
	workingDir := filepath.Join(t.TempDir(), "project")
	path := transcriptPathForWorkingDir(t, home, workingDir, sessionID)
	turnStart := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)
	appendLine(t, path, `{"type":"assistant","timestamp":"`+ts+`","message":{"id":"m1","content":[{"type":"text","text":"first"}]}}`+"\n")

	var globCalls atomic.Int32
	var openCalls atomic.Int32
	claudeTranscriptGlob = func(pattern string) ([]string, error) {
		globCalls.Add(1)
		return filepath.Glob(pattern)
	}
	originalOpen := claudeTranscriptOpen
	claudeTranscriptOpen = func(name string) (*os.File, error) {
		openCalls.Add(1)
		return originalOpen(name)
	}

	tailer := newClaudeTranscriptTailer(sessionID, workingDir)
	defer tailer.Close()
	now := time.Now()
	events, err := tailer.Read(now, turnStart, nil)
	if err != nil || len(events) != 1 || events[0].Text != "first" {
		t.Fatalf("first read events=%+v err=%v", events, err)
	}
	for i := 1; i <= 100; i++ {
		if i == 50 {
			appendLine(t, path, `{"type":"assistant","timestamp":"`+ts+`","message":{"id":"m2","content":[{"type":"text","text":"second"}]}}`+"\n")
		}
		events, err = tailer.Read(now.Add(time.Duration(i)*claudeTranscriptStreamPollInterval), turnStart, nil)
		if err != nil {
			t.Fatalf("poll %d events=%+v err=%v", i, events, err)
		}
		if i == 50 {
			if len(events) != 1 || events[0].Text != "second" {
				t.Fatalf("poll %d appended events=%+v, want second", i, events)
			}
		} else if len(events) != 0 {
			t.Fatalf("poll %d unexpectedly repeated events=%+v", i, events)
		}
	}
	if got := globCalls.Load(); got != 0 {
		t.Fatalf("global glob calls = %d, want 0", got)
	}
	if got := openCalls.Load(); got != 1 {
		t.Fatalf("transcript open calls = %d, want 1 across 101 polls", got)
	}
}

func TestClaudeTranscriptTailerBoundsFallbackScansWhileTranscriptIsMissing(t *testing.T) {
	resetClaudeTranscriptResolverForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	const sessionID = "55555555-6666-7777-8888-999900001111"
	workingDir := filepath.Join(t.TempDir(), "missing")
	var globCalls atomic.Int32
	claudeTranscriptGlob = func(string) ([]string, error) {
		globCalls.Add(1)
		return nil, nil
	}

	tailer := newClaudeTranscriptTailer(sessionID, workingDir)
	defer tailer.Close()
	now := time.Now()
	for i := 0; i < 100; i++ {
		if events, err := tailer.Read(now.Add(time.Duration(i)*claudeTranscriptStreamPollInterval), time.Time{}, nil); err != nil || len(events) != 0 {
			t.Fatalf("poll %d events=%+v err=%v", i, events, err)
		}
	}
	if got := globCalls.Load(); got != 1 {
		t.Fatalf("global glob calls across 25s of missing-file polling = %d, want 1", got)
	}
}

func TestClaudeTranscriptTailerBacksOffUntilDelayedTranscriptAppears(t *testing.T) {
	resetClaudeTranscriptResolverForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	const sessionID = "33333333-4444-5555-6666-777788889999"
	workingDir := filepath.Join(t.TempDir(), "delayed")
	path := transcriptPathForWorkingDir(t, home, workingDir, sessionID)
	turnStart := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)

	var globCalls atomic.Int32
	claudeTranscriptGlob = func(string) ([]string, error) {
		globCalls.Add(1)
		return nil, nil
	}

	tailer := newClaudeTranscriptTailer(sessionID, workingDir)
	defer tailer.Close()
	now := time.Now()
	if events, err := tailer.Read(now, turnStart, nil); err != nil || len(events) != 0 {
		t.Fatalf("initial read events=%+v err=%v", events, err)
	}
	if events, err := tailer.Read(now.Add(100*time.Millisecond), turnStart, nil); err != nil || len(events) != 0 {
		t.Fatalf("pre-backoff read events=%+v err=%v", events, err)
	}
	if got := globCalls.Load(); got != 0 {
		t.Fatalf("glob calls before direct-path creation = %d, want 0", got)
	}

	appendLine(t, path, `{"type":"assistant","timestamp":"`+ts+`","message":{"id":"m1","content":[{"type":"text","text":"ready"}]}}`+"\n")
	events, err := tailer.Read(now.Add(claudeTranscriptStreamPollInterval), turnStart, nil)
	if err != nil || len(events) != 1 || events[0].Text != "ready" {
		t.Fatalf("post-creation read events=%+v err=%v", events, err)
	}
	if got := globCalls.Load(); got != 0 {
		t.Fatalf("glob calls after direct-path creation = %d, want 0", got)
	}
}

func TestOpenClaudeTranscriptInvalidatesMissingCachedPath(t *testing.T) {
	resetClaudeTranscriptResolverForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	const sessionID = "44444444-5555-6666-7777-888899990000"
	legacyOne := filepath.Join(home, ".claude", "projects", "legacy-one", sessionID+".jsonl")
	legacyTwo := filepath.Join(home, ".claude", "projects", "legacy-two", sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(legacyOne), 0o755); err != nil {
		t.Fatalf("MkdirAll legacy one: %v", err)
	}
	appendLine(t, legacyOne, "{}\n")

	var globCalls atomic.Int32
	claudeTranscriptGlob = func(pattern string) ([]string, error) {
		globCalls.Add(1)
		return filepath.Glob(pattern)
	}
	path, err := resolveClaudeTranscriptPath(sessionID, "", true)
	if err != nil || path != legacyOne {
		t.Fatalf("initial resolve path=%q err=%v", path, err)
	}
	if err := os.Remove(legacyOne); err != nil {
		t.Fatalf("remove legacy one: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(legacyTwo), 0o755); err != nil {
		t.Fatalf("MkdirAll legacy two: %v", err)
	}
	appendLine(t, legacyTwo, "{}\n")

	f, reopenedPath, err := openClaudeTranscript(sessionID, "")
	if err != nil {
		t.Fatalf("reopen after replacement: %v", err)
	}
	defer f.Close()
	if reopenedPath != legacyTwo {
		t.Fatalf("reopened path = %q, want %q", reopenedPath, legacyTwo)
	}
	if got := globCalls.Load(); got != 2 {
		t.Fatalf("global glob calls = %d, want initial discovery plus one re-resolution", got)
	}
}
