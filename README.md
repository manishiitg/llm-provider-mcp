# Multi-LLM Provider

Use one coding agent from another. `llm-provider-mcp` connects local coding CLIs
through one MCP server, so a Codex or Claude Code session can delegate work to
Cursor, Pi, Codex, or Claude Code without leaving the current project.

Delegations run asynchronously in detached tmux sessions. The host receives a
job ID immediately, continues its own work, and checks the job later for progress
or a final result. Each target uses its existing local login, model access, and
filesystem permissions.

## Why use it

- Send a difficult task to a more capable model while the host keeps working.
- Send a small or repetitive task to a faster or lower-cost model.
- Use models available in Cursor, OpenRouter, Gemini, MiniMax, GLM, or Kimi from
  a Codex or Claude Code session.
- Run several independent delegations without blocking the host conversation.
- Inspect a live terminal tail or attach directly to tmux when a job needs human
  attention.

## Supported coding CLIs

| CLI | MCP host | Delegation target | Model selection |
|---|---:|---:|---|
| Codex CLI | Yes | Yes | Codex model IDs and reasoning levels |
| Claude Code | Yes | Yes | Claude Code model selectors |
| Cursor Agent | Manual MCP registration | Yes | Composer, Grok, and account-visible models |
| Pi CLI | Manual MCP registration | Yes | Gemini, OpenRouter, MiniMax, GLM, and Kimi |

The interactive setup currently registers Codex and Claude Code as hosts. Cursor
and Pi can use the same stdio MCP server through manual project configuration.
All four CLIs can run as local delegation targets.

## Quick start

Requirements:

- macOS or Linux
- tmux 3.x or newer
- at least one host CLI: `codex` or `claude`
- at least one authenticated target CLI: Cursor Agent, Pi, Codex, or Claude Code

From the project where you want to use delegation, run:

```bash
curl -fsSL https://raw.githubusercontent.com/manishiitg/multi-llm-provider-go/main/scripts/install-mcp.sh \
  | sh
```

The setup wizard detects installed CLIs and explains each choice. Select one or
more hosts and one or more targets with Up/Down, toggle with Space, and confirm
with Enter. Setup registers the MCP locally for the current project, verifies
target authentication, and installs a small delegation skill for each selected
host.

Start a new Codex or Claude Code session in the same project, then ask naturally:

```text
Delegate the authentication test failure to Cursor using composer-2.5.
Keep working here and check the delegated job when it is ready.
```

Or choose a different capability or cost profile:

```text
Ask Codex to review this migration with its strongest reasoning model.
Ask Pi to summarize these logs using Gemini 3.5 Flash.
Ask Pi to implement the small cleanup using the OpenRouter free-model router.
```

The host starts the background job, reports its `job_id`, and polls after the
recommended delay. It can request a bounded terminal snapshot while the job is
running or return the tmux attach command for direct human inspection.

## How delegation works

1. The host calls `delegate_coding_agent` with a provider, task, optional model,
   and its current trusted project directory.
2. The MCP server validates the provider and workspace, records the job in
   SQLite, and launches a detached worker.
3. The target CLI runs inside tmux with unattended project-scoped permissions.
4. The host calls `get_coding_agent_job` until the job completes, fails, is
   cancelled, or times out.
5. The host reviews the returned result and verifies any changes in the project.

Polling is the current completion mechanism. Push notifications through MCP
Tasks are planned after client behavior is consistent across the supported CLIs.

## Install and set up

Run the interactive installer:

```bash
curl -fsSL https://raw.githubusercontent.com/manishiitg/multi-llm-provider-go/main/scripts/install-mcp.sh \
  | sh
```

The bootstrap installs the binary at `~/.local/bin/llm-provider-mcp`, then starts
the Go setup wizard. The wizard detects local CLIs, asks which installed Codex
and Claude Code hosts should receive the MCP registration, and then asks which
Cursor, Pi, Codex, or Claude CLIs should be available as delegation targets.
Interactive terminals use checklists: move with Up/Down, toggle any number of
items with Space, and confirm with Enter. Redirected and accessible sessions use
the text fallback.

Setup also installs the bundled `delegate-coding-agent` skill in the current
project for every selected host:

- Codex: `.agents/skills/delegate-coding-agent/SKILL.md`
- Claude Code: `.claude/skills/delegate-coding-agent/SKILL.md`

