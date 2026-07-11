# Go Compatibility Library

The repository includes a general Go provider module used by MCP Agent and MCP
Agent Builder. It predates the `llm-provider-mcp` product and remains supported
as a compatibility surface for those downstream repositories.

The module path intentionally remains:

```bash
go get github.com/manishiitg/multi-llm-provider-go@latest
```

## Provider Families

API and cloud providers:

- AWS Bedrock
- OpenAI
- Anthropic
- OpenRouter
- Google Vertex AI
- Azure AI
- Z.AI
- Kimi
- MiniMax and MiniMax Coding Plan

Coding-agent adapters:

- Claude Code
- Codex CLI
- Cursor Agent
- Pi CLI
- Gemini CLI and Antigravity CLI for deprecated compatibility

Media providers:

- ElevenLabs
- Deepgram

## Capabilities

Depending on the provider, the common interfaces cover:

- Text generation and streaming
- Tool calling
- Structured output
- Token usage and model metadata
- Embeddings
- Image input and image generation
- Audio generation and transcription
- Music generation
- CLI-native coding-agent execution

## Basic Initialization

```go
model, err := llmproviders.InitializeLLM(llmproviders.Config{
    Provider: llmproviders.ProviderOpenAI,
    ModelID:  "gpt-5.4",
    APIKey:   os.Getenv("OPENAI_API_KEY"),
    Logger:   logger,
})
if err != nil {
    return err
}
```

See `.env.example`, package documentation, and the adapter tests for the
provider-specific configuration currently supported.

## Compatibility Policy

- Existing exported APIs used by MCP Agent and MCP Agent Builder are protected
  by downstream compile checks in CI.
- New product-facing functionality should be added through the coding-agent MCP
  packages unless it is genuinely reusable provider behavior.
- Deprecated exported APIs are removed only in a documented breaking release.
- The repository rename does not change the Go module path.

## Testing

```bash
go test ./...
make build
./bin/llm-test --help
```

Real provider tests require the corresponding native CLI or API credentials and
are opt-in. Replay and contract tests remain the normal CI path.
