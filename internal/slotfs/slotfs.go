// Package slotfs lets the coding-CLI launch code tell that a launch belongs to a user's own Linux
// account (a "slot", provisioned by the builder's provision-slots.sh) and prepare it accordingly.
//
// A launch is a slot launch when its working folder lies in a slot's state folder,
// <state root>/<slot>/..., as set by the agent server. Nothing else changes for hosts without
// slots: with no allow-list file, or with AGENTWORKS_SLOT_CLI off, every function here reduces to
// what the code did before (os.TempDir, owner-only modes, no sudo).
//
// The roots come from the root-owned allow-list that slotctl also reads, so the platform cannot point
// them elsewhere. The request format in WrapCmd mirrors the builder's workspace/slots.ExecRequest.
package slotfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// EnvEnabled turns CLI launches as slots on ("on").
	EnvEnabled = "AGENTWORKS_SLOT_CLI"
	// ConfigPath is the root-owned allow-list written by provision-slots.sh.
	ConfigPath = "/usr/local/libexec/agentworks/slotctl.json"
	// EnvUsers limits CLI launches as slots to these user ids (comma-separated) while a rollout is
	// tested; empty means every user who holds a slot.
	EnvUsers = "AGENTWORKS_SLOT_CLI_USERS"
	// EnvConfig overrides ConfigPath (tests).
	EnvConfig = "AGENTWORKS_SLOTCTL_CONFIG"

	sudoPath    = "/usr/bin/sudo"
	slotctlPath = "/usr/local/libexec/agentworks/slotctl"
	// EnvSlotctl overrides slotctlPath: a product that has its own slot accounts on a shared host.
	EnvSlotctl = "AGENTWORKS_SLOTCTL"
	// EnvPrefix names the slot accounts of this product (default "slot": slot01, slot02, ...).
	EnvPrefix = "AGENTWORKS_SLOT_PREFIX"
)

// slotctl is the root-owned launcher this product's service account may run as a slot.
func slotctl() string {
	if override := strings.TrimSpace(os.Getenv(EnvSlotctl)); override != "" {
		return override
	}
	return slotctlPath
}

var (
	nameMu    sync.Mutex
	nameCache = map[string]*regexp.Regexp{}
)

// slotNameMatches reports whether name is one of this product's slot accounts: the product's prefix (the
// config's slot_prefix, else AGENTWORKS_SLOT_PREFIX, else "slot") followed by two or three digits.
func slotNameMatches(name string) bool {
	prefix := strings.TrimSpace(os.Getenv(EnvPrefix))
	if cfg, ok := loadConfigUnchecked(); ok && cfg.SlotPrefix != "" {
		prefix = cfg.SlotPrefix
	}
	if prefix == "" {
		prefix = "slot"
	}
	nameMu.Lock()
	re, ok := nameCache[prefix]
	if !ok {
		re = regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + "[0-9]{2,3}$")
		nameCache[prefix] = re
	}
	nameMu.Unlock()
	return re.MatchString(name)
}

type config struct {
	SlotPrefix    string `json:"slot_prefix"`
	SlotRunRoot   string `json:"slot_run_root"`
	SlotStateRoot string `json:"slot_state_root"`
	DocsRoot      string `json:"docs_root"`
	SlotTable     string `json:"slot_table"`
}

func loadConfig() (config, bool) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv(EnvEnabled)), "on") {
		return config{}, false
	}
	return loadConfigUnchecked()
}

// loadConfigUnchecked reads the allow-list whether or not CLI launches as slots are switched on.
func loadConfigUnchecked() (config, bool) {
	path := ConfigPath
	if override := strings.TrimSpace(os.Getenv(EnvConfig)); override != "" {
		path = override
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, false
	}
	var cfg config
	if json.Unmarshal(data, &cfg) != nil || cfg.SlotRunRoot == "" || cfg.SlotStateRoot == "" {
		return config{}, false
	}
	return cfg, true
}

// SlotOf returns the slot a path belongs to (a path inside <state root>/<slot> or <run root>/<slot>).
func SlotOf(path string) (slot string, ok bool) {
	cfg, on := loadConfig()
	if !on || strings.TrimSpace(path) == "" {
		return "", false
	}
	clean := filepath.Clean(path)
	for _, root := range []string{cfg.SlotStateRoot, cfg.SlotRunRoot} {
		prefix := filepath.Clean(root) + string(filepath.Separator)
		if strings.HasPrefix(clean, prefix) {
			first := strings.SplitN(strings.TrimPrefix(clean, prefix), string(filepath.Separator), 2)[0]
			if slotNameMatches(first) {
				return first, true
			}
		}
	}
	// A user's own tree, <docs root>/_users/<user id>/..., belongs to the slot that user holds.
	if cfg.DocsRoot != "" && cfg.SlotTable != "" {
		prefix := filepath.Join(filepath.Clean(cfg.DocsRoot), "_users") + string(filepath.Separator)
		if strings.HasPrefix(clean, prefix) {
			userID := strings.SplitN(strings.TrimPrefix(clean, prefix), string(filepath.Separator), 2)[0]
			if canaryAllows(userID) {
				if slot := slotOfUser(cfg.SlotTable, userID); slot != "" {
					return slot, true
				}
			}
		}
	}
	return "", false
}

