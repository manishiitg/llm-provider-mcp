package musecli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The CPU profile showed every question/background poll rewalking all dates.
// Cache a session's fixed path, but do not share it across provider accounts.
func TestMuseSessionLogPathCachesWithinAccountAndRelocatesAfterRemoval(t *testing.T) {
	root := t.TempDir()
	write := func(home, date string) string {
		t.Helper()
		p := filepath.Join(home, "muse", "sessions", date, "native-session", "session.jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := filepath.Join(root, "account-a"), filepath.Join(root, "account-b")
	first := write(a, "2026/11/13")
	if got := museSessionLogPath("native-session", a); got != first {
		t.Fatal("initial log not located")
	}
	earlier := write(a, "2026/01/01")
	if got := museSessionLogPath("native-session", a); got != first {
		t.Fatal("poll searched dates again instead of retaining existing path")
	}
	other := write(b, "2026/05/01")
	if got := museSessionLogPath("native-session", b); got != other {
		t.Fatal("account data homes shared a path")
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if got := museSessionLogPath("native-session", a); got != earlier {
		t.Fatal("removed cached file was not re-resolved")
	}
}

func BenchmarkMuseSessionLogPathPolling(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 180; i++ {
		p := filepath.Join(root, "muse", "sessions", fmt.Sprintf("%04d/%02d/%02d", 2020+i/36, 1+i%12, 1+i%28), fmt.Sprintf("session-%d", i), "session.jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0600); err != nil {
			b.Fatal(err)
		}
	}
	pattern := filepath.Join(root, "muse", "sessions", "*", "*", "*", "session-179", "session.jsonl")
	b.Run("before_glob", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if matches, err := filepath.Glob(pattern); err != nil || len(matches) != 1 {
				b.Fatal("missing benchmark log")
			}
		}
	})
	museSessionLogPath("session-179", root)
	b.Run("after_cached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if museSessionLogPath("session-179", root) == "" {
				b.Fatal("missing cached log")
			}
		}
	})
}
