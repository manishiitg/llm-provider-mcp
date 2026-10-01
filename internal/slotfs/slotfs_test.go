package slotfs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) (state, run string) {
	t.Helper()
	root := t.TempDir()
	state, run = filepath.Join(root, "state"), filepath.Join(root, "run")
	for _, d := range []string{filepath.Join(state, "slot05", "cli"), filepath.Join(run, "slot05")} {
		if err := os.MkdirAll(d, 0o770); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(root, "slotctl.json")
	body := `{"slot_run_root":"` + run + `","slot_state_root":"` + state + `"}`
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvConfig, cfg)
	t.Setenv(EnvEnabled, "on")
	return state, run
}

func TestSlotOfOnlyMatchesSlotFoldersUnderTheRoots(t *testing.T) {
	state, run := setup(t)
	if slot, ok := SlotOf(filepath.Join(state, "slot05", "cli", "x")); !ok || slot != "slot05" {
		t.Fatalf("state path: %q %v", slot, ok)
	}
	if slot, ok := SlotOf(filepath.Join(run, "slot05", "f")); !ok || slot != "slot05" {
		t.Fatalf("run path: %q %v", slot, ok)
	}
	for _, p := range []string{state, filepath.Join(state, "notaslot", "x"), filepath.Join(state, "slot5", "x"), "/tmp/slot05", "", filepath.Join(state, "..", "slot05")} {
		if _, ok := SlotOf(p); ok {
			t.Fatalf("%q must not be a slot launch", p)
		}
	}
}

func TestNothingChangesWhenTheFeatureIsOffOrUnconfigured(t *testing.T) {
	state, _ := setup(t)
	hint := filepath.Join(state, "slot05", "cli")
	t.Setenv(EnvEnabled, "")
	if IsSlotLaunch(hint) || TempDir(hint) != os.TempDir() || Mode(hint, 0o600) != 0o600 {
		t.Fatal("with the flag off a launch must be exactly as before")
	}
	t.Setenv(EnvEnabled, "on")
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "missing.json"))
	if IsSlotLaunch(hint) {
		t.Fatal("with no allow-list nothing is a slot launch")
	}
}

func TestLaunchFilesForASlotLandInItsRunFolderWithGroupAccess(t *testing.T) {
	state, run := setup(t)
	hint := filepath.Join(state, "slot05", "cli")
	f, err := CreateTemp(hint, "launch-*.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if filepath.Dir(f.Name()) != filepath.Join(run, "slot05") {
		t.Fatalf("file %s is not in the slot's run folder", f.Name())
	}
	if info, _ := f.Stat(); info.Mode().Perm() != 0o660 {
		t.Fatalf("mode %o, want 660", info.Mode().Perm())
	}
	dir, err := MkdirTemp(hint, "cfg-*")
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o770 {
		t.Fatalf("dir mode %o, want 770", info.Mode().Perm())
	}
	if Mode(hint, 0o600) != 0o660 || Mode(hint, 0o700) != 0o770 || Mode(hint, 0o644) != 0o644 {
		t.Fatalf("Mode: %o %o %o", Mode(hint, 0o600), Mode(hint, 0o700), Mode(hint, 0o644))
	}
}

func TestWrapCmdRunsTheCommandAsTheSlotWithTheRequestInAFile(t *testing.T) {
	state, run := setup(t)
	hint := filepath.Join(state, "slot05", "cli")
	cmd := exec.CommandContext(context.Background(), "/srv/agents/releases/x/bin/runner", "--config", "/c.json", "--", "/usr/bin/env", "cursor-agent")
	cmd.Dir = hint
	cmd.Env = []string{"A=1"}
	cleanup, err := WrapCmd(cmd, hint, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != sudoPath || len(cmd.Args) != 8 || cmd.Args[3] != "slot05" || cmd.Args[5] != "exec" || cmd.Args[6] != "--request-file" {
		t.Fatalf("argv %v", cmd.Args)
	}
	file := cmd.Args[7]
	if filepath.Dir(file) != filepath.Join(run, "slot05") {
		t.Fatalf("request file %s is not in the slot's run folder", file)
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), `"A=1"`) || !strings.Contains(string(data), "cursor-agent") {
		t.Fatalf("request %q err %v", data, err)
	}
	for _, e := range cmd.Env {
		if !strings.HasPrefix(e, "PATH=") {
			t.Fatalf("sudo must not receive the platform environment: %v", cmd.Env)
		}
	}
	cleanup()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("cleanup must remove the request file")
	}
	if _, err := WrapCmd(exec.CommandContext(context.Background(), "/bin/true"), "/tmp/elsewhere", nil); !errors.Is(err, ErrNotSlot) {
		t.Fatalf("a launch outside the slots must not be wrapped: %v", err)
	}
}