func canaryAllows(userID string) bool {
	list := strings.TrimSpace(os.Getenv(EnvUsers))
	if list == "" {
		return true
	}
	for _, id := range strings.Split(list, ",") {
		if strings.TrimSpace(id) == userID {
			return true
		}
	}
	return false
}

func slotOfUser(tablePath, userID string) string {
	data, err := os.ReadFile(tablePath)
	if err != nil || userID == "" {
		return ""
	}
	var table struct {
		Slots map[string]string `json:"slots"`
	}
	if json.Unmarshal(data, &table) != nil {
		return ""
	}
	for slot, holder := range table.Slots {
		if holder == userID && slotNameMatches(slot) {
			return slot
		}
	}
	return ""
}

// RunDir is a slot's run folder: group-writable for the platform, where launch files for that slot go.
func RunDir(slot string) (string, bool) {
	cfg, on := loadConfig()
	if !on || !slotNameMatches(slot) {
		return "", false
	}
	return filepath.Join(cfg.SlotRunRoot, slot), true
}

// IsSlotLaunch reports whether hint (a launch's working or home folder) belongs to a slot.
func IsSlotLaunch(hint string) bool {
	_, ok := SlotOf(hint)
	return ok
}

// TempDir is where launch files for hint go: the slot's run folder for a slot launch, else os.TempDir().
func TempDir(hint string) string {
	if slot, ok := SlotOf(hint); ok {
		if dir, ok := RunDir(slot); ok {
			return dir
		}
	}
	return os.TempDir()
}

// Mode widens an owner-only mode to the owner's group for a slot launch (0600 -> 0660, 0700 ->
// 0770): the slot reaches these files through its group and nobody else can. A mode that already
// grants group or other access is left alone.
func Mode(hint string, mode os.FileMode) os.FileMode {
	if !IsSlotLaunch(hint) || mode&0o077 != 0 {
		return mode // not a slot launch, or already shared on purpose
	}
	return mode | (mode&0o700)>>3
}

// CreateTemp is os.CreateTemp for hint's launch files, readable by the slot when it is one.
func CreateTemp(hint, pattern string) (*os.File, error) {
	f, err := os.CreateTemp(TempDir(hint), pattern)
	if err != nil {
		return nil, err
	}
	if IsSlotLaunch(hint) {
		if err := f.Chmod(0o660); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return nil, err
		}
	}
	return f, nil
}

// MkdirTemp is os.MkdirTemp for hint's launch files, enterable by the slot when it is one.
func MkdirTemp(hint, pattern string) (string, error) {
	dir, err := os.MkdirTemp(TempDir(hint), pattern)
	if err != nil {
		return "", err
	}
	if IsSlotLaunch(hint) {
		if err := os.Chmod(dir, 0o770|os.ModeSetgid); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

type request struct {
	Argv   []string `json:"argv"`
	Cwd    string   `json:"cwd"`
	Env    []string `json:"env"`
	Userns bool     `json:"userns,omitempty"`
	FD3    string   `json:"fd3,omitempty"`
}

// ErrNotSlot is returned by WrapCmd for a launch that does not belong to a slot.
var ErrNotSlot = errors.New("not a slot launch")

// WrapCmd rewrites cmd, in place, to run as the slot of hint through sudo and slotctl. The request
// (program, folder, environment) is left in a file in the slot's run folder, so the command keeps
// its own standard input and output. env is the complete environment; when nil a minimal one is used
// (cmd.Env nil would otherwise mean "the platform's whole environment"). It returns the cleanup that
// removes the request file if the command never starts.
func WrapCmd(cmd *exec.Cmd, hint string, env []string) (cleanup func(), err error) {
	slot, ok := SlotOf(hint)
	if !ok {
		return func() {}, ErrNotSlot
	}
	runDir, _ := RunDir(slot)
	if cmd == nil || len(cmd.Args) == 0 {
		return func() {}, errors.New("empty command")
	}
	if env == nil {
		env = cmd.Env
	}
	if env == nil {
		env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	}
	cwd := cmd.Dir
	if cwd == "" {
		cwd = runDir
	}
	body, err := json.Marshal(request{Argv: append([]string(nil), cmd.Args...), Cwd: cwd, Env: env})
	if err != nil {
		return func() {}, err
	}
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return func() {}, err
	}
	file := filepath.Join(runDir, "req-"+hex.EncodeToString(token)+".json")
	if err := os.WriteFile(file, body, 0o660); err != nil {
		return func() {}, fmt.Errorf("write the slot launch request: %w", err)
	}
	if err := os.Chmod(file, 0o660); err != nil {
		_ = os.Remove(file)
		return func() {}, err
	}
	cmd.Path = sudoPath
	cmd.Args = []string{sudoPath, "-n", "-u", slot, slotctl(), "exec", "--request-file", file}
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Dir = "/"
	return func() { _ = os.Remove(file) }, nil
}

var grantMu sync.Mutex

func setfacl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "setfacl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("setfacl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// traverse lets slot walk to path through every folder above it that is not already world-searchable,
// by an access entry for that slot alone (no other slot gains anything).
func traverse(slot, path string) error {
	var dirs []string
	for dir := filepath.Dir(filepath.Clean(path)); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		dirs = append([]string{dir}, dirs...)
	}
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil && info.Mode().Perm()&0o001 != 0 {
			continue
		}
		if groupOwns(slot, dir) {
			// The slot's group already has the folder (its own project or state folder). A named entry
			// for the slot would override that group access with search-only, so none is added, and
			// one left by an earlier launch is removed.
			_ = setfacl("-x", "u:"+slot, dir)
			continue
		}
		if err := setfacl("-m", "u:"+slot+":--x", dir); err != nil {
			return err
		}
	}
	return nil
}

