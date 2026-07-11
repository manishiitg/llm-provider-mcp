# Providers And Models

The MCP product exposes four non-deprecated local coding-agent targets. Each
target uses its own installed CLI and existing authentication.

| Provider ID | CLI | Authentication | Typical use |
|---|---|---|---|
| `cursor-cli` | Cursor Agent | Cursor account login | Composer and account-visible models |
| `pi-cli` | Pi | Provider keys managed by Pi | Gemini, OpenRouter, MiniMax, GLM, Kimi |
| `codex-cli` | Codex CLI | `codex login` | OpenAI coding models and reasoning levels |
| `claude-code` | Claude Code | `claude` login | Claude Code models and native tools |

Run the local catalog command before choosing a model:

```bash
llm-provider-mcp models cursor-cli
llm-provider-mcp models pi-cli
llm-provider-mcp models codex-cli
llm-provider-mcp models claude-code
```

Add `--json` for machine-readable output. Cursor also supports `--live` to query
the current account-visible model catalog.

## Cursor Agent

Omitting the model currently selects `composer-2.5`. The friendly selector
`grok-4.5` maps to Cursor's `grok-4.5-xhigh`; exact Cursor IDs pass through
unchanged.

Cursor model availability belongs to the current Cursor account and can change
independently of this project. Run `cursor-agent models` or the live catalog
command when an exact selector is rejected.

## Pi

The curated catalog keeps one current model per supported family, with separate
Gemini Flash and Pro tracks:

- `google/gemini-3.5-flash`
- `google/gemini-3.1-pro-preview`
- `minimax/MiniMax-M2.7`
- `zai/glm-5.2`
- `moonshotai/kimi-k2.7-code`

Pi also accepts dynamic OpenRouter selectors such as
`openrouter/moonshotai/kimi-k2.7-code`. Use `openrouter/openrouter/free` to let
OpenRouter select an available free model. Dynamic OpenRouter models are not
hardcoded because that catalog changes independently.

Pi credentials remain in Pi's own configuration. The setup wizard never asks
the user to paste provider API keys into `llm-provider-mcp`.

## Codex CLI

Codex accepts model IDs and reasoning levels exposed by the installed CLI. Use
the catalog instead of assuming that an account has access to every advertised
model.

GPT-5.6 Sol, Terra, and Luna require a Codex 0.145 build. In the July 11, 2026
real demo, stable Codex CLI 0.144.1 rejected `gpt-5.6-sol` with an upgrade
message, while `0.145.0-alpha.4` completed the job. Until 0.145 reaches the
stable channel, install the alpha explicitly only when you intend to use these
selectors:

```bash
npm install -g @openai/codex@alpha
```

Codex delegated jobs run with `approval_policy=never` and the `workspace-write`
sandbox so a detached job cannot wait on an invisible approval prompt.

## Claude Code

Claude Code accepts its native model selectors and uses project-scoped tool
permissions for detached jobs. Setup checks the existing Claude authentication
status and can open the native login flow when needed.

## Deprecated Compatibility Providers

Gemini CLI and Antigravity CLI remain in the Go module for existing downstream
sessions. New MCP setup does not offer them. Use Pi for new Gemini-backed model
routing.
