package nativereplies

import "testing"

func TestNativeReplyOwnershipSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	if err := Configure(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Configure("") })
	for _, first := range []string{Foreground, Observer} {
		root := "root-" + first
		if owner, err := Claim("claude-code", "native", root, first); err != nil || owner != first {
			t.Fatalf("first=%s owner=%s err=%v", first, owner, err)
		}
		other := Observer
		if first == Observer {
			other = Foreground
		}
		if owner, err := Claim("claude-code", "native", root, other); err != nil || owner != first {
			t.Fatalf("second stole root: %s/%v", owner, err)
		}
	}
	if err := SettleForeground("claude-code", "native", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim("claude-code", "native", "unfinished", Foreground); err != nil {
		t.Fatal(err)
	}
	if err := Configure(dir); err != nil {
		t.Fatal(err)
	}
	if s := State("claude-code", "native", "root-foreground"); s.Owner != Foreground || !s.Committed {
		t.Fatal("committed root lost ownership")
	}
	if s := State("claude-code", "native", "unfinished"); s.Owner != Observer || s.Committed {
		t.Fatal("unfinished root was stranded")
	}
	if _, err := Claim("claude-code", "native", "failed", Foreground); err != nil {
		t.Fatal(err)
	}
	if err := SettleForeground("claude-code", "native", false); err != nil {
		t.Fatal(err)
	}
	if State("claude-code", "native", "failed").Owner != Observer {
		t.Fatal("failed turn retained unpublished root")
	}
}

func TestClaudeNativeReplyLineage(t *testing.T) {
	var c ClaudeLineage
	for _, row := range []string{
		`{"type":"user","uuid":"human","sessionId":"native","origin":{"kind":"human"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"human"}`,
		`{"type":"user","uuid":"stop","parentUuid":"a1","origin":{"kind":"task-notification"},"queueTranscriptOnly":true}`,
	} {
		if c.Observe([]byte(row)).Helper {
			t.Fatal("stopped notification started a turn")
		}
	}
	c.Observe([]byte(`{"type":"user","uuid":"notice","sessionId":"native","parentUuid":"stop","origin":{"kind":"task-notification"},"turnOrigin":"task_notification"}`))
	root := c.Observe([]byte(`{"type":"assistant","uuid":"a2","parentUuid":"notice"}`))
	if root.Root != "notice" || !root.Helper || root.Session != "native" {
		t.Fatalf("wrong lineage: %+v", root)
	}
	if c.Observe([]byte(`{"type":"assistant","uuid":"late","parentUuid":"a1"}`)).Helper {
		t.Fatal("interleaved foreground branch was misattributed")
	}
}
