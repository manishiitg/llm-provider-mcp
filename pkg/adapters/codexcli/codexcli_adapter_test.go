package codexcli

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (l *MockLogger) Infof(format string, args ...any)  { fmt.Printf("INFO: "+format+"\n", args...) }
func (l *MockLogger) Errorf(format string, args ...any) { fmt.Printf("ERROR: "+format+"\n", args...) }
func (l *MockLogger) Debugf(format string, args ...interface{}) {
	fmt.Printf("DEBUG: "+format+"\n", args...)
}

// Keep the real CLI transport contract on the cheaper Spark model; model-tier
// defaults are tested separately from the tmux protocol itself.
const codexCLIRealContractModel = "gpt-5.3-codex-spark"

func TestCodexCLIAdapterImplementsWebSearchModel(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
	if _, ok := interface{}(adapter).(llmtypes.WebSearchModel); !ok {
		t.Fatal("CodexCLIAdapter should implement llmtypes.WebSearchModel")
	}
}

func TestCodexInteractiveStreamTmuxScreenFlag(t *testing.T) {
	t.Setenv(EnvCodexInteractiveStreamTmuxScreen, "")
	if !codexInteractiveStreamTmuxScreenEnabled() {
		t.Fatal("tmux screen streaming should be enabled by default")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(EnvCodexInteractiveStreamTmuxScreen, value)
		if !codexInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be enabled for %q", value)
		}
	}

	for _, value := range []string{"0", "false", "FALSE", "no", "off"} {
		t.Setenv(EnvCodexInteractiveStreamTmuxScreen, value)
		if codexInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be disabled for %q", value)
		}
	}
}

func TestCodexInteractiveShellCommandUsesCallerWorkingDir(t *testing.T) {
	got := codexInteractiveShellCommand([]string{"codex", "--no-alt-screen"}, "/tmp/user chat")
	if !strings.HasPrefix(got, "cd '/tmp/user chat' && exec ") {
		t.Fatalf("shell command = %q, want caller cwd before exec", got)
	}
	if strings.Contains(got, "--cd") {
		t.Fatalf("shell command = %q, interactive cwd must not rely on --cd", got)
	}
}

func TestCodexBridgeOnlyDisablesPluginAndDummyToolSurfaces(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "gpt-5.3-codex-spark", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithDisableShellTool()(opts)

	args, systemPromptFile, err := adapter.buildCodexInteractiveArgs(opts, "")
	if err != nil {
		t.Fatalf("buildCodexInteractiveArgs error = %v", err)
	}
	if systemPromptFile != "" {
		t.Fatalf("systemPromptFile = %q, want empty", systemPromptFile)
	}

	for _, feature := range []string{"plugins", "unavailable_dummy_tools"} {
		if !codexArgsContainPair(args, "--disable", feature) {
			t.Fatalf("args missing --disable %s: %v", feature, args)
		}
	}
}

func TestWriteCodexImageContentFilesFromBase64(t *testing.T) {
	raw := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	tempDir, paths, err := writeCodexImageContentFiles([]llmtypes.ImageContent{
		{
			SourceType: "base64",
			MediaType:  "image/png",
			Data:       base64.StdEncoding.EncodeToString(raw),
		},
	})
	if err != nil {
		t.Fatalf("writeCodexImageContentFiles() error = %v", err)
	}
	defer os.RemoveAll(tempDir)

	if len(paths) != 1 {
		t.Fatalf("paths = %v, want one image path", paths)
	}
	if !strings.HasSuffix(paths[0], ".png") {
		t.Fatalf("image path = %q, want .png extension", paths[0])
	}
	got, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("read image file: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("image bytes = %v, want %v", got, raw)
	}
}

func codexArgsContainPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestWriteCodexImageContentFilesRejectsURL(t *testing.T) {
	_, _, err := writeCodexImageContentFiles([]llmtypes.ImageContent{
		{SourceType: "url", Data: "https://example.com/image.png"},
	})
	if err == nil {
		t.Fatal("writeCodexImageContentFiles() error = nil, want unsupported URL error")
	}
	if !strings.Contains(err.Error(), "image URLs are not supported") {
		t.Fatalf("error = %v, want unsupported URL error", err)
	}
}

