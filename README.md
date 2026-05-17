# llm-providers

A Go module providing a unified interface for multiple Large Language Model (LLM) providers, including AWS Bedrock, OpenAI, Anthropic, OpenRouter, Google Vertex AI, Azure AI, **Z.AI**, **Claude Code CLI**, and **MiniMax**.

## Overview

This module abstracts the differences between various LLM providers, providing a consistent API for:
- Text generation
- Tool calling (API-based and CLI-native)
- Streaming responses (chunk-based and multi-turn)
- Token usage tracking (input/output/cache/cost)
- Structured output
- **Agentic Capabilities** (via Claude Code CLI)

## Installation

```bash
go get github.com/manishiitg/multi-llm-provider-go
```

Or with a specific version:

```bash
go get github.com/manishiitg/multi-llm-provider-go@v0.1.0
```

## Supported Providers

- **AWS Bedrock** - Claude models via Bedrock Runtime API
- **OpenAI** - GPT models (GPT-4, GPT-3.5, etc.)
- **Anthropic** - Claude models via direct API
- **OpenRouter** - Multi-provider access via OpenRouter API
- **Vertex AI** - Google Gemini models and Anthropic Claude via Vertex AI
- **Azure AI** - OpenAI models via Azure AI Services/Foundry
- **Z.AI** - GLM chat models via `api.z.ai` Coding Plan chat completions API
- **Claude Code CLI** - Local agentic CLI integration (`claude`)
- **MiniMax** - MiniMax-M2.7/M2.5/M2.1/M2 text models + image-01 image generation

## Quick Start

```go
package main

import (
    "context"
    "github.com/manishiitg/multi-llm-provider-go"
    "github.com/manishiitg/multi-llm-provider-go/interfaces"
)

func main() {
    // Initialize an LLM provider (e.g., Claude Code CLI)
    config := llmproviders.Config{
        Provider:    llmproviders.ProviderClaudeCode,
        ModelID:     "claude-code", // Uses local CLI authentication
        Temperature: 0.7,
        Logger:      yourLogger,
        EventEmitter: yourEventEmitter,
    }
    
    llm, err := llmproviders.InitializeLLM(config)
    if err != nil {
        panic(err)
    }
    
    // Generate content (Streaming)
    ctx := context.Background()
    streamChan := make(chan llmtypes.StreamChunk)
    
    go func() {
        response, err := llm.GenerateContent(ctx, []llmtypes.MessageContent{
            llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "Check the git status of this repo"),
        }, llmtypes.WithStreamingChan(streamChan))
        // Handle error/response...
    }()

    for chunk := range streamChan {
        fmt.Print(chunk.Content)
    }
}
```

## Module Structure

```
llm-providers/
├── cmd/
│   └── llm-test/              # Test binary
├── pkg/
│   ├── adapters/              # Provider-specific adapters
│   │   ├── bedrock/
│   │   ├── openai/
│   │   ├── anthropic/
│   │   ├── vertex/
│   │   ├── azure/
│   │   ├── minimax/           # MiniMax text + image adapter
│   │   └── claudecode/        # Claude Code CLI Adapter
│   └── interfaces/            # Public interfaces
├── internal/
│   └── testing/               # Test utilities
├── llmtypes/                  # Type definitions
├── providers.go               # Main provider initialization
├── events.go                  # Event definitions
└── types.go                   # Type re-exports
```

## Configuration

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
- **Claude Code**: Uses experimental interactive CLI mode. Requires `claude` and `tmux` 3.x+ binaries in PATH. Authenticate Claude Code via interactive login before use.

### Provider Configuration

Each provider can be configured with:
- Model ID
- Temperature
- Max tokens
- Fallback models (for rate limiting)
- Custom options

## Testing

Build and run the test tool:

```bash
cd llm-providers
make build
./bin/llm-test --help
```

## Test Coverage

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
| **Vertex AI** | imagen-4.0-generate-001 | $0.04/image | 1:1, 16:9, 9:16, 4:3, 3:4 | ❌ |
| **Vertex AI** | imagen-4.0-fast-generate-001 | $0.02/image | 1:1, 16:9, 9:16, 4:3, 3:4 | ❌ |
| **Vertex AI** | imagen-4.0-ultra-generate-001 | $0.06/image | 1:1, 16:9, 9:16, 4:3, 3:4 | ❌ |
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
