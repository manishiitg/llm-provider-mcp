# Contributing

Contributions should preserve the local, asynchronous coding-agent delegation
workflow and the public Go APIs consumed by downstream repositories.

## Before Opening An Issue

- Run `llm-provider-mcp doctor` for installation or authentication problems.
- Search existing issues for the provider, host, model, and error text.
- Remove credentials, private source, and sensitive terminal output.
- Use the security process for vulnerabilities instead of a public issue.

## Development Setup

Requirements:

- Go 1.25.8 or newer
- tmux 3.x or newer
- `golangci-lint`
- Native coding CLIs required by the tests you intend to run

```bash
git clone https://github.com/manishiitg/llm-provider-mcp.git
cd llm-provider-mcp
go mod download
make build-mcp
go test ./...
```

## Required Checks

Before opening a pull request:

```bash
gofmt -w <changed-go-files>
go test ./...
golangci-lint run --timeout=5m ./...
git diff --check
```

Changes to exported provider APIs must also compile against MCP Agent and MCP
Agent Builder. CI performs those downstream checks automatically.

## Coding-Agent Changes

`CodingAgentProviderContracts()` is the capability source of truth. A provider
change may also require updates to:

- Model discovery
- Setup detection and authentication
- Job runner dispatch
- MCP tool schemas or responses
- tmux/session lifecycle registries
- Project skill guidance
- Static contract tests and opt-in real E2E tests

Do not advertise a capability before the adapter, setup, tests, and cleanup path
all support it.

## Tests

Keep ordinary tests deterministic and offline. Real CLI or provider tests must
remain opt-in behind an explicit environment variable and explain required
credentials in the skip message.

Use the smallest real model suitable for contract verification. Real E2E tests
must clean up owned tmux sessions and temporary project artifacts.

## Pull Requests

- Keep changes scoped to one behavior or cleanup.
- Explain user-visible behavior and compatibility impact.
- Include tests proportional to the change risk.
- Document new configuration and limitations.
- Do not include generated credentials, local MCP registration, or private
  terminal transcripts.

Maintainers may ask for a downstream compatibility migration before accepting a
breaking public-API change.
