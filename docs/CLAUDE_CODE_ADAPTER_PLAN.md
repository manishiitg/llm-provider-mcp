# Plan: Claude Code CLI Adapter Implementation

## Objective
Implement a new LLM provider adapter for [Claude Code CLI](https://github.com/anthropics/claude-code) (`claude`). This will allow the library to use the local `claude` CLI as an LLM provider, leveraging its powerful agentic capabilities and efficient local context handling.

## Architecture & Design

### Multi-Turn Conversation Strategy (Stream-JSON)
The `llmtypes.Model` interface defines `GenerateContent` as a stateless operation where the entire conversation history is provided in the `messages []MessageContent` slice. 

To map this to the `claude` CLI, we will use a **stateless conversation playback approach piped via stdin in JSON format**:
1.  **Extract System Prompt:** Identify any system messages and pass them via the `--system-prompt` flag.
2.  **Construct JSON History:** Convert the remaining conversation history (User/Assistant turns) into a sequence of JSON objects (one per line).
3.  **Stateless Execution via Stdin:** Pipe the JSON objects to `claude -p --input-format stream-json --output-format json` via stdin.
4.  **Rationale:** This approach is the native protocol used by Claude's official IDE integrations. It avoids the ambiguity of text-based transcripts, bypasses command-line argument limits, and correctly preserves complex conversation state (including tool calls/results in the future).

### Tool Management & Capabilities
Claude Code is an agentic CLI with a built-in toolset. Our adapter will handle tools with the following principles:
1.  **Default Capabilities:** By default, the CLI enables a core set of tools including `Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`, and `WebSearch`.
2.  **WebSearch:** We will ensure `WebSearch` remains enabled by default, as it is a core value-add of the CLI provider.
3.  **Core Tool Persistence:** Core tools (File operations, Bash, Task) are intrinsic to the CLI's operation as an agent. While the `--tools` and `--disallowedTools` flags exist, they may not fully disable these core capabilities if the model's internal logic requires them.
4.  **Explicit Control:** The adapter will support mapping library-level tool definitions to the CLI's `--tools` (whitelist) and `--disallowedTools` (blacklist) flags for advanced users.

### Permission Handling (Interactive Loop)
Claude Code CLI enforces permissions for sensitive tools (like Bash or Write).
1.  **Detection:** The adapter will detect `permission_denials` in the JSON response.
2.  **Human-in-the-Loop:** If denied, the adapter will trigger an `ask_user` tool call (simulated or real depending on agent config) to request permission.
3.  **Resolution:** Upon approval, the agent loop (outside the adapter) is responsible for re-invoking the model with the approval context or appropriate flags (e.g., `--dangerously-skip-permissions` if configured for trusted execution).
4.  **Autonomous Mode:** Users can optionally enable `DangerouslySkipPermissions` in the adapter config to bypass all checks entirely.

### Ephemeral MCP Configuration
Claude Code supports ephemeral MCP server configuration for a single session via the `--mcp-config` flag.
1.  **Configuration:** A JSON string defining the MCP servers (SSE or Stdio).
2.  **Execution:** Pass the configuration to the CLI using `--mcp-config '<json_string>'`.
3.  **Use Case:** Allows an agent to dynamically connect to tools like `docfork` for specific tasks without modifying the user's global configuration.

### Component Breakdown

#### 1. Provider Registration (`providers.go`)
- Add `ProviderClaudeCode Provider = "claude-code"` to the enum.
- Add `claudecodeadapter` to the imports.
- Update `InitializeLLM` to handle the new provider.
- Implement `initializeClaudeCode` helper.

#### 2. Adapter Implementation (`pkg/adapters/claudecode/claudecode_adapter.go`)
- **`ClaudeCodeAdapter` Struct:** Holds `modelID` and `logger`.
- **`GenerateContent` Method:**
    - Parse `llmtypes.CallOptions`.
    - Extract system prompt and construct the `stream-json` history.
    - Check for ephemeral MCP configuration in metadata/options.
    - Execute `claude` with `--input-format stream-json`, `--output-format json`, `--system-prompt`, and `--mcp-config`.
    - Set `cmd.Stdin` to the JSON stream.
    - Parse the final JSON response for `result`, `usage`, and `permission_denials`.

#### 3. Data Mapping & Usage Tracking
| CLI JSON Field | Go Struct Field | Description |
| :--- | :--- | :--- |
| `result` | `Choices[0].Content` | The generated text content. |
| `usage.input_tokens` | `Usage.InputTokens` | Total tokens in the prompt. |
| `usage.output_tokens` | `Usage.OutputTokens` | Tokens generated in the response. |
| `usage.cache_read_input_tokens` | `Usage.CacheTokens` | Tokens read from the prompt cache. |
| `total_cost_usd` | `GenerationInfo.Additional["cost_usd"]` | Estimated cost of the request. |
| `permission_denials` | `GenerationInfo.Additional["permission_denials"]` | List of denied tool calls. |

## Implementation Phases

### Phase 1: Core Support (Current Focus)
- Basic provider registration.
- Multi-turn conversation via `stream-json` piping.
- System prompt and Ephemeral MCP configuration support.
- JSON response parsing (text + usage + cost + permission_denials).
- Error handling for CLI execution failures.

### Phase 2: Advanced Features (Current)
- **Streaming:** Utilizing `--output-format stream-json` for real-time response chunks via `StreamChan`.
- **Enhanced Execution:** Non-blocking pipe architecture for CLI communication.

### Phase 3: Future Enhancements
- **Image Support:** Mapping `ImageContent` to CLI --file flags or JSON content.
- **Library Tool Integration:** Mapping Go-defined tools to CLI tool calls via ephemeral MCP servers.

## Verification Strategy
1.  **Unit Tests:** Test `stream-json` construction and response parsing.
2.  **Integration Tests:** Verify multi-turn context and ephemeral MCP tool discovery.
3.  **Manual CLI Check:**
    - `echo '{"type": "user", "message": {"role": "user", "content": [{"type": "text", "text": "Hi"}]}}' | claude -p --input-format stream-json --output-format json --mcp-config '{"mcpServers":{...}}'`

## Security Considerations
- **Command Injection:** Pass JSON and flags as direct arguments to `exec.Command`.
- **Secret Handling:** Ensure MCP configurations containing API keys are handled securely in memory.