The skill teaches the host how to choose a powerful, balanced, or fast model,
start an asynchronous delegation, poll it without blocking, inspect tmux output,
and verify the result. Re-running setup updates installer-managed copies but
refuses to overwrite an unrelated skill with the same name. `uninstall` removes
only copies marked as managed by this installer.

For a noninteractive Codex install, use:

```bash
curl -fsSL https://raw.githubusercontent.com/manishiitg/multi-llm-provider-go/main/scripts/install-mcp.sh \
  | sh -s -- --client codex --providers cursor-cli --non-interactive
```

For a noninteractive Claude Code install, use:

```bash
curl -fsSL https://raw.githubusercontent.com/manishiitg/multi-llm-provider-go/main/scripts/install-mcp.sh \
  | sh -s -- --client claude --providers cursor-cli --non-interactive
```

The installer downloads a checksum-verified macOS or Linux release archive when
one is available. Before the first MCP-enabled release, it falls back to building
the command from `main` and requires Go 1.25.8 or newer. The binary is installed at
`~/.local/bin/llm-provider-mcp` by default.

The included smoke test performs MCP initialization and verifies all five tools;
it does not invoke a provider or consume provider usage. Run
`sh scripts/install-mcp.sh --help` for binary-only installation, custom provider
allowlists, and installation directories.

Project skills are intentionally local-only. Setup displays the exact current
directory and skill paths and asks for confirmation before writing them. Claude
Code's MCP registration is also available only to the current user in that
project. Run setup from the intended project directory, and do not commit the
generated skill directories unless you intentionally want to share them.

The normal setup does not ask for a delegated working directory. The calling
host should pass its current trusted project root to each delegation. Use
`--workspace PATH` only when the MCP server should enforce a fixed root.

Setup verifies authentication for every selected target: Cursor through its
JSON status command, Codex through `codex login status`, Claude Code through its
JSON auth status, and Pi through its available model catalog. A target that is
not ready can open its native login flow directly from setup; credentials remain
owned by that CLI and are never collected by this installer. Setup can then run
optional small, read-only connectivity prompts with explicit consent. Running
`llm-provider-mcp doctor` without arguments checks tmux, MCP, installation, and
authentication for all four providers.

Supported non-deprecated coding-agent providers are discovered from
`CodingAgentProviderContracts()`. They currently include Claude Code, Codex CLI,
Cursor CLI, and Pi CLI.

Install or build the server:

```bash
go install github.com/manishiitg/multi-llm-provider-go/cmd/llm-provider-mcp@main

# From a checkout
make build-mcp
```

The server exposes five tools:

- `list_coding_agents`
- `list_coding_agent_models`
- `delegate_coding_agent`
- `get_coding_agent_job`
- `cancel_coding_agent_job`

`delegate_coding_agent` returns a `job_id`, `poll_after_seconds`, and
`next_tool`. Poll `get_coding_agent_job` until the status is `completed`,
`failed`, `cancelled`, or `timed_out`. A completed status contains the final
provider response. After the detached tmux worker starts, running status also
includes `tmux_session`, `tmux_capture_command`, and `tmux_attach_command`.
Set `include_terminal_output` to `true` on `get_coding_agent_job` to receive a
fresh, ANSI-cleaned, UTF-8-safe terminal tail. It captures 80 lines of
scrollback and byte-bounds the model-facing tail to 4 KB.
It uses the same shared tmux capture package as MCP Agent BuilderGo's `query_step`.
Keep it `false` for ordinary polling to avoid repeatedly adding unchanged pane
content to the host context. The capture command remains available for deeper
history. The attach command is intended for a human terminal and is omitted
after the session closes.

The installed binary also provides lifecycle commands:

```bash
llm-provider-mcp setup
llm-provider-mcp doctor
llm-provider-mcp models cursor-cli
llm-provider-mcp models cursor-cli --live
llm-provider-mcp uninstall
```

### Selecting a delegated model

Call `list_coding_agent_models` to discover curated selectors from the host, or
run `llm-provider-mcp models PROVIDER` locally. `delegate_coding_agent` accepts
an optional provider-specific `model`. For
Cursor, omitting it currently uses `composer-2.5`:

```json
{
  "provider": "cursor-cli",
  "model": "composer-2.5",
  "task": "Review the authentication flow",
  "working_dir": "/current/trusted/project"
}
```

The friendly selector `grok-4.5` maps to Cursor's `grok-4.5-xhigh`. Exact
Cursor IDs such as `grok-4.5-medium` and `composer-2.5-fast` pass through
unchanged. Run `cursor-agent models` to see the models available to the current
Cursor account; availability can change independently of this module.