func TestAUsersOwnTreeIsTheirSlotLaunch(t *testing.T) {
	root := t.TempDir()
	table := filepath.Join(root, "slots.json")
	if err := os.WriteFile(table, []byte(`{"slots":{"slot08":"user-a"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "slotctl.json")
	body := `{"slot_run_root":"` + root + `/run","slot_state_root":"` + root + `/state","docs_root":"` + root + `/docs","slot_table":"` + table + `"}`
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvConfig, cfg)
	t.Setenv(EnvEnabled, "on")
	if slot, ok := SlotOf(root + "/docs/_users/user-a/Chats/Code/projects/p"); !ok || slot != "slot08" {
		t.Fatalf("own tree: %q %v", slot, ok)
	}
	if _, ok := SlotOf(root + "/docs/_users/user-b/Chats/Code/projects/p"); ok {
		t.Fatal("a user without a slot has no slot launch")
	}
	if _, ok := SlotOf(root + "/docs/Workflow/x"); ok {
		t.Fatal("shared folders are not anyone's tree")
	}
}

func TestCanaryLimitsWhichUsersLaunchAsSlots(t *testing.T) {
	root := t.TempDir()
	table := filepath.Join(root, "slots.json")
	_ = os.WriteFile(table, []byte(`{"slots":{"slot01":"user-a","slot02":"user-b"}}`), 0o600)
	cfg := filepath.Join(root, "slotctl.json")
	_ = os.WriteFile(cfg, []byte(`{"slot_run_root":"`+root+`/run","slot_state_root":"`+root+`/state","docs_root":"`+root+`/docs","slot_table":"`+table+`"}`), 0o644)
	t.Setenv(EnvConfig, cfg)
	t.Setenv(EnvEnabled, "on")
	t.Setenv(EnvUsers, "user-b")
	if IsSlotLaunch(root + "/docs/_users/user-a/x") {
		t.Fatal("a user outside the canary list must not launch as a slot")
	}
	if !IsSlotLaunch(root + "/docs/_users/user-b/x") {
		t.Fatal("a listed user must launch as a slot")
	}
	t.Setenv(EnvUsers, "")
	if !IsSlotLaunch(root + "/docs/_users/user-a/x") {
		t.Fatal("with no list every slot holder launches as a slot")
	}
}

// A product with its own slot accounts on a shared host (prefix "cf") recognises only its own, and a
// default-prefix account is not one of its slots.
func TestSlotOfHonoursTheProductsSlotPrefix(t *testing.T) {
	root := t.TempDir()
	state, run := filepath.Join(root, "state"), filepath.Join(root, "run")
	cfg := filepath.Join(root, "slotctl.json")
	body := `{"slot_prefix":"cf","slot_run_root":"` + run + `","slot_state_root":"` + state + `"}`
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvConfig, cfg)
	t.Setenv(EnvEnabled, "on")
	if slot, ok := SlotOf(filepath.Join(state, "cf07", "cli", "x")); !ok || slot != "cf07" {
		t.Fatalf("own slot: %q %v", slot, ok)
	}
	if _, ok := SlotOf(filepath.Join(state, "slot07", "cli", "x")); ok {
		t.Fatal("another product's slot name must not be recognised")
	}
	t.Setenv(EnvSlotctl, "/usr/local/libexec/agentworks/confida/slotctl")
	if slotctl() != "/usr/local/libexec/agentworks/confida/slotctl" {
		t.Fatal("the slot program override is ignored")
	}
}
