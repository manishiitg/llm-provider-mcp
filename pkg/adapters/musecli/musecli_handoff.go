package musecli

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/pkg/pathidentity"
)

const nativeHandoffLimit int64 = 32 << 30

var nativeHandoffID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var nativeHandoffDate = regexp.MustCompile(`^[0-9]{4}/[0-9]{2}/[0-9]{2}$`)

// NativeSessionBundle carries only one journal tree, never configuration,
// credentials, hooks or a caller-selected destination path. Permission frames
// remain historical records: the destination launch reapplies its own policy.
type NativeSessionBundle struct {
	SessionID string                       `json:"session_id"`
	Date      string                       `json:"date"`
	Workspace string                       `json:"workspace"`
	Files     map[string]NativeSessionFile `json:"files"`
	Root      string                       `json:"-"`
}

// File bytes stay on disk and travel through the private delta transport.
type NativeSessionFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func nativeHandoffFile(name string) bool {
	if name == "session.jsonl" {
		return true
	}
	p := strings.Split(name, "/")
	if len(p) < 3 || len(p) > 17 || len(p)%2 != 1 || p[len(p)-1] != "session.jsonl" {
		return false
	}
	for i := 0; i < len(p)-1; i += 2 {
		if p[i] != "subagent" || !nativeHandoffID.MatchString(p[i+1]) {
			return false
		}
	}
	return true
}

func (b *NativeSessionBundle) Validate() error {
	if b == nil || !nativeHandoffID.MatchString(b.SessionID) || !nativeHandoffDate.MatchString(b.Date) || !filepath.IsAbs(b.Workspace) || len(b.Files) == 0 || len(b.Files) > 4096 {
		return fmt.Errorf("invalid Muse session transfer")
	}
	var total int64
	for name, f := range b.Files {
		hash, err := hex.DecodeString(f.SHA256)
		if !nativeHandoffFile(name) || f.Size <= 0 || f.Size > nativeHandoffLimit || err != nil || len(hash) != 32 {
			return fmt.Errorf("invalid Muse journal descriptor")
		}
		total += f.Size
		if total > nativeHandoffLimit {
			return fmt.Errorf("Muse session exceeds the transfer capacity")
		}
	}
	if _, ok := b.Files["session.jsonl"]; !ok {
		return fmt.Errorf("Muse root journal is missing")
	}
	return nil
}

// ValidateFiles streams journals: memory usage does not grow with a long chat.
func (b *NativeSessionBundle) ValidateFiles() error {
	if err := b.Validate(); err != nil {
		return err
	}
	for name, want := range b.Files {
		path, err := confinedJournalPath(b.Root, name, false)
		if err != nil {
			return err
		}
		got, workspace, err := inspectNativeJournal(path, name == "session.jsonl", b.SessionID)
		if err != nil {
			return err
		}
		if got != want || (name == "session.jsonl" && workspace != b.Workspace) {
			return fmt.Errorf("Muse journal changed during transfer")
		}
	}
	return nil
}

func inspectNativeJournal(path string, root bool, id string) (NativeSessionFile, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return NativeSessionFile{}, "", err
	}
	defer f.Close()
	hash := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(io.LimitReader(f, nativeHandoffLimit+1), hash), 64<<10)
	var size int64
	workspace := ""
	for {
		line, e := reader.ReadBytes('\n')
		if e == io.EOF && len(line) == 0 {
			break
		}
		if e != nil || !json.Valid(line) {
			return NativeSessionFile{}, "", fmt.Errorf("incomplete or invalid Muse journal")
		}
		size += int64(len(line))
		if size > nativeHandoffLimit {
			return NativeSessionFile{}, "", fmt.Errorf("Muse journal exceeds transfer capacity")
		}
		if root {
			var row struct {
				Stream      struct{ Kind, ID string }
				PayloadType string `json:"payload_type"`
				Payload     struct {
					Record struct {
						Workspace string `json:"workspace_root"`
						Build     struct{ Semver string }
					}
				}
			}
			if err := json.Unmarshal(line, &row); err != nil {
				return NativeSessionFile{}, "", err
			}
			if row.Stream.Kind == "session" && row.Stream.ID != id {
				return NativeSessionFile{}, "", fmt.Errorf("Muse journal belongs to another session")
			}
			if row.PayloadType == "runtime.session.metadata" {
				if row.Payload.Record.Build.Semver != "1.4.14" {
					return NativeSessionFile{}, "", fmt.Errorf("Muse journal format is unsupported")
				}
				workspace = row.Payload.Record.Workspace
			}
		}
	}
	if size == 0 || (root && workspace == "") {
		return NativeSessionFile{}, "", fmt.Errorf("Muse journal metadata is missing")
	}
	return NativeSessionFile{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, workspace, nil
}