The curated Pi CLI list keeps only the latest model in each supported family,
with separate Gemini Flash and Pro tracks:

- `google/gemini-3.5-flash`
- `google/gemini-3.1-pro-preview`
- `minimax/MiniMax-M2.7`
- `zai/glm-5.2`
- `moonshotai/kimi-k2.7-code`

Pi also accepts dynamic OpenRouter selectors such as
`openrouter/moonshotai/kimi-k2.7-code`. Use `openrouter/openrouter/free` for the
OpenRouter free-model router. These are intentionally not hardcoded because
OpenRouter's catalog changes independently.

Register the built binary as a local stdio MCP server. For Codex, add it to
`~/.codex/config.toml` or a trusted project's `.codex/config.toml`:

```toml
[mcp_servers.llm-provider]
command = "/absolute/path/to/llm-provider-mcp"
required = true
startup_timeout_sec = 10
tool_timeout_sec = 30
```

Claude Code and Cursor use the standard JSON stdio server shape in `.mcp.json`
and `.cursor/mcp.json`, respectively:

```json
{
  "mcpServers": {
    "llm-provider": {
      "command": "/absolute/path/to/llm-provider-mcp",
      "args": []
    }
  }
}
```

Pi can use the same server entry in `.pi/mcp.json` through its MCP adapter.
Each target CLI must already be installed and authenticated on the machine.

Job state is stored at `~/.local/state/llm-provider-mcp/jobs.db` by default.
Configuration environment variables:

- `LLM_PROVIDER_MCP_STATE`: override the SQLite state path.
- `LLM_PROVIDER_MCP_ALLOWED_PROVIDERS`: comma-separated provider allowlist.
- `LLM_PROVIDER_MCP_WORKSPACE_ROOTS`: OS path-list of allowed workspace roots.

Detached tmux jobs use an unattended policy so they never wait on an approval
dialog that the host cannot see. Standard coding tools are enabled by default:

- Codex uses `approval_policy=never` with the `workspace-write` sandbox.
- Cursor uses force approval with its sandbox enabled.
- Claude Code uses `dontAsk`, project-scoped Read/Edit/Write rules, and a Bash
  sandbox that fails closed when unavailable.
- Pi trusts project-local resources for the run and enables its native coding
  tools. Pi does not currently provide a hard workspace sandbox, so its Bash
  and file tools retain the permissions of the local user running the MCP.

All providers receive an explicit instruction not to access paths outside the
delegated working directory. `LLM_PROVIDER_MCP_WORKSPACE_ROOTS` additionally
restricts which working directories the MCP accepts, but it is not a process
sandbox. Do not delegate untrusted prompts or repositories to Pi until a
provider-independent sandbox is added.

Push completion uses polling today. Native MCP Tasks notifications will be
evaluated after Codex, Claude Code, Cursor, and Pi client behavior is certified.

## Go provider library

The MCP server is built on this repository's Go provider module. Applications
can also import that module directly for a common API across hosted LLMs and
local coding-agent CLIs:

```bash
go get github.com/manishiitg/multi-llm-provider-go@latest
```

The library supports text generation, tool calling, streaming, token usage,
structured output, image generation, and CLI-native agent execution. Provider
adapters include AWS Bedrock, OpenAI, Anthropic, OpenRouter, Google Vertex AI,
Azure AI, Z.AI, MiniMax, Codex CLI, Cursor Agent, Claude Code, Gemini CLI, and
Pi CLI.

The MCP implementation reuses these adapters instead of maintaining a separate
execution layer. In particular, the shared tmux capture package is also used by
MCP Agent BuilderGo's `query_step` behavior.

## Go library configuration

### Environment Variables

See `.env.example` for all available environment variables. Key variables:

- `OPENAI_API_KEY` - OpenAI API key
- `ANTHROPIC_API_KEY` - Anthropic API key
- `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION` - AWS credentials for Bedrock
- `GOOGLE_API_KEY` or `VERTEX_API_KEY` - Google API key for Vertex AI
- `OPEN_ROUTER_API_KEY` - OpenRouter API key
- `AZURE_AI_ENDPOINT`, `AZURE_AI_API_KEY` - Azure AI Services endpoint and API key
- `ZAI_API_KEY` - Z.AI API key
- `MINIMAX_API_KEY` - MiniMax API key (for both text and image generation)
- **Claude Code**: Uses tmux interactive CLI mode. Requires `claude` and `tmux` 3.x+ binaries in PATH. Authenticate Claude Code via interactive login before use.

