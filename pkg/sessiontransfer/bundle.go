// Package sessiontransfer carries one native conversation's private state.
// Account configuration, credentials and shared indexes are never payloads.
package sessiontransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/internal/slotfs"
)

const MaxBytes int64 = 32 << 30

type File struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Bundle struct {
	Provider  string          `json:"provider"`
	Format    string          `json:"format"`
	Transport string          `json:"transport"`
	Snapshot  string          `json:"snapshot,omitempty"`
	Parent    string          `json:"parent_snapshot,omitempty"`
	SessionID string          `json:"session_id"`
	Workspace string          `json:"workspace"`
	Date      string          `json:"date,omitempty"`
	Files     map[string]File `json:"files"`
	Root      string          `json:"-"`
}

var UUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var sdkID = regexp.MustCompile(`^agent-[A-Za-z0-9_-]{1,120}$`)
var date = regexp.MustCompile(`^[0-9]{4}/[0-9]{2}/[0-9]{2}$`)
var leaf = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}(\.jsonl|\.meta\.json|\.txt)$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (b *Bundle) Validate() error {
	if b == nil || !filepath.IsAbs(b.Workspace) || len(b.Files) == 0 || len(b.Files) > 4096 {
		return fmt.Errorf("invalid native session descriptor")
	}
	if b.Transport != "sdk" && b.Transport != "acp" && b.Transport != "tmux" && b.Transport != "structured" && b.Transport != "exec" && b.Transport != "app-server" {
		return fmt.Errorf("unsupported native runtime")
	}
	switch b.Provider {
	case "claude-code":
		if b.Transport != "sdk" && b.Transport != "structured" && b.Transport != "tmux" {
			return fmt.Errorf("unsupported Claude native runtime")
		}
	case "codex-cli":
		if b.Transport != "app-server" && b.Transport != "tmux" {
			return fmt.Errorf("unsupported Codex native runtime")
		}
	case "muse-cli":
		if b.Transport != "structured" && b.Transport != "tmux" {
			return fmt.Errorf("unsupported Muse native runtime")
		}
	case "cursor-cli":
	default:
		return fmt.Errorf("unsupported native program")
	}
	if b.Format == "cursor-sdk-sqlite" {
		if b.Provider != "cursor-cli" || b.Transport != "sdk" || !sdkID.MatchString(b.SessionID) {
			return fmt.Errorf("invalid Cursor SDK identity")
		}
	} else if !UUID.MatchString(b.SessionID) {
		return fmt.Errorf("invalid native session identity")
	}
	if strings.HasSuffix(b.Format, "sqlite") && (!digest.MatchString(b.Snapshot) || b.Parent != "" && !digest.MatchString(b.Parent)) {
		return fmt.Errorf("invalid native snapshot lineage")
	}
	var total int64
	for name, f := range b.Files {
		if !b.allowed(name) || f.Size <= 0 || f.Size > MaxBytes || !digest.MatchString(f.SHA256) {
			return fmt.Errorf("invalid native session file")
		}
		total += f.Size
		if total > MaxBytes {
			return fmt.Errorf("native session exceeds transfer capacity")
		}
	}
	root := "session.jsonl"
	if b.Format == "cursor-sdk-sqlite" {
		root = "index.db"
	}
	if b.Format == "cursor-cli-sqlite" {
		root = "store.db"
	}
	if _, ok := b.Files[root]; !ok {
		return fmt.Errorf("native session root is missing")
	}
	return nil
}

func (b *Bundle) allowed(name string) bool {
	if filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	p := strings.Split(name, "/")
	switch b.Format {
	case "muse-jsonl":
		if b.Provider != "muse-cli" || !date.MatchString(b.Date) {
			return false
		}
		if name == "session.jsonl" {
			return true
		}
		if len(p) < 3 || len(p) > 17 || len(p)%2 != 1 || p[len(p)-1] != "session.jsonl" {
			return false
		}
		for i := 0; i < len(p)-1; i += 2 {
			if p[i] != "subagent" || !UUID.MatchString(p[i+1]) {
				return false
			}
		}
		return true
	case "claude-jsonl":
		if b.Provider != "claude-code" {
			return false
		}
		return name == "session.jsonl" || len(p) == 2 && (p[0] == "subagents" || p[0] == "tool-results") && leaf.MatchString(p[1])
	case "codex-rollout":
		return b.Provider == "codex-cli" && date.MatchString(b.Date) && (name == "session.jsonl" || len(p) == 2 && p[0] == "children" && strings.HasSuffix(p[1], ".jsonl") && UUID.MatchString(strings.TrimSuffix(p[1], ".jsonl")))
	case "cursor-cli-sqlite":
		return b.Provider == "cursor-cli" && b.Transport == "tmux" && (name == "store.db" || name == "meta.json" || name == "prompt_history.json")
	case "cursor-sdk-sqlite":
		return name == "index.db" || len(p) == 3 && p[0] == "agents" && p[1] == fmt.Sprintf("agent-%x", sha256.Sum256([]byte(b.SessionID))) && p[2] == "store.db"
	}
	return false
}