// NativeSessionAncestor checks the destination's actual history, independently
// of rsync's cached basis. Never overwrite turns made on both machines.
func NativeSessionAncestor(previous, incoming *NativeSessionBundle) error {
	for name, old := range previous.Files {
		next, ok := incoming.Files[name]
		if !ok || next.Size < old.Size {
			return fmt.Errorf("Muse session changed on both computers; both copies were kept")
		}
		path, err := confinedJournalPath(incoming.Root, name, false)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, err := io.CopyN(hash, f, old.Size)
		f.Close()
		if err != nil || n != old.Size || hex.EncodeToString(hash.Sum(nil)) != old.SHA256 {
			return fmt.Errorf("Muse session changed on both computers; both copies were kept")
		}
	}
	return nil
}

// confinedJournalPath rejects links within the store. The store itself is a
// trusted account/runtime root; its physical spelling may differ on macOS.
func confinedJournalPath(root, relative string, create bool) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("Muse store must be absolute")
	}
	if create {
		if err := os.MkdirAll(root, 0700); err != nil {
			return "", err
		}
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid Muse journal path")
		}
		base = filepath.Join(base, part)
		if create && i < len(parts)-1 {
			if err := os.Mkdir(base, 0700); err != nil && !os.IsExist(err) {
				return "", err
			}
		}
		info, err := os.Lstat(base)
		if os.IsNotExist(err) && create && i == len(parts)-1 {
			return base, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return "", fmt.Errorf("Muse journal path is not a regular private file")
		}
	}
	return base, nil
}