### Provider Configuration

Each provider can be configured with:
- Model ID
- Temperature
- Max tokens
- Fallback models (for rate limiting)
- Custom options

## Go library testing

Build and run the test tool:

```bash
cd llm-providers
make build
./bin/llm-test --help
```

## Go library test coverage

The `llm-test` tool provides comprehensive test coverage for all LLM providers.

### Provider Test Coverage

All providers have **identical test coverage** using standardized tests, with specific capabilities noted:

#### Test Coverage Matrix

| Provider | Plain Text | Tool Calls | Structured Output | Image Input | Token Usage | Streaming | Agentic |
|----------|------------|------------|-------------------|-------------|-------------|-----------|---------|
| **Anthropic** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| **OpenAI** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| **Bedrock** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| **OpenRouter** | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ |
| **Vertex AI** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| **Azure AI** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| **Z.AI** | ✅ | ✅ | ✅ | ✅ (`glm-4.6v`) | ✅ | ❌ | ❌ |
| **MiniMax** | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ❌ |
| **Codex CLI** | ✅ | ✅ (native agent tools) | ❌ | ❌ | ✅ | ✅ | ✅ |
| **Cursor CLI** | ✅ | ✅ (native agent tools) | ❌ | ❌ | ❌ | ✅ (tmux snapshots) | ✅ |
| **Claude Code** | ✅ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ |

#### Image Generation

Image generation is a separate interface (`ImageGenerationModel`) initialized via `InitializeImageGenerationModel`.

For Z.AI, image input was verified live with `glm-4.6v` on the Coding Plan endpoint. `glm-5v-turbo` uses the same message format but may require a higher plan tier.

| Provider | Model | Cost | Aspect Ratios | Subject Reference (Editing) |
|----------|-------|------|---------------|-----------------------------|
| **Vertex AI** | gemini-3.1-flash-image | $0.067/1K image | 1:1, 16:9, 9:16, 4:3, 3:4, 2:3, 3:2, 21:9 | ✅ |
| **Vertex AI** | gemini-3-pro-image | $0.134/1K-2K image | 1:1, 16:9, 9:16, 4:3, 3:4, 2:3, 3:2, 21:9 | ✅ |
| **MiniMax** | image-01 | $0.0035/image | 1:1, 16:9, 9:16, 4:3, 3:4 | ✅ (URL) |
| **Codex CLI** | codex-cli / gpt-5.4 / gpt-5.3-codex | token-priced | Prompt-driven | local input image (implemented) |

**Codex CLI image generation**

Codex image generation is implemented through the native Codex CLI image workflow. The adapter asks Codex to save the generated asset to a temporary local path, then reads the resulting file bytes back into `ImageGenerationResponse`, so higher layers can keep using the standard `GenerateImages()` interface.

**MiniMax image generation example:**

```go
imageGen, err := llmproviders.InitializeImageGenerationModel(llmproviders.Config{
    Provider: llmproviders.ProviderMiniMax,
    ModelID:  "image-01",
    Logger:   logger,
})

// Basic generation
resp, err := imageGen.GenerateImages(ctx, "A mountain lake at sunset",
    llmproviders.WithAspectRatio("16:9"),
    llmproviders.WithNumberOfImages(2),
)

// Subject-reference editing (keep character, change scene)
resp, err := imageGen.GenerateImages(ctx, "Same person in a library",
    llmproviders.WithInputImageURL("https://example.com/reference.jpg"),
    llmproviders.WithAspectRatio("16:9"),
)
```

**CLI test commands:**
```bash
# Basic generation
./bin/llm-test minimax-image-generate --prompt "A futuristic city" --aspect-ratio 16:9 --num-images 2

# Subject-reference editing
./bin/llm-test minimax-image-generate \
  --prompt "Same person in a library" \
  --input-image-url "https://example.com/reference.jpg" \
  --aspect-ratio 16:9

# Codex native image generation
./bin/llm-test codex-cli-image-generate \
  --model codex-cli \
  --prompt "A complex infographic about the modern LLM stack" \
  --aspect-ratio 16:9
```

#### Claude Code CLI (`claude-code-*`)

