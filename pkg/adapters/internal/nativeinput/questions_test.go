package nativeinput

import (
	"context"
	"strings"
	"testing"
	"time"
)

// PLAT-872-21: native resolution/turn completion must close pending UI waits.
func TestRequestsCancelOnlyMatchingQuestionAndCloseRemaining(t *testing.T) {
	var r Requests
	first, second := make(chan struct{}), make(chan struct{})
	r.Start(context.Background(), "first", func(ctx context.Context) { <-ctx.Done(); close(first) })
	r.Start(context.Background(), "second", func(ctx context.Context) { <-ctx.Done(); close(second) })
	r.Cancel("first")
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("resolved question stayed pending")
	}
	select {
	case <-second:
		t.Fatal("unrelated question cancelled")
	default:
	}
	r.Close()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("turn exit left question pending")
	}
}

// PLAT-872-54: permission cards must not expose common inline credentials.
func TestCommandApprovalDisplayMasksInlineCredentials(t *testing.T) {
	for _, command := range []string{
		`API_TOKEN="private-fixture" printf ok`,
		`curl -H "Authorization: Bearer private-fixture" https://example.com`,
		`curl --token private-fixture https://example.com`,
		`tool --password='private-fixture'`,
		`mysql -u user -pprivate-fixture`,
		`curl https://user:private-fixture@example.com`,
	} {
		if got := approvalDisplay(command); strings.Contains(got, "private-fixture") || !strings.Contains(got, "[hidden]") {
			t.Fatalf("credential masking failed for %q", command)
		}
	}
	if got := approvalDisplay("printf hello"); got != "printf hello" {
		t.Fatal("ordinary command changed")
	}
}