func ExportNativeSession(dataHome, id, workspace string) (*NativeSessionBundle, error) {
	if !nativeHandoffID.MatchString(id) {
		return nil, fmt.Errorf("invalid Muse session ID")
	}
	root := filepath.Join(dataHome, "muse", "sessions")
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", id, "session.jsonl"))
	if err != nil || len(matches) != 1 {
		return nil, fmt.Errorf("Muse session journal is unavailable or ambiguous")
	}
	rel, err := filepath.Rel(root, matches[0])
	if err != nil {
		return nil, err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 5 {
		return nil, fmt.Errorf("invalid Muse journal layout")
	}
	b := &NativeSessionBundle{SessionID: id, Date: strings.Join(parts[:3], "/"), Files: map[string]NativeSessionFile{}}
	sessionRoot := filepath.Dir(matches[0])
	b.Root = sessionRoot
	total := int64(0)
	err = filepath.WalkDir(sessionRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Muse journal tree contains a link")
		}
		if d.IsDir() {
			return nil
		}
		name, e := filepath.Rel(sessionRoot, path)
		if e != nil {
			return e
		}
		name = filepath.ToSlash(name)
		if !nativeHandoffFile(name) {
			return nil
		}
		p, e := confinedJournalPath(dataHome, "muse/sessions/"+b.Date+"/"+id+"/"+name, false)
		if e != nil {
			return e
		}
		info, e := os.Stat(p)
		if e != nil {
			return e
		}
		total += info.Size()
		if total > nativeHandoffLimit || len(b.Files) >= 4096 {
			return fmt.Errorf("Muse journal is too large")
		}
		var workspace string
		b.Files[name], workspace, e = inspectNativeJournal(p, name == "session.jsonl", id)
		if name == "session.jsonl" {
			b.Workspace = workspace
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	if err = b.Validate(); err != nil {
		return nil, err
	}
	if !pathidentity.Same(workspace, b.Workspace) && !nativeHandoffWorkspace(dataHome, id, workspace) {
		return nil, fmt.Errorf("Muse session is not bound to this workspace")
	}
	return b, nil
}

// ImportNativeSession preserves divergent copies. An existing journal must be
// a byte-for-byte ancestor; timestamps never decide which conversation wins.
func ImportNativeSession(dataHome, workspace string, b *NativeSessionBundle) error {
	if err := b.ValidateFiles(); err != nil {
		return err
	}
	destination, err := pathidentity.Resolve(workspace)
	if err != nil {
		return err
	}
	root := filepath.Join(dataHome, "muse", "sessions")
	matches, _ := filepath.Glob(filepath.Join(root, "*", "*", "*", b.SessionID, "session.jsonl"))
	want := filepath.Join(root, filepath.FromSlash(b.Date), b.SessionID, "session.jsonl")
	for _, match := range matches {
		if filepath.Clean(match) != filepath.Clean(want) {
			return fmt.Errorf("Muse session already exists at a different journal location")
		}
	}
	paths := map[string]string{}
	if _, err := os.Stat(filepath.Dir(want)); err == nil {
		if err = filepath.WalkDir(filepath.Dir(want), func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("Muse journal tree contains a link")
			}
			if d.IsDir() {
				return nil
			}
			name, e := filepath.Rel(filepath.Dir(want), path)
			if e != nil {
				return e
			}
			name = filepath.ToSlash(name)
			if nativeHandoffFile(name) {
				if _, exists := b.Files[name]; !exists {
					return fmt.Errorf("Muse session has additional local history; both copies were kept")
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for name := range b.Files {
		p, e := confinedJournalPath(dataHome, "muse/sessions/"+b.Date+"/"+b.SessionID+"/"+name, true)
		if e != nil {
			return e
		}
		if _, statErr := os.Stat(p); statErr == nil {
			old, _, e := inspectNativeJournal(p, name == "session.jsonl", b.SessionID)
			if e != nil {
				return e
			}
			if e = NativeSessionAncestor(&NativeSessionBundle{Files: map[string]NativeSessionFile{name: old}}, b); e != nil {
				return e
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		paths[name] = p
	}
	marker, err := confinedJournalPath(dataHome, "muse/sessions/"+b.Date+"/"+b.SessionID+"/agentworks-handoff.json", true)
	if err != nil {
		return err
	}
	for name, p := range paths {
		source, err := confinedJournalPath(b.Root, name, false)
		if err != nil {
			return err
		}
		if err := copyNativeHandoffFile(p, source); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Workspace string `json:"workspace"`
	}{b.SessionID, destination})
	return writeNativeHandoffFile(marker, raw)
}

func copyNativeHandoffFile(path, source string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.CreateTemp(filepath.Dir(path), ".handoff-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = io.Copy(f, in)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func writeNativeHandoffFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".handoff-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func nativeHandoffWorkspace(dataHome, id, workspace string) bool {
	path := museSessionLogPath(id, dataHome)
	if path == "" {
		return false
	}
	root := filepath.Join(dataHome, "muse", "sessions")
	rel, err := filepath.Rel(root, filepath.Join(filepath.Dir(path), "agentworks-handoff.json"))
	if err != nil {
		return false
	}
	path, err = confinedJournalPath(dataHome, "muse/sessions/"+filepath.ToSlash(rel), false)
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		return false
	}
	var marker struct {
		SessionID string `json:"session_id"`
		Workspace string `json:"workspace"`
	}
	return json.Unmarshal(raw, &marker) == nil && marker.SessionID == id && pathidentity.Same(marker.Workspace, workspace)
}

func nativeHandoffInstructions(human, instructions string) string {
	return "[AgentWorks handoff: This native session moved between computers. Earlier workspace instructions are superseded. Follow these current destination instructions:\n" + instructions + "\nEnd current destination instructions.]\n\n" + human
}
