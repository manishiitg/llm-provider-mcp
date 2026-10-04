package slotfs

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// identityHost is a host with two users, A (slot08) and B (slot09), and a docs tree with the folders each product
// launches a CLI in. It is the provider half of the PLAT-442 regression table.
type identityHost struct {
	docs string
}

func newIdentityHost(t *testing.T) identityHost {
	t.Helper()
	root := t.TempDir()
	table := filepath.Join(root, "slots.json")
	if err := os.WriteFile(table, []byte(`{"slots":{"slot08":"user-a","slot09":"user-b"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "slotctl.json")
	body := `{"slot_run_root":"` + root + `/run","slot_state_root":"` + root + `/state","docs_root":"` + root + `/docs","slot_table":"` + table + `"}`
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvConfig, cfg)
	t.Setenv(EnvEnabled, "on")
	t.Setenv(EnvUsers, "")
	return identityHost{docs: filepath.Join(root, "docs")}
}

func (h identityHost) dir(parts ...string) string {
	return filepath.Join(append([]string{h.docs}, parts...)...)
}

// identityCase is one row of the regression table: the folder a turn launches in, who the application says the
// turn runs as (owner; "" with goal means the app account), and the slot that must result.
type identityCase struct {
	name     string
	hint     string
	canary   string
	owner    string // the application's declared run-as user ("" = none)
	slot     string // the slot the application resolved for owner
	noDecl   bool   // the row is only meaningful for the path rule (no application decision exists)
	wantSlot string
}

func identityTable(h identityHost) []identityCase {
	a := func(parts ...string) string { return h.dir(append([]string{"_users", "user-a"}, parts...)...) }
	return []identityCase{
		{name: "Code, owner A's turn", hint: a("Chats", "Code", "projects", "app-1"), owner: "user-a", slot: "slot08", wantSlot: "slot08"},
		{name: "Code, a file inside the project", hint: a("Chats", "Code", "projects", "app-1", "code", "x"), owner: "user-a", slot: "slot08", wantSlot: "slot08"},
		{name: "Crew, owner A's turn", hint: a("Chats", "Work", "projects", "crew-1"), owner: "user-a", slot: "slot08", wantSlot: "slot08"},
		// B's Run-mode turn works in A's folder and the owner decided it runs as A's slot (PLAT-442 decision 1).
		{name: "Crew, reader B's Run-mode turn runs as the owner's slot", hint: a("Chats", "Work", "projects", "crew-1"), owner: "user-a", slot: "slot08", wantSlot: "slot08"},
		{name: "B's own Crew, owner B's turn", hint: h.dir("_users", "user-b", "Chats", "Work", "projects", "crew-2"), owner: "user-b", slot: "slot09", wantSlot: "slot09"},
		// Goals keep running as the app account (decision 2).
		{name: "Goal: Workflow/<name> names no user, so no slot", hint: h.dir("Workflow", "goal-1"), wantSlot: ""},
		{name: "Goal run folder", hint: h.dir("Workflow", "goal-1", "runs", "iteration-0"), wantSlot: ""},
		{name: "a user without a slot", hint: h.dir("_users", "user-c", "Chats", "Code", "projects", "p"), owner: "user-c", wantSlot: ""},
		{name: "canary lists only B: A's Code is not a slot launch", hint: a("Chats", "Code", "projects", "app-1"), canary: "user-b", owner: "user-a", slot: "slot08", wantSlot: ""},
		{name: "canary lists only B: reader B in A's Crew is not a slot launch either", hint: a("Chats", "Work", "projects", "crew-1"), canary: "user-b", owner: "user-a", slot: "slot08", wantSlot: ""},
		{name: "canary lists A: A's Crew still is", hint: a("Chats", "Work", "projects", "crew-1"), canary: "user-a", owner: "user-a", slot: "slot08", wantSlot: "slot08"},
		{name: "outside the docs root", hint: filepath.Join(filepath.Dir(h.docs), "elsewhere", "_users", "user-a", "x"), noDecl: true, wantSlot: ""},
		{name: "a logical path names no user", hint: "Chats/Code/projects/app-1", noDecl: true, wantSlot: ""},
	}
}

func declareFor(t *testing.T, tc identityCase) {
	t.Helper()
	llmtypes.ResetDeclaredRunAsForTest()
	t.Cleanup(llmtypes.ResetDeclaredRunAsForTest)
	llmtypes.DeclareRunAs(tc.hint, llmtypes.RunAs{Declared: true, User: tc.owner, Slot: tc.slot, Root: tc.hint})
}

// TestSlotOfRegressionTable is the PLAT-442 regression guard: for each product's launch folder the slot is the
// same whether the application declares the run-as identity (the real path) or the legacy folder rule decides.
func TestSlotOfRegressionTable(t *testing.T) {
	h := newIdentityHost(t)
	for _, tc := range identityTable(h) {
		t.Run("fallback/"+tc.name, func(t *testing.T) {
			llmtypes.ResetDeclaredRunAsForTest()
			t.Setenv(EnvUsers, tc.canary)
			slot, ok := SlotOf(tc.hint)
			if slot != tc.wantSlot || ok != (tc.wantSlot != "") {
				t.Fatalf("SlotOf(%q) = %q,%v, want %q", tc.hint, slot, ok, tc.wantSlot)
			}
		})
		if tc.noDecl {
			continue
		}
		t.Run("explicit/"+tc.name, func(t *testing.T) {
			t.Setenv(EnvUsers, tc.canary)
			declareFor(t, tc)
			slot, ok := SlotOf(tc.hint)
			if slot != tc.wantSlot || ok != (tc.wantSlot != "") {
				t.Fatalf("explicit SlotOf(%q) = %q,%v, want %q", tc.hint, slot, ok, tc.wantSlot)
			}
		})
	}
}

// With an explicit decision the folder is never consulted: a shared folder carries an owner's slot, and a folder
// inside a user's tree declared "app account" stays on the app account.
func TestExplicitRunAsIgnoresTheFolder(t *testing.T) {
	h := newIdentityHost(t)
	t.Cleanup(llmtypes.ResetDeclaredRunAsForTest)
	shared := h.dir("Crew", "crew-1", "code")
	llmtypes.DeclareRunAs(h.dir("Crew", "crew-1"), llmtypes.RunAs{Declared: true, User: "user-a", Slot: "slot08"})
	if slot, ok := SlotOf(shared); !ok || slot != "slot08" {
		t.Fatalf("shared Crew folder with an owner declared: %q %v", slot, ok)
	}
	inTree := h.dir("_users", "user-a", "Chats", "Work", "projects", "goalish")
	llmtypes.DeclareRunAs(inTree, llmtypes.RunAs{Declared: true})
	if slot, ok := SlotOf(inTree); ok {
		t.Fatalf("a folder declared app-account ran as %q", slot)
	}
	// The host's slot table wins over a wrong name.
	wrong := h.dir("Crew", "crew-2")
	llmtypes.DeclareRunAs(wrong, llmtypes.RunAs{Declared: true, User: "user-a", Slot: "slot09"})
	if slot, ok := SlotOf(wrong); !ok || slot != "slot08" {
		t.Fatalf("table must win: %q %v", slot, ok)
	}
	// A user with no slot is the app account even if the application names one.
	llmtypes.DeclareRunAs(h.dir("Crew", "crew-3"), llmtypes.RunAs{Declared: true, User: "user-c", Slot: "slot09"})
	if slot, ok := SlotOf(h.dir("Crew", "crew-3")); ok {
		t.Fatalf("a user without a slot ran as %q", slot)
	}
}

func TestSlotFallbackIsLoggedAndExplicitIsNot(t *testing.T) {
	h := newIdentityHost(t)
	t.Cleanup(llmtypes.ResetDeclaredRunAsForTest)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	explicit := h.dir("_users", "user-a", "Chats", "Code", "projects", "explicit-p")
	llmtypes.DeclareRunAs(explicit, llmtypes.RunAs{Declared: true, User: "user-a", Slot: "slot08"})
	if _, ok := SlotOf(explicit); !ok {
		t.Fatal("explicit launch must be a slot launch")
	}
	if strings.Contains(buf.String(), "SLOT_FALLBACK") {
		t.Fatalf("explicit launch logged a fallback: %s", buf.String())
	}
	fallback := h.dir("_users", "user-a", "Chats", "Code", "projects", "undeclared-p")
	if _, ok := SlotOf(fallback); !ok {
		t.Fatal("fallback launch must still be a slot launch")
	}
	if !strings.Contains(buf.String(), "[SLOT_FALLBACK] path-inferred slot slot08") {
		t.Fatalf("fallback not logged: %q", buf.String())
	}
}