// Path rejects links in transferred paths. Only the account-selected root may
// have a different physical spelling (for example /tmp on macOS).
func Path(root, name string, create bool) (string, error) {
	if !filepath.IsAbs(root) || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return "", fmt.Errorf("invalid private native path")
	}
	if create {
		var missing []string
		for p := root; ; p = filepath.Dir(p) {
			_, e := os.Lstat(p)
			if e == nil {
				break
			}
			if !os.IsNotExist(e) || p == filepath.Dir(p) {
				return "", e
			}
			missing = append(missing, p)
		}
		for i := len(missing) - 1; i >= 0; i-- {
			if e := os.Mkdir(missing[i], 0700); e != nil && !os.IsExist(e) {
				return "", e
			}
			if e := slotfs.ShareTree(root, missing[i]); e != nil {
				return "", e
			}
		}
	}
	base, e := filepath.EvalSymlinks(root)
	if e != nil {
		return "", e
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid native path component")
		}
		base = filepath.Join(base, part)
		if create && i < len(parts)-1 {
			if e = os.Mkdir(base, 0700); e != nil {
				if !os.IsExist(e) {
					return "", e
				}
			} else if e = slotfs.ShareTree(root, base); e != nil {
				return "", e
			}
		}
		info, e := os.Lstat(base)
		if create && i == len(parts)-1 && os.IsNotExist(e) {
			return base, nil
		}
		if e != nil {
			return "", e
		}
		if info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("native state contains a link or special file")
		}
	}
	return base, nil
}

func Inspect(path string) (File, error) {
	f, e := os.Open(path)
	if e != nil {
		return File{}, e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() {
		return File{}, fmt.Errorf("native state is not a regular file")
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, MaxBytes+1))
	if e != nil {
		return File{}, e
	}
	if n > MaxBytes {
		return File{}, fmt.Errorf("native file exceeds capacity")
	}
	return File{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func (b *Bundle) ValidateFiles() error {
	if e := b.Validate(); e != nil {
		return e
	}
	for name, want := range b.Files {
		p, e := Path(b.Root, name, false)
		if e != nil {
			return e
		}
		got, e := Inspect(p)
		if e != nil {
			return e
		}
		if got != want {
			return fmt.Errorf("native state changed during transfer")
		}
	}
	return nil
}

func Copy(root, destination, source string) error {
	in, e := os.Open(source)
	if e != nil {
		return e
	}
	defer in.Close()
	f, e := os.CreateTemp(filepath.Dir(destination), ".native-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	_, e = io.Copy(f, in)
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = slotfs.ShareTree(root, f.Name()); e != nil {
		return e
	}
	return os.Rename(f.Name(), destination)
}

func Write(root, destination string, raw []byte) error {
	f, e := os.CreateTemp(filepath.Dir(destination), ".native-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(raw); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = slotfs.ShareTree(root, f.Name()); e != nil {
		return e
	}
	return os.Rename(f.Name(), destination)
}

func Ancestor(old, next *Bundle) error {
	if old.Provider != next.Provider || old.Format != next.Format || old.SessionID != next.SessionID {
		return fmt.Errorf("native session identity changed")
	}
	for name, want := range old.Files {
		got, ok := next.Files[name]
		if !ok || got.Size < want.Size {
			return Diverged()
		}
		p, e := Path(next.Root, name, false)
		if e != nil {
			return e
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.CopyN(h, f, want.Size)
		f.Close()
		if e != nil || n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			return Diverged()
		}
	}
	return nil
}

func Diverged() error {
	return fmt.Errorf("native history changed on both computers; both copies were kept")
}