// groupOwns reports whether dir belongs to the slot's own group with group search access.
func groupOwns(slot, dir string) bool {
	group, err := user.LookupGroup(slot)
	if err != nil {
		return false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o010 == 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && strconv.Itoa(int(st.Gid)) == group.Gid
}

// GrantAccess gives one slot read and write on a shared file the platform owns (a CLI login that
// rotates its tokens and so cannot be copied), and lets it reach the file. Done once per slot and path.
func GrantAccess(slot, path string) error {
	return grant(slot, path, true)
}

// GrantRuntime gives one slot access to a file or folder the platform prepared for a launch (MCP
// config, hooks, status file): read, plus write when the CLI writes there. A missing file the CLI
// writes is created empty first. Something already readable by everyone needs nothing but a way in.
func GrantRuntime(slot, path string, write bool) error {
	return grant(slot, path, write)
}

func grant(slot, path string, write bool) error {
	if !slotNameMatches(slot) {
		return fmt.Errorf("invalid slot %q", slot)
	}
	clean := filepath.Clean(path)
	// Applied at every launch, never cached: an adapter that re-tightens its folder (chmod 0700) resets
	// the ACL mask and cancels the entry, so a remembered grant would be wrong.
	grantMu.Lock()
	defer grantMu.Unlock()
	info, err := os.Stat(clean)
	if os.IsNotExist(err) && write {
		f, cerr := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY, 0o600)
		if cerr != nil {
			return nil // the adapter creates it; nothing to grant yet
		}
		_ = f.Close()
		info, err = os.Stat(clean)
	}
	if err != nil {
		return nil // nothing there to grant (a path granted "in case")
	}
	if err := traverse(slot, clean); err != nil {
		return err
	}
	perm := "r"
	if write {
		perm = "rw"
	}
	if info.IsDir() {
		perm = strings.Replace(perm, "r", "rX", 1)
		if write {
			perm = "rwX"
		}
		if !write && info.Mode().Perm()&0o005 == 0o005 {
			return nil // a folder everyone can read and enter
		}
		if err := setfacl("-R", "-m", "u:"+slot+":"+perm, clean); err != nil {
			return err
		}
		if err := setfacl("-R", "-d", "-m", "u:"+slot+":"+perm, clean); err != nil {
			return err
		}
	} else {
		if !write && info.Mode().Perm()&0o004 != 0 {
			return nil
		}
		if err := setfacl("-m", "u:"+slot+":"+perm, clean); err != nil {
			return err
		}
	}
	return nil
}

// On reports whether launches as slots are switched on for this process at all (the flag; the
// allow-list and the user decide whether a given launch is one).
func On() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvEnabled)), "on") }

// ShareTree gives a slot launch's group full use of a tree the platform just prepared (a CLI's
// config folder): the folder's files are made group-owned by the slot's group and group read-write
// (and group-searchable for folders), with the setgid bit on folders so new files stay in that group.
// Nothing for a launch that is not a slot's.
func ShareTree(hint, root string) error {
	slot, ok := SlotOf(hint)
	if !ok {
		return nil
	}
	group, err := user.LookupGroup(slot)
	if err != nil {
		return fmt.Errorf("group %s: %w", slot, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if err := os.Lchown(path, -1, gid); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() | 0o060
		if info.IsDir() {
			return os.Chmod(path, mode|0o010|os.ModeSetgid)
		}
		if info.Mode().Perm()&0o100 != 0 {
			mode |= 0o010
		}
		return os.Chmod(path, mode)
	})
}