The **Claude Code adapter** is unique because it integrates with a local **Agentic CLI**. Unlike standard API providers, it has:
- **Native Tools**: Access to local filesystem (`read_file`, `write_file`), shell (`bash`), and git.
- **Permission Handling**: Requires user approval for sensitive actions (e.g., `rm -rf`, `brew install`).
- **Stateful/Stateless Hybrid**: Supports stateless conversation playback via `stream-json` while leveraging the CLI's internal agent capabilities.

| Test Type | Command | Features |
|-----------|---------|----------|
| Streaming Content | `claude-code-streaming-content` | Basic real-time token streaming |
| Streaming Multi-Turn | `claude-code-streaming-multiturn` | Multi-turn conversation history playback with context retention |
| Permission Denial | `claude-code-permission` | Detecting and parsing permission denial events from CLI |

**Example:**
```bash
./bin/llm-test claude-code-streaming-content
./bin/llm-test claude-code-permission
```

### Claude Code vs. Standard LLMs

| Feature | Standard LLM (OpenAI/Anthropic) | Claude Code CLI Adapter |
| :--- | :--- | :--- |
| **Execution** | Remote API Call | Local Subprocess (`exec.Command`) |
| **Tools** | You must define & execute tools | **Built-in Agent Tools** (Bash, File Ops, Glob, Grep) |
| **FileSystem** | No access (unless you build tools) | **Direct Access** to local project files |
| **Permissions** | N/A (Stateless) | **Permission Denials** reported for sensitive actions |
| **Latency** | Low (Direct API) | Higher (Agent thinking + CLI overhead) |
| **Cost** | Token-based | Token-based (tracked via CLI output) |
| **Best For** | Fast chat, defined tasks, RAG | **Autonomous coding**, local refactoring, shell automation |

### Other Provider Tests

(See full list in original README for standard providers like Anthropic, OpenAI, Bedrock, etc.)

## MiniMax Provider

### Text Models

| Model | Input | Output | Cache Read | Cache Write | Context |
|-------|-------|--------|------------|-------------|---------|
| MiniMax-M2.7 | $0.30/M | $1.20/M | $0.03/M | $0.375/M | 1M tokens |
| MiniMax-M2.5 | $0.30/M | $1.20/M | $0.03/M | $0.375/M | 1M tokens |
| MiniMax-M2.5-highspeed | $0.60/M | $2.40/M | $0.03/M | $0.375/M | 1M tokens |
| MiniMax-M2.1 | $0.30/M | $1.20/M | $0.03/M | $0.375/M | 1M tokens |
| MiniMax-M2.1-highspeed | $0.60/M | $2.40/M | $0.03/M | $0.375/M | 1M tokens |
| MiniMax-M2 | $0.30/M | $1.20/M | $0.03/M | $0.375/M | 1M tokens |

Uses the OpenAI-compatible endpoint (`/v1/text/chatcompletion_v2`) with full support for tool calling, streaming, JSON mode, and prompt caching.

### Image Model

| Model | Price | Notes |
|-------|-------|-------|
| image-01 | $0.0035/image | Supports subject-reference editing via URL |

## Z.AI Provider

### Text Models

Z.AI integration uses the chat completions API and defaults to the Coding Plan endpoint for this repo:

- Default base URL: `https://api.z.ai/api/coding/paas/v4`
- Override when needed with `ZAI_BASE_URL`
- Default test model: `glm-4.7`

### Vision Model

- `glm-4.6v` works on the Coding Plan access tested in this repo
- `glm-5v-turbo` is the newer multimodal coding model, but may require a higher Z.AI plan

### Pending

- Web search is intentionally not enabled in the current Z.AI integration
- Keep this as a TODO for a future release once the Z.AI search surface is stable and returns production-usable results for this package

### Test Commands

```bash
./bin/llm-test zai --model glm-4.7
./bin/llm-test zai-tool-call --model glm-4.7
./bin/llm-test zai-streaming-content --model glm-4.7
./bin/llm-test zai-streaming-mixed --model glm-4.7
./bin/llm-test zai-streaming-multiturn --model glm-4.7
./bin/llm-test zai-structured-output --model glm-4.7
./bin/llm-test zai-token-usage --model glm-4.7
./bin/llm-test zai-image --model glm-4.6v
./bin/llm-test zai-image --model glm-4.6v --image-path /path/to/local-image.webp
```

## Code Quality

This project uses [golangci-lint](https://golangci-lint.run/) for production-critical code quality checks.

## Security & Secret Scanning

This project uses [gitleaks](https://github.com/gitleaks/gitleaks) to prevent accidental secret commits.

## License

See LICENSE file for details.
