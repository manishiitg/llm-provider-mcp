# Resolved issue: agy interactive adapter declared turns "done" prematurely

**Component:** `pkg/adapters/agycli/agycli_interactive_adapter.go`
**Observed with:** Antigravity CLI `1.0.12`, Gemini 3.5 Flash
**Severity:** real production bug (turns cut short during tool-loading / early generation), not test-only
**Status:** resolved in `f5687b3` (`Fix Agy prompt echo completion detection`)

## Symptom

`GenerateContent` returned in ~8s with the wrapped tail of the user's own prompt as the
"answer", **before agy had even called a tool**. Reproduced by
`TestAgyCLIRealInteractiveLiveInputProcessesQueuedFollowupContract` (real-E2E):

```
agycli_real_contract_test.go:1174: GenerateContent returned before slow MCP tool call
start: err=<nil> content="until the tool returns. Then reply exactly AGY_FIRST_DONE_…"
```

Live pane capture at the moment of the false completion:
```
> Call the api-bridge slow_contract MCP tool ... delay_ms 8000. Do not answer
  until the tool returns. Then reply exactly AGY_FIRST_DONE_…    ← wrapped prompt (no '>' marker)
⣾  Generating...                                                 ← agy is actively working (animated spinner)
─────────
>                                                                ← but a '>' input box is ALSO visible
```

## Root cause

The original turn-completion path in `hasAgyReadyPrompt` accepted the turn as **done** when:
1. a `>` input box is visible (`hasAgyReadyMarker`) — agy shows one **even while busy**, and
2. no recognized live-generation signal is present (`hasAgyLiveGenerationActivity`, ~2613).

`hasAgyLiveGenerationActivity` only recognizes `"ctrl+c to stop"`, `"esc to interrupt"`,
`"composing"`, `○ ` tool cards, `"esc to cancel"`. It does **not** recognize agy 1.0.12's
spinner busy-states (`⣾ Generating…`, `⣷ Discovering Tool Functionality…`). So while agy is
actively generating/loading tools, the detector reported "not busy", the always-present `>`
box satisfied "ready", and the turn was declared complete — returning the prompt-echo
(itself a secondary wrapped-prompt extraction leak, same class as the codex bug fixed on
branch `codex-fix-and-queue-tests`).

## Why the obvious fix is WRONG

The tempting fix — "treat a braille spinner / `Generating…` line as busy" in
`hasAgyLiveGenerationActivity` — makes the E2E pass but **regresses agy's deliberate design**
and would **hang** turns. Agy *intentionally* leaves a spinner-prefixed `Generating…` line
in the pane **after** completion and detects "done" via pane **stability** (ignoring the
cycling spinner). Two unit tests enshrine this and FAIL under the naive fix:
- `TestAgyGeneratingStatusWithReadyPromptIsReady` — "spinner + Generating + ready prompt = done"
- `TestAgySpinnerStableKeyIgnoresCyclingSpinner` — "the spinner must be ignored" (see `agySpinnerStableKey`)

The crux: **a single pane snapshot cannot distinguish active-generating from stale-generating** —
the content is identical (`⣾ Generating…` + `>` box). The only difference is temporal
(active = pane keeps changing across snapshots; stale = byte-stable). So the correct signal is
the **stability mechanism over time**, not a per-line classification. The naive patch was
verified to break both tests; it was reverted.

## Correct fix (implemented)

The shipped fix distinguishes "stable because **done**" from "stable because
**momentarily paused mid-generation**" by requiring evidence of real assistant output
before accepting readiness as terminal:

- Do **not** accept `hasAgyReadyPrompt` as terminal while the extracted assistant text is empty
  / equal to the echoed user prompt **and** a `Generating...`/spinner status is present — i.e.
  require a **non-empty real answer** before completing.
- Keep ignoring the cycling spinner for the stability key (don't regress
  `TestAgySpinnerStableKeyIgnoresCyclingSpinner`); the gate is "no real answer yet", not "spinner present".
- Drop wrapped prompt tails and Agy TUI chrome from final extraction so prompt echoes are not
  surfaced as assistant answers.

Validation run after the fix:
- `go test ./pkg/adapters/agycli -count=1`
- `go test . -count=1`
- `RUN_AGY_CLI_REAL_E2E=1 go test ./pkg/adapters/agycli -run TestAgyCLIRealInteractiveLiveInputProcessesQueuedFollowupContract -count=1 -v -timeout 4m`

## What is NOT in question

Agy's CLI **does** natively queue + process a mid-turn message. The passing real E2E
confirms the bug was purely the **adapter's completion detection**, not Agy's input handling.
