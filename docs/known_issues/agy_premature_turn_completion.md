# Known issue: agy interactive adapter declares turns "done" prematurely

**Component:** `pkg/adapters/agycli/agycli_interactive_adapter.go`
**Observed with:** Antigravity CLI `1.0.12`, Gemini 3.5 Flash
**Severity:** real production bug (turns cut short during tool-loading / early generation), not test-only
**Status:** diagnosed; fix deferred (needs careful design — see "Why the obvious fix is wrong")

## Symptom

`GenerateContent` returns in ~8s with the wrapped tail of the user's own prompt as the
"answer", **before agy has even called a tool**. Reproduced by
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

Turn-completion in `hasAgyReadyPrompt` (~line 2580) accepts the turn as **done** when:
1. a `>` input box is visible (`hasAgyReadyMarker`) — agy shows one **even while busy**, and
2. no recognized live-generation signal is present (`hasAgyLiveGenerationActivity`, ~2613).

`hasAgyLiveGenerationActivity` only recognizes `"ctrl+c to stop"`, `"esc to interrupt"`,
`"composing"`, `○ ` tool cards, `"esc to cancel"`. It does **not** recognize agy 1.0.12's
spinner busy-states (`⣾ Generating…`, `⣷ Discovering Tool Functionality…`). So while agy is
actively generating/loading tools, the detector reports "not busy", the always-present `>`
box satisfies "ready", and the turn is declared complete — returning the prompt-echo
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

## Correct fix (proposed, not yet implemented)

Distinguish "stable because **done**" from "stable because **momentarily paused mid-generation**".
Require evidence of an actual turn before accepting stability as completion, e.g.:

- Do **not** accept `hasAgyReadyPrompt` as terminal while the extracted assistant text is empty
  / equal to the echoed user prompt **and** a `Generating…`/spinner status is present — i.e.
  require a **non-empty real answer** (or a confirmed post-submit pane change) before completing.
- Keep ignoring the cycling spinner for the stability key (don't regress
  `TestAgySpinnerStableKeyIgnoresCyclingSpinner`); the gate is "no real answer yet", not "spinner present".

This keeps both existing unit tests green AND fixes the live premature-completion. Validate with:
- `go test ./pkg/adapters/agycli -count=1` (unit suite must stay green), and
- `RUN_AGY_CLI_REAL_E2E=1 go test ./pkg/adapters/agycli -run TestAgyCLIRealInteractiveLiveInputProcessesQueuedFollowupContract` (must engage the tool and process the queued follow-up — it passed at 31s under the temporary patch, confirming agy itself queues+processes correctly once completion detection is fixed).

## What is NOT in question

Agy's CLI **does** natively queue + process a mid-turn message — confirmed: under the temporary
patch the E2E passed (31.46s) with the queued follow-up's marker in the final content. This bug
is purely the **adapter's completion detection**, not agy's input handling.
