# Muse Coding Agent Contract Specification

Recon date: 2026-09-10. CLI: `muse` (Muse Code) `1.1.1`, `~/.local/bin/muse`.
Status: recon only — no adapter exists yet. Every claim below is marked
**verified** (observed live, echo provider, zero API spend) or **TBD**
(needs a Meta-provider run or code you have not written yet).

> Name warning: `copilot` on PATH is AWS ECS Copilot, not this agent.
> Provider id: `muse-cli`. CLIName: `muse`.

---

## Dual-Transport Model

Like Codex, Muse ships both transports, so the adapter should plan for both
with tmux as the default product path:

1. **Stateful tmux transport (default path, TBD).** Bare `muse [PROMPT]`
   runs the interactive TUI; no subcommand means interactive mode. Tmux
   smoke test not yet run — first P0 proof to write (`fresh_launch`).
2. **Structured transport (`muse exec --json`, verified).**
   `muse exec --provider echo --json "say the word pineapple"` exits 0 and
   streams MSP wire-schema JSONL: `run.output.delta` (`payload.text`) while
   running, terminal `run.terminal.completed` (`payload.text`,
   `payload.terminal`) at the end. `muse schema` exports the wire schema
   (JSON Schema / TypeScript) — pin the parser against it.

## Session Registry & Lifecycle

* Sidecar transcript (**verified**): `$XDG_DATA_HOME/muse/sessions/YYYY/MM/DD/<session-uuid>/session.jsonl`,
  plus `subagent/<id>/session.jsonl` children. Respects `XDG_DATA_HOME`
  (confirmed by redirecting it to `/tmp`). This is the final-answer and
  token-usage source for tmux mode — never pane-scrape for the final reply.
* `muse export --session <id|path> [--last] [--out <file>] [--redacted]`
  dumps one session as a self-contained JSON document (messages, tool
  calls/results, approvals, model ids, fork/subagent lineage).
* Suggested tmux registry prefix (following the other providers):
  `mlp-muse-cli-int`.

## Resume (verified at CLI level, context carryover TBD)

* Interactive: `muse resume --last | <session-uuid>` (bare `resume` opens a picker).
* Headless: `muse exec --session-id <uuid> "<next prompt>"` reuses the
  session — verified: second run exited 0 and appended to the same
  `session.jsonl` (116 lines after two echo turns).
* Context carryover is **TBD**: the echo provider cannot prove the model
  sees prior turns. Needs one Meta-provider two-turn run.
* Contract: `SupportsNativeResume: true` once proven; surface the session id
  (`stream.id`) in `GenerationInfo.Additional` and consume it via
  `NativeResumeOption`, like the other four providers.

## Auth

* `muse login` (Meta account, browser code approval). `META_API_KEY` env
  **always takes priority** over the account login.
* `muse auth set [--provider <PROVIDER>] --api-key-stdin` (key via stdin,
  never argv). `muse exec --api-key-stdin` for one-shot runs.
* Same shape as Claude Code: saved login or explicit key, no third-party
  key forwarding. `APIKeyEnvVars: ["META_API_KEY"]` (TBD: exact provider
  token names for `--provider meta`).

## MCP Bridge & System Prompt