func TestCodexCLIInteractiveRejectsImageContent(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})

	_, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Describe this image."},
				llmtypes.ImageContent{SourceType: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="},
			},
		},
	}, WithInteractiveSessionID("codex-image-test"), WithPersistentInteractiveSession(true))
	if err == nil {
		t.Fatal("GenerateContent() error = nil, want unsupported interactive image error")
	}
	if !strings.Contains(err.Error(), "interactive transport does not support llmtypes.ImageContent") {
		t.Fatalf("GenerateContent() error = %v, want interactive image unsupported error", err)
	}
}

func TestCodexCLIInteractiveIntegrationSpark(t *testing.T) {
	if os.Getenv("RUN_CODEX_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_CODEX_CLI_INTERACTIVE_E2E=1 to run real Codex CLI interactive tmux E2E")
	}
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ownerSessionID := "codex-interactive-e2e-" + codexRandomHex(4)
	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithDisableShellTool(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
	}

	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Keep answers short."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Remember the token CODEX_TMUX_OK_4821. Reply exactly: saved CODEX_TMUX_OK_4821"}}},
	}, options...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	if got := first.Choices[0].Content; !strings.Contains(got, "CODEX_TMUX_OK_4821") {
		t.Fatalf("first content = %q, want token", got)
	}

	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What token did I ask you to remember? Reply with only the token."}}},
	}, options...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	if got := second.Choices[0].Content; !strings.Contains(got, "CODEX_TMUX_OK_4821") {
		t.Fatalf("second content = %q, want token from same tmux session", got)
	}
}

func TestExtractCodexVisibleAssistantTextFiltersTUIProgress(t *testing.T) {
	input := `
▐▛███▜▌ Codex
Thinking with high effort · esc to interrupt
Calling api-bridge… (ctrl+o to expand)
Press Ctrl+O to expand pasted text
Let me check the plan and summarize it.
Called api-bridge 2 times (ctrl+o to expand)
Here are the steps:
1. Prepare fixtures
2. Run the probes
❯
`
	got := extractCodexVisibleAssistantText(input)
	want := "Let me check the plan and summarize it.\nHere are the steps:\n1. Prepare fixtures\n2. Run the probes"
	if got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

func TestStripCodexHistoricalAssistantTextRemovesPaneReplay(t *testing.T) {
	previous := `Hello! I'm your Workflow Builder agent. I'm currently in the testing
workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?`

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "full previous response before new answer",
			text: previous + "\nYes, I do! A message sequence is ordered.",
			want: "Yes, I do! A message sequence is ordered.",
		},
		{
			name: "suffix of previous response before new answer",
			text: `workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?
Yes, I do! A message sequence is ordered.`,
			want: "Yes, I do! A message sequence is ordered.",
		},
		{
			name: "only historical suffix",
			text: `workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripCodexHistoricalAssistantText(tt.text, []string{previous})
			if got != tt.want {
				t.Fatalf("stripped = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripCodexEchoedUserPromptKeepsAssistantAnswer(t *testing.T) {
	token := "REAL_CODEX_TMUX_abc123"
	prompt := fmt.Sprintf(`This is a real Codex CLI tmux contract test.

Preserve input safely:

blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Reply exactly:
saved %s`, token, token)
	visible := fmt.Sprintf(`│ >_ OpenAI Codex (v0.130.0)                            │
│ directory: ~/ai-work/…/pkg/adapters/codexcli          │
Tip: New Use /fast to enable our fastest inference with increased plan usage.
Preserve input safely:
blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते
Reply exactly:
saved %s
saved %s
gpt-5.3-codex-spark low · ~/ai-work/multi-llm-provider-go/pkg/adapters/codexc…`, token, token, token)

	filtered := extractCodexVisibleAssistantText(visible)
	got := stripCodexEchoedUserPrompt(filtered, prompt)
	want := "saved " + token
	if got != want {
		t.Fatalf("stripped prompt = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsLiveInputEcho(t *testing.T) {
	visible := `sent immediately)
↳ hmm
Actual answer after the live input.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Actual answer after the live input."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsCodexLandingURL(t *testing.T) {
	visible := `https://chatgpt.com/codex?app-landing-page=true
Here are the current top-level steps in the plan.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Here are the current top-level steps in the plan."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsToolStatusReplay(t *testing.T) {
	visible := `Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Searching the web
Searched https://example.com/
Called workspace.list_mcp_resources({"cursor":null,"server":"workspace"})
└ Error: resources/list failed: unknown MCP server 'workspace'
Called codex.list_mcp_resource_templates({})
└ {"resourceTemplates": []}
Called
└ workflow.read_mcp_resource({"server":"workflow","uri":"planning/plan.json"})
Error: resources/read failed: unknown MCP server 'workflow'
Updated Plan
└ quick check
✔ test
Updated Plan
└ □ try
Spawned Dalton (gpt-5.3-codex-spark high)
└ ping
Waiting for Dalton
Finished waiting
└ Dalton: Completed - pong
Now I will run the workflow step.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Now I will run the workflow step."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
}

