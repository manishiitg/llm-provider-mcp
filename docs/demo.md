# Real Delegation Demo

The README animation is based on a real end-to-end run performed on July 11,
2026. It is rendered from the sanitized tool events listed in
[`demo-transcript.json`](assets/demo-transcript.json), not invented provider
output.

## Environment

- Host: Claude Code 2.1.207, Claude Sonnet 5
- Target: Cursor Agent 2026.07.09, Composer 2.5
- MCP server: locally built `llm-provider-mcp` 0.6.0 demo binary
- Workspace: disposable Go repository
- Initial failure: `Add(7, 5) = 2, want 12`

## Task

Claude Code was instructed not to edit the repository itself. It had to:

1. Confirm the failing test.
2. Delegate the bounded fix to `cursor-cli` through MCP.
3. Record the asynchronous job ID.
4. Poll to a terminal state.
5. Inspect the resulting Git diff.
6. Rerun the test without cache.

## Result

- Job ID: `job_a945abbdb5d45df757fe7cf9639565ca`
- Target status: completed
- Target elapsed time: 28 seconds
- Changed file: `calculator.go`
- Change: `return a - b` to `return a + b`
- Target verification: `go test ./...` passed
- Host verification: `go test -count=1 ./...` passed

The run also exercised project-local setup, managed skill loading, deferred MCP
tool discovery, durable job creation, tmux progress metadata, bounded terminal
capture, final extraction, usage reporting, session cleanup, and independent
host verification.

## Reproduce

Create a disposable Go repository containing:

```go
// calculator.go
package calculator

func Add(a, b int) int { return a - b }
```

```go
// calculator_test.go
package calculator

import "testing"

func TestAdd(t *testing.T) {
    if got := Add(7, 5); got != 12 {
        t.Fatalf("Add(7, 5) = %d, want 12", got)
    }
}
```

Run `llm-provider-mcp setup`, select Claude Code as the host and Cursor as the
target, restart Claude in that project, and use:

```text
Use the llm-provider MCP tools to delegate this task to cursor-cli: Fix the
failing TestAdd, run go test ./..., and report what changed. Do not edit files
yourself. Poll the asynchronous job to completion, inspect the resulting diff,
and rerun go test -count=1 ./... yourself.
```

Provider versions, model output, and elapsed time will vary.
