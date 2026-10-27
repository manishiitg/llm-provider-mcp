package cursorcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-872-50: a refused saved handle must restore history once, publish its
// replacement before sending, and keep normal streaming and later continuation.
// The only fake is the external Cursor protocol process; adapter routing is real.
func TestCursorACPInvalidLoadRestoresHistoryOnce(t *testing.T) {
	for _, refusal := range []int{-32602, -32000} {
		t.Run(fmt.Sprint(refusal), func(t *testing.T) {
			cwd, bin := t.TempDir(), t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := fmt.Sprintf(`#!/usr/bin/env python3
import sys,json,os
for line in sys.stdin:
 q=json.loads(line)
 with open('wire.jsonl','a') as f: f.write(json.dumps(q)+'\n')
 m=q.get('method'); p=q.get('params',{})
 reply={'jsonrpc':'2.0','id':q['id'],'result':{}}
 if m=='session/load' and p['sessionId']=='old-handle':
  reply.pop('result'); reply['error']={'code':%d,'message':'Invalid params'}
 elif m=='session/new': reply['result']={'sessionId':'new-handle'}
 elif m=='session/prompt':
  if not os.path.exists('published-handle'): sys.exit(3)
  print(json.dumps({'jsonrpc':'2.0','method':'session/update','params':{'sessionId':p['sessionId'],'update':{'sessionUpdate':'agent_message_chunk','content':{'type':'text','text':'OK'}}}}),flush=True)
  reply['result']={'stopReason':'end_turn'}
 print(json.dumps(reply),flush=True)
`, refusal)
			if err := os.WriteFile(filepath.Join(bin, "cursor-agent"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			owner := t.Name()
			defer closeCursorACP(owner)
			adapter := NewCursorCLIAdapter("", "composer-2.5", nil)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			history := []llmtypes.MessageContent{
				llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Remember HISTORY_SENTINEL."),
				llmtypes.TextPart(llmtypes.ChatMessageTypeAI, "Remembered."),
				llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "First follow-up."),
			}
			saved := "old-handle"
			options := []llmtypes.CallOption{WithWorkingDir(cwd), WithInteractiveSessionID(owner), WithPersistentInteractiveSession(true), func(o *llmtypes.CallOptions) {
				o.CodingProviderHistory = history
				o.OnCodingProviderSession = func(h llmtypes.CodingProviderSessionHandle) {
					saved = h.NativeSessionID
					if err := os.WriteFile(filepath.Join(cwd, "published-handle"), []byte(saved), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}}
			run := func(prompt string) (*llmtypes.ContentResponse, error) {
				stream := make(chan llmtypes.StreamChunk, 32)
				opts := append(append([]llmtypes.CallOption{}, options...), WithResumeSessionID(saved), llmtypes.WithStreamingChan(stream))
				response, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, prompt)}, opts...)
				var text strings.Builder
				for chunk := range stream {
					if chunk.Type == llmtypes.StreamChunkTypeContent {
						text.WriteString(chunk.Content)
					}
				}
				if err == nil && text.String() != "OK" {
					t.Fatalf("stream lost: %q", text.String())
				}
				return response, err
			}
			first, err := run("First follow-up.")
			if refusal == -32000 {
				if err == nil {
					t.Fatal("unrelated load error must fail")
				}
				data, _ := os.ReadFile(filepath.Join(cwd, "wire.jsonl"))
				if strings.Contains(string(data), "session/new") || strings.Contains(string(data), "session/prompt") {
					t.Fatal("refused load was retried")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(first)
			if !ok || saved != "new-handle" || handle.NativeSessionID != saved {
				t.Fatalf("replacement not retained: %+v %s", handle, saved)
			}
			closeCursorACP(owner) // force next turn through native session/load again
			if _, err = run("Second follow-up."); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(cwd, "wire.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var prompts []string
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var request struct {
					Method string `json:"method"`
					Params struct {
						Prompt []struct {
							Text string `json:"text"`
						} `json:"prompt"`
					} `json:"params"`
				}
				if err := json.Unmarshal([]byte(line), &request); err != nil {
					t.Fatal(err)
				}
				if request.Method == "session/prompt" {
					prompts = append(prompts, request.Params.Prompt[0].Text)
				}
			}
			if len(prompts) != 2 || strings.Count(prompts[0], "First follow-up.") != 1 || !strings.Contains(prompts[0], "HISTORY_SENTINEL") || strings.Contains(prompts[1], "HISTORY_SENTINEL") || prompts[1] != "Second follow-up." {
				t.Fatalf("wrong history replay: %#v", prompts)
			}
		})
	}
}
