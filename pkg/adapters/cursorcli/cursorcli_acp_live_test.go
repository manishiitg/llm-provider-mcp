package cursorcli

import (
	"context"
	"fmt"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCursorACPCancelAndReuseLive(t *testing.T) {
	requireRealCursorCLIE2E(t)
	owner := t.Name()
	defer closeCursorACP(owner)
	cwd := t.TempDir()
	marker := filepath.Join(cwd, "started.json")
	script := writeCursorSlowContractMCPServer(t, marker)
	config := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, script)
	opts := []llmtypes.CallOption{WithWorkingDir(cwd), WithInteractiveSessionID(owner), WithPersistentInteractiveSession(true), WithMCPConfig(config), WithDenyBuiltinTools(true)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a := NewCursorCLIAdapter("", "composer-2.5", nil)
	done := make(chan error, 1)
	go func() {
		_, err := a.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Call api-bridge contract_echo_token with token CANCEL_TEST. Wait for the result and report it.")}, opts...)
		done <- err
	}()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("turn ended before tool started: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	select {
	case err := <-done:
		t.Fatalf("slow tool reported completion early: %v", err)
	case <-time.After(2 * time.Second):
	}
	if err := InterruptACP(ctx, owner, "Escape"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled turn reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cancel did not settle")
	}
	next, err := a.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Do not use tools. Reply exactly AFTER_CANCEL.")}, opts...)
	if err != nil || strings.TrimSpace(next.Choices[0].Content) != "AFTER_CANCEL" {
		t.Fatalf("reuse failed: %v %v", next, err)
	}
}

func TestCursorACPParallelMCPIsolationLive(t *testing.T) {
	requireRealCursorCLIE2E(t)
	cwd := t.TempDir()
	type result struct {
		text  string
		err   error
		owner string
	}
	results := make(chan result, 2)
	for _, label := range []string{"one", "two"} {
		owner := t.Name() + label
		defer closeCursorACP(owner)
		script := writeCursorIsolationMCPServer(t, label, filepath.Join(cwd, label+".json"))
		config := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, script)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			a := NewCursorCLIAdapter("", "composer-2.5", nil)
			r, err := a.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Call api-bridge write_contract with token isolated. Reply only with its result.")}, WithWorkingDir(cwd), WithInteractiveSessionID(owner), WithPersistentInteractiveSession(true), WithMCPConfig(config), WithDenyBuiltinTools(true))
			text := ""
			if r != nil && len(r.Choices) > 0 {
				text = r.Choices[0].Content
			}
			results <- result{text, err, label}
		}()
	}
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil || strings.TrimSpace(r.text) != "ISOLATED_OK_"+r.owner+"_isolated" {
			t.Fatalf("owner %s: %q %v", r.owner, r.text, r.err)
		}
	}
}

// PLAT-872-21: same-process continuity, restart resume, no replay and scoped cleanup.
func TestCursorACPLifecycleLive(t *testing.T) {
	requireRealCursorCLIE2E(t)
	owner := t.Name()
	defer closeCursorACP(owner)
	cwd := t.TempDir()
	a := NewCursorCLIAdapter("", "composer-2.5", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	token := "ACP_MEMORY_" + cursorRandomHex(5)
	options := []llmtypes.CallOption{WithWorkingDir(cwd), WithInteractiveSessionID(owner), WithPersistentInteractiveSession(true), WithForce()}
	run := func(prompt, resume string) *llmtypes.ContentResponse {
		t.Helper()
		stream := make(chan llmtypes.StreamChunk, 4096)
		opts := append(append([]llmtypes.CallOption{}, options...), WithResumeSessionID(resume), llmtypes.WithStreamingChan(stream))
		r, err := a.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Do not read files or use tools. Follow the requested response exactly."), llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, prompt)}, opts...)
		if err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for ch := range stream {
			if ch.Type == llmtypes.StreamChunkTypeTerminal {
				t.Fatal("terminal frame")
			}
			if ch.Type == llmtypes.StreamChunkTypeContent {
				text.WriteString(ch.Content)
			}
		}
		if text.String() != r.Choices[0].Content {
			t.Fatal("stream/final mismatch")
		}
		return r
	}
	first := run("Remember "+token+". Reply exactly ACK.", "")
	h, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(first)
	if !ok || h.Transport != "acp" || h.NativeSessionID == "" || h.TmuxSession != "" {
		t.Fatalf("invalid handle %+v", h)
	}
	cursorACPSessions.Lock()
	s := cursorACPSessions.sessions[owner]
	cursorACPSessions.Unlock()
	pid := s.rpc.cmd.Process.Pid
	second := run("What token did I ask you to remember? Reply only that token.", "")
	if strings.TrimSpace(second.Choices[0].Content) != token {
		t.Fatal("lost context: " + second.Choices[0].Content)
	}
	if s.rpc.cmd.Process.Pid != pid {
		t.Fatal("warm process replaced")
	}
	closeCursorACP(owner)
	select {
	case <-s.rpc.exited:
	default:
		t.Fatal("cleanup left native process running")
	}
	third := run("Again reply only the token I asked you to remember.", h.NativeSessionID)
	if strings.TrimSpace(third.Choices[0].Content) != token {
		t.Fatal("resume lost context: " + third.Choices[0].Content)
	}
}