func TestExtractCodexVisibleAssistantTextDropsFlattenedToolStatusReplay(t *testing.T) {
	visible := `Hi! 👋 What would you like me to do for the workflow today? Called codex.list_mcp_resources({}) └ {"resources": []} Updated Plan └ Need confirm target group and step before running. □ Gather available groups and step IDs. □ Request step/group details from user. Called codex.list_mcp_resource_templates({}) └ {"resourceTemplates": []} Called └ workflow.read_mcp_resource({"server":"workflow","uri":"planning/plan.json"}) Error: resources/read failed: unknown MCP server 'workflow' Updated Plan └ Fetching available context before running requested step. ✔ Gather available groups and step IDs. □ Request step/group details from user. Updated Plan └ Need to identify valid groups and steps before execution. ✔ Gather available groups and step IDs. ✔ Request step/group details from user.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Hi! 👋 What would you like me to do for the workflow today?"
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestExtractCodexVisibleAssistantTextDropsToolReplayFragments(t *testing.T) {
	visible := `ver"})
environment.
□ Check current model auth status
bridge.get_api_spec({"server_name":"llm_config_tools","tool_name":"list_llm_capabilities"})
base: http://127.0.0.1:18743/s/session-id
auth: Bearer $MCP_API_TOKEN
POST /tools/custom/list_llm_capabilities
# List supported and currently usable LLM providers/models by capability.
tored. Supports optional provider override, aspect ratio, resolution, number of im...
mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-model-test && curl -sS -X POST "$MCP_API_URL/tools/custom/image_gen" -H "Authorization: Bearer $MCP_API_TOKEN" -H "Content-Type: application/json" -d '{"provider":"vertex","model_id":"gemini-3.1-flash-image-preview","prompt":"A calm cyberpunk city skyline","aspect_ratio":"16:9","resolution":"1K","number_of_images":1,"output_path":"_users/default/Chats/image-model-test/vertex_test.png"}'"})
{"stdout": "", "stderr": "mkdir: /Users/mipl/ai-work/mcp-agent-builder-go: Operation not permitted\n", "exit_code": 1, "execution_time_ms": 30}
32
-rw-r--r--@ 1 mipl staff 0 30 Apr 15:42 _index.json
drwxr-xr-x@ 3 mipl staff 96 9 May 19:55 _system
&& ls -l Chats/test.txt"})
{"stdout": "", "stderr": "touch: Chats/test.txt: Operation not permitted\n", "exit_code": 1, "execution_time_ms": 27}
Here is the actual answer.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Here is the actual answer."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestExtractCodexVisibleAssistantTextDropsModelCatalogAndShellReplay(t *testing.T) {
	visible := `catalog the frontend uses from /api/llm-config/models/metadata.
provider: string (required)
relative output_path so the caller decides exactly where the generated image is stored.
json, requests
base='http://127.0.0.1:18743/s/session-id'
url=base+'/tools/custom/list_provider_models'
headers={'Authorization':'Bearer '+os.environ['MCP_API_TOKEN'],'Content-Type':'application/json'}
for p in ['codex-cli','minimax-coding-plan','vertex','openai']:
 r=requests.post(url,headers=headers,json={'provider':p},timeout=60)
 print(json.dumps(r.json(),indent=2)[:2000])
"http://127.0.0.1:18743/s/session-id/tools/custom/list_provider_models"
{
  "count": 4,
  "models": [
    {
      "model_id": "pricing varies)",
      "context_window": 200000,
      "input_cost_per_1m": 0,
      "output_cost_per_1m": 0
    }
  ]
}
absolute host path (/Users/mipl/.codex/skills/.system/imagegen/SKILL.md) docs, /workspace-docs. Did you mean: /Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/skills/.system/imagegen/SKILL.md?
model-test && for m in low medium high; do
  echo "Generating with model: $m"
  payload='{"provider":"codex-cli","model_id":"'$m'","prompt":"A futuristic neon cityscape","aspect_ratio":"16:9","output_path":"Chats/image-model-test/'"$m"'.png"}'
32
-rw-r--r--@ 1 mipl staff 0 30 Apr 15:42 _index.json
drwxr-xr-x@ 3 mipl staff 96 9 May 19:55 _system
Actual concise answer.`

	got := extractCodexVisibleAssistantText(visible)
	want := "Actual concise answer."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestExtractCodexVisibleAssistantTextDropsBulletedGenericMCPResourceReplay(t *testing.T) {
	visible := `Generating...
• Called list.read_mcp_resource({"server":"list","uri":"bad"})
Error: resources/read failed: unknown MCP server 'list'
• • •
I need the step name or group before I can run it.`

	got := extractCodexVisibleAssistantText(visible)
	want := "I need the step name or group before I can run it."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestNormalizeCodexPaneSnapshotSegmentsAssistantAndStatusBlocks(t *testing.T) {
	raw := `Hi, I can help with that.
Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Updated Plan
└ Need valid step details.
□ Ask user for step.
• Called list.read_mcp_resource({"server":"list","uri":"bad"})
Error: resources/read failed: unknown MCP server 'list'
Now I need the step name before running it.`

	snapshot := normalizeCodexPaneSnapshot(raw)
	wantAssistant := `Hi, I can help with that.
Now I need the step name before running it.`
	if snapshot.AssistantText != wantAssistant {
		t.Fatalf("assistant text = %q, want %q", snapshot.AssistantText, wantAssistant)
	}
	wantKinds := []codexSegmentKind{
		codexSegmentAssistant,
		codexSegmentToolStatus,
		codexSegmentPlanStatus,
		codexSegmentToolStatus,
		codexSegmentAssistant,
	}
	if len(snapshot.Segments) != len(wantKinds) {
		t.Fatalf("segments = %#v, want %d segments", snapshot.Segments, len(wantKinds))
	}
	for i, want := range wantKinds {
		if snapshot.Segments[i].Kind != want {
			t.Fatalf("segment %d kind = %q, want %q; segments=%#v", i, snapshot.Segments[i].Kind, want, snapshot.Segments)
		}
	}
	if snapshot.Fingerprint == "" || strings.Contains(snapshot.Fingerprint, "read_mcp_resource") {
		t.Fatalf("fingerprint = %q, want assistant-only fingerprint", snapshot.Fingerprint)
	}
}

func TestParseCodexInteractiveResponseDropsInternalToolReplay(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
Calling codex.list_mcp_resources({"cursor":null})
Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Searching the web
Searched https://example.com/
Called workspace.list_mcp_resources({"cursor":null,"server":"workspace"})
└ Error: resources/list failed: unknown MCP server 'workspace'
Updated Plan
└ quick check
✔ test
Called
└ workflow.read_mcp_resource({"server":"workflow","uri":"planning/plan.json"})
Error: resources/read failed: unknown MCP server 'workflow'
Here are the current top-level steps in the plan:
1. Prepare Regression Fixtures
2. Forbidden Access Probe
3. Execution Regression Router
›`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Here are the current top-level steps in the plan:
1. Prepare Regression Fixtures
2. Forbidden Access Probe
3. Execution Regression Router`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseKeepsOnlyFinalAnswerAfterShellReplay(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
this step's allowed folders. Allowed: _users/default/Chats/, _users/
default/chat_history/, skills/, subagents/, Downloads/, Workflow/,
Cannot read from ".". Wr...
32\n-rw-r--r--@  1 mipl  staff     0 30 Apr 15:42 _index.json\ndrwxr-xr-
x@  3 mipl  staff    96  9 May 19:55 _system\ndrwxr-xr-x   4 mipl  staff
128 10 May 09:34 _users\ndrwxr-xr-x@ 18 mipl  staff   576 11 May 10:46 .
\n-rw-r--r--@  1 mipl  staff  6148 10 May 09:34 .DS_Store\ndrwxr-xr-x
staff     0 30 Apr 15:42 _index.json\ndrwxr-xr-x@  3 mipl  staff    96
9 May 19:55 _system\ndrwxr-xr-x   4 mipl  staff   128 10 May 09:34
_users\ndrwxr-xr-x@ 18 mipl  staff   576 11 May 10:46 .\n-rw-r--r--@  1
mipl  staff  6148 10 May 09:34 .DS_Store\ndrwxr-xr-x  18 mipl  staff
(depth<=2):' && find . -maxdepth 2 -mindepth 1 -type d 2>/dev/null |
default\n./.git\n./.git/filter-repo\n./.git/gk\n./.git/hooks\n./.git/
info\n./.git/logs\n./.git/objects\n./.git/refs\n./Chats\n./config\n./
config/whatsapp-sessions\n./knowledgebase\n./knowledgebase/notes\n./
learnings\n./learnings/_global\n./skills\n./skills/agent-browser\n./
skills/ai-social-media-conte...
Here’s what’s in the current workspace root:
Files
- _index.json
- .DS_Store
- .gitignore
- SKILL.md
- skills-lock.json
Folders
- _system, _users, Chats, config, Downloads (symlink), knowledgebase,
learnings, skills, subagents, Workflow
I can also list files/folders inside any one of those (e.g. Chats or Workflow)
if you want a full breakdown.
›`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Here’s what’s in the current workspace root:
Files
- _index.json
- .DS_Store
- .gitignore
- SKILL.md
- skills-lock.json
Folders
- _system, _users, Chats, config, Downloads (symlink), knowledgebase,
learnings, skills, subagents, Workflow
I can also list files/folders inside any one of those (e.g. Chats or Workflow)
if you want a full breakdown.`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponsePrefersSeparatorFramedFinalAnswer(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
Calling codex.list_mcp_resources({"cursor":null})
Called codex.list_mcp_resources({"cursor":null})
└ {"resources": []}
Intermediate assistant-looking replay near tool output
────────────────────────────────────────────────────────────────────────────────
Here is the final answer:
- alpha
- beta
────────────────────────────────────────────────────────────────────────────────
❯`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Here is the final answer:
- alpha
- beta`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseKeepsUnframedSavedImagePath(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
The generated image is saved at:

/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-generation/random-anything.png

(Equivalent relative path from workspace root: _users/default/Chats/image-generation/random-anything.png.)

› Find and fix a bug in @filename

gpt-5.3-codex-spark high · ~/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats
`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `The generated image is saved at:
/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-generation/random-anything.png
(Equivalent relative path from workspace root: _users/default/Chats/image-generation/random-anything.png.)`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseKeepsFramedReadImagePath(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
• Called
  └ api-bridge.execute_shell_command({"command":"curl -sS -X POST \"$MCP_API_URL/tools/custom/read_image\" ..."})
    {"stdout": "{\"filepath\":\"/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-model-test/vertex_1.jpg\",\"response\":\"...\"}"}

────────────────────────────────────────────────────────────────────────────────

• Yep — I found an image in the workspace and read it.

  I analyzed this image:

  /Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-model-test/vertex_1.jpg

  ### What it is

  A wide cyberpunk-style cityscape at dusk with a dark balcony foreground.

  ### Readable text detected

  - OBERNETICS
  - arasaka
  - NEO-VERIDIA

────────────────────────────────────────────────────────────────────────────────

› Improve documentation in @filename

gpt-5.3-codex-spark high · ~/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats
`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Yep — I found an image in the workspace and read it.
I analyzed this image:
/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats/image-model-test/vertex_1.jpg
### What it is
A wide cyberpunk-style cityscape at dusk with a dark balcony foreground.
### Readable text detected
- OBERNETICS
- arasaka
- NEO-VERIDIA`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseKeepsFramedWorkspacePath(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
	────────────────────────────────────────────────────────────────────────────────

• Here are the files/folders in:

  /Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats

  - Folders: analysis, chat-system-summary, daily-summary, generated-images,
    skills, workflows.
  - Files: .writetest, find_step.py, fix_markdown.py, report_plan.md.

────────────────────────────────────────────────────────────────────────────────
❯`

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := `Here are the files/folders in:
/Users/mipl/ai-work/mcp-agent-builder-go/workspace-docs/_users/default/Chats
- Folders: analysis, chat-system-summary, daily-summary, generated-images,
skills, workflows.
- Files: .writetest, find_step.py, fix_markdown.py, report_plan.md.`
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestParseCodexInteractiveResponseIgnoresRateLimitReminderModal(t *testing.T) {
	baseline := "Codex ready\n›"
	captured := baseline + `
› Take note of the exact token E2E_NOTE_deadbeef. Do not use tools. Reply with
  exactly ACK_E2E_NOTE_deadbeef and nothing else.

⚠ Heads up, you have less than 5% of your 5h limit left. Run /status for a breakdown.

• ACK_E2E_NOTE_deadbeef


  Approaching rate limits
  Switch to gpt-5.4-mini for lower credit usage?

› 1. Switch to gpt-5.4-mini                 Small, fast, and cost-efficient model for simpler coding tasks.
  2. Keep current model
  3. Keep current model (never show again)  Hide future rate limit reminders about switching models.

  Press enter to confirm or esc to go back
`

	if !hasCodexRateLimitReminderModal(captured) {
		t.Fatalf("rate limit reminder modal was not detected")
	}
	if hasCodexReadyPrompt(captured) {
		t.Fatalf("rate limit reminder selected option must not be treated as ready prompt")
	}
	if got := selectedCodexRateLimitReminderOption(captured); got != 1 {
		t.Fatalf("selected reminder option = %d, want 1", got)
	}

	got := parseCodexInteractiveResponse(captured, baseline, "", nil)
	want := "ACK_E2E_NOTE_deadbeef"
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	assertCodexNoInternalStatus(t, got)
}

func TestCodexIdleDetectionIgnoresAssistantProseAboutRunning(t *testing.T) {
	pane := `
	⏺ The prepare-test-fixtures step is now running in the background.
	  I will wait for the automatic notification before proceeding.

────────────────────────────────────────────────────────────────────────────────
❯
`
	if !hasCodexReadyPrompt(pane) {
		t.Fatalf("ready prompt not detected")
	}
	if hasCodexActivity(pane) {
		t.Fatalf("assistant prose containing running should not count as active TUI state")
	}
	if isCodexTUILine("The prepare-test-fixtures step is now running in the background.") {
		t.Fatalf("assistant prose containing running should not be filtered as TUI chrome")
	}
}

func TestCodexTUIFilterKeepsAssistantProseAboutTokens(t *testing.T) {
	if isCodexTUILine("Tokenizer behavior depends on how many tokens are in the prompt.") {
		t.Fatalf("assistant prose about tokens should not be filtered as Codex TUI chrome")
	}

	if !isCodexTUILine("· Working (9s · ↑ 363 tokens · thinking with high effort)") {
		t.Fatalf("Codex token/status line should still be filtered as TUI chrome")
	}
}

func TestLooksLikeCodexRateLimit(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{line: "error: 429 Too Many Requests", want: true},
		{line: "service unavailable from upstream", want: true},
		{line: "you hit your usage limit, try again later", want: true},
		{line: `WARN codex_core::shell_snapshot: Failed to delete shell snapshot at "/tmp/x": No such file or directory`, want: false},
		{line: "migration 21 was previously applied but is missing in the resolved migrations", want: false},
	}

	for _, tt := range tests {
		if got := looksLikeCodexRateLimit(tt.line); got != tt.want {
			t.Fatalf("looksLikeCodexRateLimit(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestCodexStringConfigOverrideEscapesDeveloperInstructions(t *testing.T) {
	got, err := codexStringConfigOverride("developer_instructions", "Line \"one\"\nPath C:\\tmp")
	if err != nil {
		t.Fatalf("codexStringConfigOverride returned error: %v", err)
	}

	want := `developer_instructions="Line \"one\"\nPath C:\\tmp"`
	if got != want {
		t.Fatalf("override = %q, want %q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("override contains a raw newline: %q", got)
	}
}

type codexDrainedStream struct {
	content        string
	terminalCount  int
	terminalSample string
}

func drainCodexStream(streamChan <-chan llmtypes.StreamChunk) codexDrainedStream {
	var parts []string
	var drained codexDrainedStream
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				drained.content = strings.TrimSpace(strings.Join(parts, ""))
				return drained
			}
			switch chunk.Type {
			case llmtypes.StreamChunkTypeContent:
				parts = append(parts, chunk.Content)
			case llmtypes.StreamChunkTypeTerminal:
				drained.terminalCount++
				if drained.terminalSample == "" {
					drained.terminalSample = chunk.Content
				}
			}
		default:
			drained.content = strings.TrimSpace(strings.Join(parts, ""))
			return drained
		}
	}
}

func assertCodexInteractiveTerminalOnlyStream(t *testing.T, streamChan <-chan llmtypes.StreamChunk) {
	t.Helper()
	drained := drainCodexStream(streamChan)
	if drained.content != "" {
		t.Fatalf("interactive stream emitted assistant-content chunk %q; want terminal snapshots only", drained.content)
	}
	if drained.terminalCount == 0 {
		t.Fatalf("interactive stream emitted no terminal snapshots")
	}
}

func assertCodexStreamQuality(t *testing.T, streamed, want string) {
	t.Helper()
	if !strings.Contains(streamed, want) {
		t.Fatalf("streamed content = %q, want assistant response containing %q", streamed, want)
	}
	assertCodexNoInternalStatus(t, streamed)
}

func assertCodexNoInternalStatus(t *testing.T, streamed string) {
	t.Helper()
	for _, noisy := range []string{
		"Generating",
		"esc to interrupt",
		"Ctrl+O",
		"ctrl+o",
		"pasted text",
		"Codex",
		"api-bridge",
		"read_mcp_resource",
		"list_mcp_resources",
		"list_mcp_resource_templates",
		"codex.list_mcp_resources",
		"workspace.list_mcp_resources",
		"Searching the web",
		"Searched https://",
		"Updated Plan",
		"Spawned ",
		"Waiting for ",
		"Finished waiting",
		"unknown MCP server",
		"execute_shell_command",
		"exit_code",
		"stdout",
		"stderr",
		"MCP_API_URL",
		"MCP_API_TOKEN",
		"Authorization: Bearer",
		"Authorization",
		"127.0.0.1",
		"/api/llm-config/models/metadata",
		"list_provider_models",
		"model_id",
		"input_cost_per_1m",
		"absolute host path",
		"writable folders",
		"Generating with model",
		"tmux focus-events",
	} {
		if strings.Contains(streamed, noisy) {
			t.Fatalf("streamed content = %q, should not contain TUI noise %q", streamed, noisy)
		}
	}
}