* MCP servers come from user `settings.json` (**verified** 2026-09-10):
  `$XDG_CONFIG_HOME/muse/settings.json` with `{"schema_version": 1,
  "mcpServers": {"<name>": {"url": "<streamable-http>"}}}` — accepted
  (login proceeds to OAuth; sandbox loopback bind is the only failure).
  No workspace-level equivalent found: `<ws>/mcp.json`,
  `<ws>/.mcp.json`, `<ws>/.agents/mcp.json`, `<ws>/.muse/settings.json`
  are all ignored (trusted or not). Adapter implication: the bridge
  writes user-level `settings.json` (merge, don't clobber).
* Mount mechanism implemented + verified live 2026-09-10
  (`musecli.WithMCPConfig`, merge/restore in `musecli_mcpsettings.go`):
  the CLI reads the merged `api-bridge` entry and attempts startup init.
  An unreachable server fails the WHOLE run: `run.terminal.failed` with
  reason "invalid run configuration: Required MCP server `api-bridge`
  failed during startup: initialization failed." — the exec lane now
  surfaces that reason in the returned error (previously only exit
  status). Consequence: the mount is proven, but the success path needs
  a real reachable bridge URL (mcpagent sidecar); that is the live
  `mcp_bridge` P0. Restore runs on success and failure alike.
* Bridge-only containment (deny native tools) is **TBD** — no
  `--disallowedTools` equivalent in help. Candidates: `--permission-profile
  <ID>` (mechanism unknown, likely settings-defined), or partial denial via
  `--disable-shell` / `--disable-write` / `--disable-web-tools`. Needs a
  live bridge+model run to settle; release-blocking for `bridge_only_tools`.
* No `--system-prompt` / `--append-system-prompt` / `--instruction` flags
  exist (grep over `muse --help` + `muse exec --help` is empty).
* System prompt goes through project rules: `muse init [--dry-run] [--force]`
  scaffolds `AGENTS.md` ("Muse Code reads this file as project rules when it
  runs in this directory") — same convention as Codex/Pi. Adapter projection
  target: `<workdir>/AGENTS.md`. `WorkingDirInstructionFile: "AGENTS.md"`.
  User-level rules path **TBD** (personal rules exist — see
  `--no-foreign-personal-context` — but the file location is unconfirmed).
* Skills are first-class: `muse skills list|inspect|enable|disable|install|import|update|uninstall`
  with `--source all|user|project|built-in|plugin` and
  `muse skills import --from claude|codex`. Project skill dirs (**verified**
  2026-09-10, one run, distinct names per dir): `<workdir>/.agents/skills/<name>/SKILL.md`
  and `<workdir>/.claude/skills/<name>/SKILL.md` (Claude compat) are both
  discovered with activation `on` by default — no `enable` step needed.
  Ruled out: `<workdir>/.muse/skills/` and `<workdir>/skills/` (never listed).
  Hard requirement: trusted workspace (`--trust-workspace` or saved trust);
  untrusted workspaces skip project skills entirely with diagnostic
  `project-skills-untrusted`. Adapter projection target:
  `<workdir>/.agents/skills/<name>/SKILL.md`.
* Bridge-only containment flag (deny native tools, the `--disallowedTools`
  equivalent) is **TBD** — release-blocking question for `bridge_only_tools`.
* `UsesNativeSystemPrompt` **TBD**.

## Trust & Approval Gates

* Workspace is **untrusted by default** (observed: "workspace is untrusted",
  "Agent delegation: auto unavailable"). Contract: `RequiresWorkspaceTrust: true`.
* Per-run gates: `--trust-workspace`, `--approval-mode untrusted|on-request|never`,
  `--disable-approval`, `--disable-sandbox`, `--sandbox-network <MODE>`,
  `--yolo`, `--permission-profile <ID>`.

## Adapter Constraints (verified — will bite if ignored)

1. **Never pass `--no-session-log`.** `exec` hard-fails:
   `local session messaging disabled: session logging is required`.
   Same for `--session-id` without a writable session store.
2. **Resolve the transcript via `XDG_DATA_HOME`** (`$XDG_DATA_HOME/muse/sessions/...`),
   not a hardcoded `~/.local/share/muse` — honors the env override.

## Exec Lane (implemented 2026-09-10, echo-verified)

`pkg/adapters/musecli/musecli_exec.go`: `GenerateContent` shells
`muse exec --json --provider <p> --trust-workspace [--api-key-stdin]
[--model <m>] <prompt>`, parses MSP wire JSONL (`run.output.delta` →
content chunks + accumulation, `run.terminal.completed` → authoritative
final text), forwards deltas to `opts.StreamChan` (caller-owned, never
closed here), attaches `{provider: muse-cli, transport: structured,
native_session_id: stream.id}`. System messages fold into a header (no
exec system-prompt flag exists); `--model` skipped for placeholder and
echo runs; key via stdin avoids argv leak. `MUSE_CLI_EXEC_PROVIDER`
overrides the provider (tests use `echo`). Always-green proof:
`TestMuseExecLaneEchoFinalText/StreamsDeltas` (XDG-isolated, no auth).
Sidecar readers (`musecli_transcript.go`, implemented 2026-09-10):
log path globbed as `$XDG_DATA_HOME/muse/sessions/*/*/*/<id>/session.jsonl`;
usage sums `goal_usage_attribution` quantities per run (echo reports honest
zeros); messages map intake prompts + `assistant_message_committed` texts;
exec lane attaches `resp.Usage` best-effort (proven by the echo test
asserting non-nil). Contract now `SurfacesTokenUsage: true`,
`TokenUsageSource: "transcript-file"`, `AdapterReadsTranscript: true`
(coherence test green); usage magnitudes on a paid model still TBD.

## Structured Resume (implemented 2026-09-10, echo-verified)

`WithMuseResumeSessionID` (adapter-local option + provider wrapper +
`nativeResumeRegistry` entry; contract `SupportsNativeResume: true`,
drift test green). Exec lane passes `--session-id`; each turn attaches
`{structured, stream.id}` so the next turn resumes it. Proof:
`TestMuseExecLaneStructuredMultiTurn` — two echo turns, same session id
both turns, both prompts in one on-disk log (cert registered, red list
unchanged at 12 since `multi_turn`/tmux is still open). Echo cannot prove
the model saw turn one — one Meta turn pair still owed. Full restore
wiring (`mcpagent.Agent` field + `server.go` switch) deferred with the
tmux lane.

## Token & Cost Tracking (TBD)

`readMuseTranscriptUsage` / `readMuseTranscriptMessages` do not exist yet.
Next probe: grep a real (Meta-provider) `session.jsonl` for usage/model
fields, following `docs/COSTS_AND_CONVERSATION_HISTORY.md` ("Adding a new
tmux provider"). Until then `TokenUsageSource` is undecided.

## Testing

* Contract entry goes in [coding_agent_contract.go](../coding_agent_contract.go)
  (`Transport: CodingAgentTransportTmux`); expect
  `TestActiveCodingAgentProvidersSatisfyP0Contract` to go red — that
  failure list is the implementation todo.
* Live proofs gate on `-coding-cli-p0-live`, mirroring the other four
  providers; credential-free P0 is the tmux lifecycle matrix.
* P0 order: tmux smoke (`fresh_launch`) → usage/message readers with
  synthetic `session.jsonl` fixtures → bridge-only proof → structured
  lane (`exec --json` argv pinned by unit test) → multi-turn resume on a
  real model.

### Recommended answers in the native question widget

The tmux adapter automatically selects the single option explicitly labelled
`(Recommended)` in Muse's `Request user input` widget. It follows the Cursor
readiness pattern: consecutive captures, validate the active dialog immediately
before sending keys, and wait for the dialog to change before another submission.
The current cursor is observed rather than assumed to be on the first choice.
Multi-question forms advance through each question and submit the final review
only when every reviewed answer is explicitly marked recommended.

No recommendation, multiple recommendations, unrecognized dialogs, and auth/trust
prompts remain manual. `WithAutoSelectRecommended(false)` restores the manual
`user_input_required` behavior for callers that need it. Automatic selections
do not emit UI announcements. Context cancellation stops further key delivery.

Live regression: `TestMuseCLIRealAutoRecommendedThreeQuestionsP0`; parser and
cancellation/duplicate checks: `TestMuseRecommendedQuestion`,
`TestMuseRecommendedReview`, and `TestMuseAutoAnswerDisabledCanceledAndDuplicate`.

Live input and retained-turn polling service the same native controls, including
questions that appear after `GenerateContent` has returned. Before each typing
attempt, live delivery resolves eligible questions and review screens. Selector
state is shared per persistent session to serialize key delivery; the selector
does not retain or write to generation stream channels.
Killing the session disables further automatic answers.

Live regression: `TestMuseCLIRealLiveInputQuestionsP0` covers a three-page form
created after live delivery, plus a preexisting dialog with its cursor on the
wrong option. It verifies both the recommended answer and the subsequent user
message reach Muse. These are real adapter/tmux tests, not HTTP or desktop UI tests.
