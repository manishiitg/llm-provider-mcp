# Roadmap

The roadmap records direction, not guaranteed delivery dates. Provider CLI and
MCP client behavior can change independently of this project.

## Available Now

- Project-local setup for Codex and Claude Code hosts
- Cursor, Pi, Codex, and Claude Code delegation targets
- Durable asynchronous jobs with SQLite state
- Detached tmux execution and human attach commands
- Bounded terminal progress snapshots
- Cancellation and timeouts
- Curated and provider-visible model discovery
- Managed project skills
- Authentication checks and optional connectivity prompts
- Downstream compatibility checks for MCP Agent and MCP Agent Builder

## Next

- Launch-focused documentation and real terminal demo
- Official MCP Registry publication
- Provider-independent workspace sandbox evaluation
- Additional installer validation across supported macOS and Linux platforms
- More direct package-level tests for compatibility-only API providers

## Evaluating

- MCP task notifications or another push completion mechanism
- First-class Cursor and Pi host registration
- Windows support through an alternative terminal/session backend
- Structured progress events beyond terminal snapshots
- Provider health and capability diagnostics

## Compatibility Cleanup

- Coordinate removal of the deprecated Antigravity CLI integration across all
  three dependent repositories.
- Deprecate unused exported event compatibility declarations before removal.
- Continue splitting the historical provider implementation into smaller files
  without changing the public Go package.

Feature requests should describe the user workflow, supported host and target,
security implications, and how the behavior can be tested deterministically.
