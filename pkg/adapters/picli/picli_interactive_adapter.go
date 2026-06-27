package picli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/shelllaunch"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxcontrol"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxexec"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
)

const (
	defaultPiInteractiveIdleTimeout = 20 * time.Minute
	defaultPiInteractiveRetention   = 30 * time.Minute

	EnvPiBinary                         = "PI_BIN"
	EnvPiInteractiveSessionPrefix       = "PI_CLI_INTERACTIVE_SESSION_PREFIX"
	EnvPiInteractiveIdleTimeoutSeconds  = "PI_CLI_INTERACTIVE_IDLE_TIMEOUT_SECONDS"
	EnvPiInteractivePromptWaitSeconds   = "PI_CLI_INTERACTIVE_PROMPT_WAIT_SECONDS"
	EnvPiInteractiveStreamTmuxScreen    = "PI_CLI_STREAM_TMUX_SCREEN"
	EnvPiInteractiveUseNpxFallback      = "PI_CLI_USE_NPX_FALLBACK"
	EnvPiInteractiveNpmIgnoreScripts    = "PI_CLI_NPM_IGNORE_SCRIPTS"
	EnvPiInteractiveRetentionSeconds    = "PI_CLI_INTERACTIVE_RETENTION_SECONDS"
	EnvPiStatuslineExtension            = "PI_CLI_STATUSLINE_EXTENSION"
	defaultPiInteractiveNpxPackage      = "@earendil-works/pi-coding-agent"
	defaultPiProvider                   = "google"
	defaultPiModel                      = "gemini-3.5-flash"
	piInteractiveMarkerExtensionFile    = "mlp-marker.ts"
	piInteractiveMarkerJSONLFile        = "markers.jsonl"
	piInteractiveMarkerPollInterval     = 100 * time.Millisecond
	piInteractiveTerminalPollInterval   = 750 * time.Millisecond
	piInteractiveTerminalScrollbackLine = 10000
)

type piInteractiveSession struct {
	ownerSessionID    string
	nativeSessionID   string
	tmuxSessionName   string
	workingDir        string
	tempDir           string
	extensionPath     string
	markerPath        string
	persistent        bool
	idleTimer         *time.Timer
	createdAt         time.Time
	lastUsed          time.Time
	modelID           string
	provider          string
	cleanupFiles      func()
	releaseMCPLease   func()
	mcpFingerprint    string
	bridgeOnlyTools   bool
	mcpExtension      string
	tokenUsageSource  string
	transcriptPath    string
	costUSD           float64
	inputCostUSD      float64
	outputCostUSD     float64
	cacheReadCostUSD  float64
	cacheWriteCostUSD float64
	cacheReadTokens   int
	cacheWriteTokens  int
	inputTokens       int
	outputTokens      int
	totalInputTokens  int
	totalOutputTokens int
	mu                sync.Mutex
}

var piInteractiveRegistry = struct {
	sync.Mutex
	sessions map[string]*piInteractiveSession
}{
	sessions: map[string]*piInteractiveSession{},
}

var piWorkspaceMCPConfigRegistry = struct {
	sync.Mutex
	leases map[string]map[*piInteractiveSession]string
}{
	leases: map[string]map[*piInteractiveSession]string{},
}

func (p *PiCLIAdapter) generateContentTmux(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; pi-cli tmux mode requires tmux")
	}
	if _, _, err := piCommandPrefix(); err != nil {
		return nil, err
	}

	persistent := piPersistentInteractiveFromOptions(opts)
	ownerSessionID := piInteractiveSessionIDFromOptions(opts)
	if ownerSessionID == "" {
		ownerSessionID = "pi-bounded-" + piRandomHex(8)
	}

	systemPrompt, conversationMessages := splitPiSystemPrompt(messages)
	launchOnly := llmtypes.CodingProviderLaunchOnlyFromOptions(opts)
	prompt := buildPiPrompt(conversationMessages)
	if opts != nil && opts.JSONSchema != nil && opts.JSONSchema.Schema != nil {
		schemaBytes, err := json.Marshal(opts.JSONSchema.Schema)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal JSON schema: %w", err)
		}
		if strings.TrimSpace(prompt) != "" && !strings.HasSuffix(prompt, "\n") {
			prompt += "\n"
		}
		prompt += "\nReturn a response that conforms to this JSON schema:\n" + string(schemaBytes) + "\n"
	}
	if !launchOnly && strings.TrimSpace(prompt) == "" {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("pi-cli prompt is empty")
	}

	session, err := p.acquirePiInteractiveSession(ctx, ownerSessionID, persistent, opts, systemPrompt)
	if err != nil {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	releaseSession := true
	defer func() {
		if !releaseSession || session == nil {
			return
		}
		if persistent {
			releasePiInteractiveSession(session)
		} else {
			releasePiBoundedInteractiveSession(session)
		}
	}()

	if err := waitForPiMarkerType(ctx, session.markerPath, "session_start", 0, piPromptWait()); err != nil {
		releaseSession = false
		session.mu.Unlock()
		cleanupPiInteractiveSession(session)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("failed to start Pi CLI tmux session %q: %w", session.tmuxSessionName, err)
	}

	if launchOnly {
		streamPiTerminalSnapshot(ctx, session.tmuxSessionName, opts.StreamChan)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		genInfo := &llmtypes.GenerationInfo{Additional: piResponseAdditional(session, persistent)}
		llmtypes.AttachCodingProviderSessionHandle(genInfo, piSessionHandle(session, llmtypes.CodingProviderSessionStatusIdle))
		return &llmtypes.ContentResponse{
			Choices: []*llmtypes.ContentChoice{{
				Content:        "",
				GenerationInfo: genInfo,
			}},
		}, nil
	}

	startOffset, _ := piMarkerFileSize(session.markerPath)
	p.logInfof("Executing Pi CLI tmux session: %s", session.tmuxSessionName)
	turnStart := time.Now().Add(-1 * time.Second)
	if err := sendPiInputToTmux(ctx, session.tmuxSessionName, prompt); err != nil {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	content, err := waitForPiInteractiveResponse(ctx, session, startOffset, opts.StreamChan)
	forcedComplete := errors.Is(err, tmuxcontrol.ErrForceComplete)
	if err != nil && !forcedComplete {
		if isPiTmuxSessionLostError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			releaseSession = false
			session.mu.Unlock()
			cleanupPiInteractiveSession(session)
		}
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	if forcedComplete && strings.TrimSpace(content) == "" {
		if captured, captureErr := capturePiPane(ctx, session.tmuxSessionName); captureErr == nil {
			content = strings.TrimSpace(captured)
		}
	}

	inputTokens, outputTokens := estimatePiTmuxTokens(prompt, content)
	totalTokens := inputTokens + outputTokens
	tokenUsageSource := "estimated"
	var transcriptMessages []llmtypes.MessageContent
	session.transcriptPath = ""
	session.cacheReadTokens = 0
	session.cacheWriteTokens = 0
	session.costUSD = 0
	session.inputCostUSD = 0
	session.outputCostUSD = 0
	session.cacheReadCostUSD = 0
	session.cacheWriteCostUSD = 0
	if transcript := readPiTranscriptSummary(session.nativeSessionID, turnStart); transcript != nil {
		session.transcriptPath = transcript.Path
		if len(transcript.Messages) > 0 {
			transcriptMessages = transcript.Messages
		}
		if transcript.hasUsage() {
			inputTokens = transcript.InputTokens
			outputTokens = transcript.OutputTokens
			totalTokens = transcript.TotalTokens
			tokenUsageSource = "transcript-file"
			session.cacheReadTokens = transcript.CacheReadTokens
			session.cacheWriteTokens = transcript.CacheWriteTokens
			session.costUSD = transcript.TotalCostUSD
			session.inputCostUSD = transcript.InputCostUSD
			session.outputCostUSD = transcript.OutputCostUSD
			session.cacheReadCostUSD = transcript.CacheReadCostUSD
			session.cacheWriteCostUSD = transcript.CacheWriteCostUSD
		}
	}
	session.inputTokens = inputTokens
	session.outputTokens = outputTokens
	session.tokenUsageSource = tokenUsageSource
	session.totalInputTokens += inputTokens
	session.totalOutputTokens += outputTokens
	streamPiStatusLine(ctx, session, opts.StreamChan)

	llmtypes.RunTrailingPaneCapture(ctx, opts.StreamChan,
		func(ctx context.Context) (string, error) {
			snap, err := capturePiPane(ctx, session.tmuxSessionName)
			if err != nil {
				return "", err
			}
			return strings.TrimRight(snap, "\n"), nil
		},
		map[string]interface{}{
			"tmux_session":           session.tmuxSessionName,
			"pi_interactive_session": session.tmuxSessionName,
		},
	)
	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

	additional := piResponseAdditional(session, persistent)
	additional["pi_token_usage_source"] = tokenUsageSource
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  intPtr(inputTokens),
		OutputTokens: intPtr(outputTokens),
		TotalTokens:  intPtr(totalTokens),
		Additional:   additional,
	}
	if session.cacheReadTokens > 0 {
		genInfo.CachedContentTokens = intPtr(session.cacheReadTokens)
	}
	if len(transcriptMessages) == 0 {
		transcriptMessages = []llmtypes.MessageContent{
			llmtypes.TextPart(llmtypes.ChatMessageTypeAI, content),
		}
	}
	llmtypes.AttachCodingProviderIntermediateMessages(genInfo, llmtypes.CodingProviderIntermediateMessages{
		Provider:  "pi-cli",
		Transport: llmtypes.CodingProviderTransportTmux,
		Messages:  transcriptMessages,
	})
	llmtypes.AttachCodingProviderSessionHandle(genInfo, piSessionHandle(session, llmtypes.CodingProviderSessionStatusIdle))

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{{
			Content:        strings.TrimSpace(content),
			GenerationInfo: genInfo,
		}},
		Usage: &llmtypes.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  totalTokens,
		},
	}, nil
}

func (p *PiCLIAdapter) acquirePiInteractiveSession(ctx context.Context, ownerSessionID string, persistent bool, opts *llmtypes.CallOptions, systemPrompt string) (*piInteractiveSession, error) {
	workingDir := piWorkingDirFromOptions(opts)
	if workingDir == "" {
		var err error
		workingDir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create Pi working dir: %w", err)
	}

	providerOverride := piProviderFromOptions(opts)
	provider, model := resolvePiProviderModel(p.GetModelID(), providerOverride)
	modelID := provider + "/" + model
	mcpConfig := piMCPConfigFromOptions(opts)
	mcpFingerprint := piMCPConfigFingerprint(mcpConfig)
	bridgeOnlyTools := piBridgeOnlyToolsFromOptions(opts)
	mcpExtension := piMCPExtensionFromOptions(opts)
	requestedNativeSessionID := piResumeSessionIDFromOptions(opts)

	if persistent {
		piInteractiveRegistry.Lock()
		if existing := piInteractiveRegistry.sessions[ownerSessionID]; existing != nil {
			sameLaunch := existing.workingDir == workingDir &&
				existing.modelID == modelID &&
				existing.provider == provider &&
				existing.mcpFingerprint == mcpFingerprint &&
				existing.bridgeOnlyTools == bridgeOnlyTools &&
				existing.mcpExtension == mcpExtension &&
				(requestedNativeSessionID == "" || existing.nativeSessionID == requestedNativeSessionID)
			if sameLaunch && piTmuxSessionExists(ctx, existing.tmuxSessionName) {
				if existing.idleTimer != nil {
					existing.idleTimer.Stop()
					existing.idleTimer = nil
				}
				piInteractiveRegistry.Unlock()
				existing.mu.Lock()
				existing.lastUsed = time.Now()
				return existing, nil
			}
			delete(piInteractiveRegistry.sessions, ownerSessionID)
			piInteractiveRegistry.Unlock()
			cleanupPiInteractiveSession(existing)
		} else {
			piInteractiveRegistry.Unlock()
		}
	}

	nativeSessionID := requestedNativeSessionID
	if nativeSessionID == "" {
		nativeSessionID = generatePiNativeSessionID()
	}

	session, err := p.startPiInteractiveSession(ctx, ownerSessionID, nativeSessionID, workingDir, persistent, provider, model, systemPrompt, opts)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	if persistent {
		piInteractiveRegistry.Lock()
		piInteractiveRegistry.sessions[ownerSessionID] = session
		piInteractiveRegistry.Unlock()
	}
	return session, nil
}

func (p *PiCLIAdapter) startPiInteractiveSession(ctx context.Context, ownerSessionID, nativeSessionID, workingDir string, persistent bool, provider, model, systemPrompt string, opts *llmtypes.CallOptions) (*piInteractiveSession, error) {
	tempDir, err := os.MkdirTemp("", "pi-cli-interactive-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create Pi temp dir: %w", err)
	}
	var cleanupFiles func()
	var releaseMCPLease func()
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			if cleanupFiles != nil {
				cleanupFiles()
			}
			if releaseMCPLease != nil {
				releaseMCPLease()
			}
			_ = os.RemoveAll(tempDir)
		}
	}()

	extensionPath := filepath.Join(tempDir, piInteractiveMarkerExtensionFile)
	if err := os.WriteFile(extensionPath, []byte(piMarkerExtensionSource()), 0o600); err != nil {
		return nil, fmt.Errorf("failed to write Pi marker extension: %w", err)
	}
	markerPath := filepath.Join(tempDir, piInteractiveMarkerJSONLFile)
	if err := os.WriteFile(markerPath, nil, 0o600); err != nil {
		return nil, fmt.Errorf("failed to initialize Pi marker file: %w", err)
	}

	sessionName := piInteractiveSessionPrefix() + "-" + piRandomHex(12)
	mcpConfig := piMCPConfigFromOptions(opts)
	bridgeOnlyTools := piBridgeOnlyToolsFromOptions(opts)
	mcpExtension := piMCPExtensionFromOptions(opts)
	session := &piInteractiveSession{
		ownerSessionID:  ownerSessionID,
		nativeSessionID: nativeSessionID,
		tmuxSessionName: sessionName,
		workingDir:      workingDir,
		tempDir:         tempDir,
		extensionPath:   extensionPath,
		markerPath:      markerPath,
		persistent:      persistent,
		createdAt:       time.Now(),
		lastUsed:        time.Now(),
		modelID:         provider + "/" + model,
		provider:        provider,
		mcpFingerprint:  piMCPConfigFingerprint(mcpConfig),
		bridgeOnlyTools: bridgeOnlyTools,
		mcpExtension:    mcpExtension,
	}
	releaseMCPLease, err = acquirePiWorkspaceMCPConfigLease(workingDir, mcpConfig, session)
	if err != nil {
		return nil, err
	}
	session.releaseMCPLease = releaseMCPLease
	cleanupFiles, err = preparePiProjectFiles(workingDir, opts)
	if err != nil {
		return nil, err
	}
	session.cleanupFiles = cleanupFiles
	args, env, err := p.piLaunchArgs(provider, model, extensionPath, markerPath, systemPrompt, nativeSessionID, opts)
	if err != nil {
		return nil, err
	}
	launchScriptPath := filepath.Join(tempDir, "launch-pi.sh")
	if err := writePiLaunchScript(launchScriptPath, args); err != nil {
		return nil, err
	}
	release, err := tmuxlaunch.Acquire(ctx, "pi-cli", sessionName)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := startPiTmuxSession(ctx, sessionName, []string{launchScriptPath}, env, workingDir); err != nil {
		return nil, err
	}

	cleanupOnError = false
	return session, nil
}

func (p *PiCLIAdapter) piLaunchArgs(provider, model, extensionPath, markerPath, systemPrompt, nativeSessionID string, opts *llmtypes.CallOptions) ([]string, []string, error) {
	args, env, err := piCommandPrefix()
	if err != nil {
		return nil, nil, err
	}
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	if nativeSessionID == "" {
		return nil, nil, fmt.Errorf("pi native session id is required")
	}
	if !isValidPiNativeSessionID(nativeSessionID) {
		return nil, nil, fmt.Errorf("pi native session id %q is invalid", nativeSessionID)
	}
	mcpConfig := piMCPConfigFromOptions(opts)
	bridgeOnlyTools := piBridgeOnlyToolsFromOptions(opts)
	if bridgeOnlyTools && strings.TrimSpace(mcpConfig) == "" {
		return nil, nil, fmt.Errorf("pi bridge-only tools require an MCP config")
	}
	args = append(args,
		"--provider", provider,
		"--model", model,
		"--no-extensions",
		"-e", extensionPath,
	)
	if statuslineExtension := piStatuslineExtensionFromOptions(opts); statuslineExtension != "" {
		args = append(args, "-e", statuslineExtension)
	}
	if strings.TrimSpace(mcpConfig) != "" {
		args = append(args, "-e", piMCPExtensionFromOptions(opts))
	}
	args = append(args,
		"--no-skills",
		"--no-context-files",
		"--session-id", nativeSessionID,
	)
	if sessionDir := piConfiguredTranscriptSessionDir(); sessionDir != "" {
		if err := os.MkdirAll(sessionDir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("failed to create Pi session dir %s: %w", sessionDir, err)
		}
		args = append(args, "--session-dir", sessionDir)
		env = append(env, "PI_CODING_AGENT_SESSION_DIR="+sessionDir)
	}
	if bridgeOnlyTools {
		args = append(args, "--no-builtin-tools")
	}
	if strings.TrimSpace(systemPrompt) != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	env = append(env, "MLP_PI_MARKER_FILE="+markerPath)
	env = append(env, piAPIKeyEnv(provider, p.apiKey)...)
	env = append(env, piBridgeShellEnvFromMCPConfig(mcpConfig)...)
	return args, env, nil
}

func writePiLaunchScript(path string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("pi launch script requires at least one command argument")
	}
	body := "#!/bin/sh\nexec " + shelllaunch.Join(args) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		return fmt.Errorf("failed to write Pi launch script %s: %w", path, err)
	}
	return nil
}

func preparePiProjectFiles(workingDir string, opts *llmtypes.CallOptions) (func(), error) {
	mcpConfig := piMCPConfigFromOptions(opts)
	if strings.TrimSpace(mcpConfig) == "" {
		return nil, nil
	}
	normalizedMCPConfig, err := normalizePiMCPConfig(mcpConfig)
	if err != nil {
		return nil, err
	}
	mcpPath := filepath.Join(workingDir, ".pi", "mcp.json")
	cleanup, err := writePiRestoredFile(mcpPath, normalizedMCPConfig)
	if err != nil {
		return nil, err
	}
	return cleanup, nil
}

func normalizePiMCPConfig(configJSON string) ([]byte, error) {
	decoder := json.NewDecoder(strings.NewReader(configJSON))
	decoder.UseNumber()
	var config map[string]interface{}
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("pi MCP config is not valid JSON: %w", err)
	}
	servers, ok := config["mcpServers"].(map[string]interface{})
	if !ok || len(servers) == 0 {
		return nil, fmt.Errorf("pi MCP config must contain a non-empty mcpServers object")
	}
	if rawBridge, ok := servers["api-bridge"]; ok {
		bridge, ok := rawBridge.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("pi MCP config api-bridge server must be an object")
		}
		if _, exists := bridge["directTools"]; !exists {
			bridge["directTools"] = true
		}
		if _, exists := bridge["lifecycle"]; !exists {
			bridge["lifecycle"] = "keep-alive"
		}
	}
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode Pi MCP config: %w", err)
	}
	return append(body, '\n'), nil
}

func writePiRestoredFile(path string, content []byte) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create Pi config dir: %w", err)
	}
	var previous []byte
	existed := false
	if data, err := os.ReadFile(path); err == nil {
		previous = data
		existed = true
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read existing Pi config %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write Pi config %s: %w", path, err)
	}
	return func() {
		if existed {
			_ = os.WriteFile(path, previous, 0o600)
		} else {
			_ = os.Remove(path)
			_ = os.Remove(filepath.Dir(path))
		}
	}, nil
}

func acquirePiWorkspaceMCPConfigLease(workingDir, mcpConfig string, session *piInteractiveSession) (func(), error) {
	if strings.TrimSpace(mcpConfig) == "" || session == nil {
		return nil, nil
	}
	key := cleanPiWorkingDirKey(workingDir)
	fingerprint := piMCPConfigFingerprint(mcpConfig)

	piWorkspaceMCPConfigRegistry.Lock()
	defer piWorkspaceMCPConfigRegistry.Unlock()
	leases := piWorkspaceMCPConfigRegistry.leases[key]
	for existing, existingFingerprint := range leases {
		if existing == nil || existing == session {
			continue
		}
		if existingFingerprint != fingerprint {
			return nil, fmt.Errorf("pi-cli does not support concurrent sessions in working directory %s with different MCP configs; use separate working directories or the same bridge config", workingDir)
		}
	}
	if leases == nil {
		leases = map[*piInteractiveSession]string{}
		piWorkspaceMCPConfigRegistry.leases[key] = leases
	}
	leases[session] = fingerprint
	released := false
	return func() {
		piWorkspaceMCPConfigRegistry.Lock()
		defer piWorkspaceMCPConfigRegistry.Unlock()
		if released {
			return
		}
		released = true
		current := piWorkspaceMCPConfigRegistry.leases[key]
		delete(current, session)
		if len(current) == 0 {
			delete(piWorkspaceMCPConfigRegistry.leases, key)
		}
	}, nil
}

func cleanPiWorkingDirKey(workingDir string) string {
	if abs, err := filepath.Abs(strings.TrimSpace(workingDir)); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(strings.TrimSpace(workingDir))
}

func piMCPConfigFingerprint(config string) string {
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(config), &decoded); err == nil {
		if mcpServers, ok := decoded["mcpServers"].(map[string]interface{}); ok {
			for _, srv := range mcpServers {
				if srvMap, ok := srv.(map[string]interface{}); ok {
					if env, ok := srvMap["env"].(map[string]interface{}); ok {
						delete(env, "MCP_SESSION_ID")
						delete(env, "MCP_VIRTUAL_SCOPE_ID")
						if apiURL, ok := env["MCP_API_URL"].(string); ok {
							if idx := strings.Index(apiURL, "/s/"); idx != -1 {
								env["MCP_API_URL"] = apiURL[:idx]
							}
						}
					}
				}
			}
		}
		if canonical, err := json.Marshal(decoded); err == nil {
			config = string(canonical)
		}
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(config)))
	return hex.EncodeToString(sum[:])
}

func piCommandPrefix() ([]string, []string, error) {
	if configured := strings.TrimSpace(os.Getenv(EnvPiBinary)); configured != "" {
		return []string{configured}, piNpmEnv(), nil
	}
	if path, err := exec.LookPath("pi"); err == nil {
		return []string{path}, piNpmEnv(), nil
	}
	if piNpxFallbackEnabled() {
		if path, err := exec.LookPath("npx"); err == nil {
			return []string{path, "--yes", defaultPiInteractiveNpxPackage}, piNpmEnv(), nil
		}
	}
	return nil, nil, fmt.Errorf("pi not found in PATH and npx fallback is unavailable. Install with `npm install -g --ignore-scripts @earendil-works/pi-coding-agent`, set PI_BIN, or install npx")
}

func piNpxFallbackEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvPiInteractiveUseNpxFallback))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func piNpmEnv() []string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvPiInteractiveNpmIgnoreScripts))) {
	case "0", "false", "no", "off":
		return nil
	default:
		return []string{"npm_config_ignore_scripts=true"}
	}
}

func piAPIKeyEnv(provider, apiKey string) []string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "google", "google-vertex":
		return []string{"GEMINI_API_KEY=" + apiKey, "GOOGLE_API_KEY=" + apiKey, "PI_API_KEY=" + apiKey}
	case "openai":
		return []string{"OPENAI_API_KEY=" + apiKey}
	case "anthropic":
		return []string{"ANTHROPIC_API_KEY=" + apiKey}
	case "openrouter":
		return []string{"OPENROUTER_API_KEY=" + apiKey}
	case "deepseek":
		return []string{"DEEPSEEK_API_KEY=" + apiKey}
	case "nvidia":
		return []string{"NVIDIA_API_KEY=" + apiKey}
	case "mistral":
		return []string{"MISTRAL_API_KEY=" + apiKey}
	case "groq":
		return []string{"GROQ_API_KEY=" + apiKey}
	case "cerebras":
		return []string{"CEREBRAS_API_KEY=" + apiKey}
	case "xai":
		return []string{"XAI_API_KEY=" + apiKey}
	case "zai":
		return []string{"ZAI_API_KEY=" + apiKey}
	case "zai-coding-cn":
		return []string{"ZAI_CODING_CN_API_KEY=" + apiKey}
	case "opencode", "opencode-go":
		return []string{"OPENCODE_API_KEY=" + apiKey}
	case "fireworks":
		return []string{"FIREWORKS_API_KEY=" + apiKey}
	case "together":
		return []string{"TOGETHER_API_KEY=" + apiKey}
	case "kimi-coding", "moonshotai", "moonshotai-cn":
		return []string{"KIMI_API_KEY=" + apiKey}
	case "minimax":
		return []string{"MINIMAX_API_KEY=" + apiKey}
	case "minimax-cn":
		return []string{"MINIMAX_CN_API_KEY=" + apiKey}
	case "vercel-ai-gateway":
		return []string{"AI_GATEWAY_API_KEY=" + apiKey}
	default:
		normalized := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(strings.TrimSpace(provider))))
		if normalized == "" {
			return []string{"PI_API_KEY=" + apiKey}
		}
		return []string{normalized + "_API_KEY=" + apiKey}
	}
}

func piBridgeShellEnvFromMCPConfig(configJSON string) []string {
	bridgeEnv := piBridgeEnvFromMCPConfig(configJSON)
	if len(bridgeEnv) == 0 {
		return nil
	}

	apiURL := strings.TrimRight(strings.TrimSpace(bridgeEnv["MCP_API_URL"]), "/")
	sessionID := strings.TrimSpace(bridgeEnv["MCP_SESSION_ID"])
	scopedURL := piSessionScopedMCPURL(apiURL, sessionID)
	token := strings.TrimSpace(bridgeEnv["MCP_API_TOKEN"])

	out := map[string]string{}
	if scopedURL != "" {
		out["MCP_API_URL"] = scopedURL
		out["MCP_MCP"] = scopedURL + "/tools/mcp"
		out["MCP_CUSTOM"] = scopedURL + "/tools/custom"
		out["MCP_VIRTUAL"] = scopedURL + "/tools/virtual"
	}
	if token != "" {
		out["MCP_API_TOKEN"] = token
		out["MCP_AUTH"] = "Authorization: Bearer " + token
	}
	if sessionID != "" {
		out["MCP_SESSION_ID"] = sessionID
	}
	if virtualScopeID := strings.TrimSpace(bridgeEnv["MCP_VIRTUAL_SCOPE_ID"]); virtualScopeID != "" {
		out["MCP_VIRTUAL_SCOPE_ID"] = virtualScopeID
	}
	if len(out) == 0 {
		return nil
	}

	keys := make([]string, 0, len(out))
	for key := range out {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+out[key])
	}
	return env
}

func piBridgeEnvFromMCPConfig(configJSON string) map[string]string {
	decoder := json.NewDecoder(strings.NewReader(configJSON))
	decoder.UseNumber()
	var config map[string]interface{}
	if err := decoder.Decode(&config); err != nil {
		return nil
	}
	servers, _ := config["mcpServers"].(map[string]interface{})
	rawBridge, _ := servers["api-bridge"].(map[string]interface{})
	rawEnv, _ := rawBridge["env"].(map[string]interface{})
	if len(rawEnv) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"MCP_API_URL":          true,
		"MCP_API_TOKEN":        true,
		"MCP_SESSION_ID":       true,
		"MCP_VIRTUAL_SCOPE_ID": true,
	}
	env := make(map[string]string, len(allowed))
	for key, value := range rawEnv {
		if !allowed[key] {
			continue
		}
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			env[key] = s
		}
	}
	return env
}

func piSessionScopedMCPURL(apiURL, sessionID string) string {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" || strings.TrimSpace(sessionID) == "" || strings.Contains(apiURL, "/s/") {
		return apiURL
	}
	return apiURL + "/s/" + strings.TrimSpace(sessionID)
}

func startPiTmuxSession(ctx context.Context, sessionName string, args []string, env []string, workingDir string) error {
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	for _, entry := range env {
		if strings.TrimSpace(entry) != "" {
			tmuxArgs = append(tmuxArgs, "-e", entry)
		}
	}
	tmuxArgs = append(tmuxArgs, shelllaunch.Command(args, workingDir))
	if err := runPiCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		return fmt.Errorf("failed to start Pi interactive session %q: %w", sessionName, err)
	}
	_ = runPiCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on")
	if err := runPiCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "history-limit", tmuxexec.DefaultHistoryLimit); err != nil {
		return fmt.Errorf("failed to configure Pi tmux history for session %q: %w", sessionName, err)
	}
	_ = runPiCommand(ctx, nil, "tmux", "set-window-option", "-t", sessionName, "window-size", "manual")
	return nil
}

func sendPiInputToTmux(ctx context.Context, sessionName, message string) error {
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("Pi interactive input is empty")
	}
	bufferName := "mlp-pi-input-" + piRandomHex(6)
	tmp, err := os.CreateTemp("", "pi-tmux-input-*.txt")
	if err != nil {
		return fmt.Errorf("failed to create Pi tmux input temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(message); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write Pi tmux input temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close Pi tmux input temp file: %w", err)
	}
	if err := runPiCommand(ctx, nil, "tmux", "load-buffer", "-b", bufferName, tmpPath); err != nil {
		return fmt.Errorf("failed to load Pi input into tmux buffer: %w", err)
	}
	_ = runPiCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-u")
	if err := runPiCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-r", "-b", bufferName, "-t", sessionName); err != nil {
		return fmt.Errorf("failed to paste input into Pi interactive session: %w", err)
	}
	if err := runPiCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m"); err != nil {
		return fmt.Errorf("failed to submit input to Pi interactive session: %w", err)
	}
	return nil
}

type piMarker struct {
	Type       string `json:"type"`
	TS         int64  `json:"ts"`
	Reason     string `json:"reason,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Role       string `json:"role,omitempty"`
	UpdateType string `json:"updateType,omitempty"`
	Delta      string `json:"delta,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	IsError    *bool  `json:"isError,omitempty"`
}

func waitForPiInteractiveResponse(ctx context.Context, session *piInteractiveSession, offset int64, streamChan chan<- llmtypes.StreamChunk) (string, error) {
	ticker := time.NewTicker(piInteractiveMarkerPollInterval)
	defer ticker.Stop()
	terminalTicker := time.NewTicker(piInteractiveTerminalPollInterval)
	defer terminalTicker.Stop()

	var content strings.Builder
	currentOffset := offset
	var lastTerminal string
	toolStart := map[string]time.Time{}

	for {
		markers, nextOffset, err := readPiMarkersSince(session.markerPath, currentOffset)
		if err != nil {
			return content.String(), err
		}
		currentOffset = nextOffset
		for _, marker := range markers {
			switch marker.Type {
			case "message_update":
				if marker.UpdateType == "text_delta" && marker.Delta != "" {
					content.WriteString(marker.Delta)
					emitPiChunk(ctx, streamChan, llmtypes.StreamChunk{
						Type:     llmtypes.StreamChunkTypeContent,
						Content:  marker.Delta,
						Metadata: piChunkMetadata(session),
					})
				}
			case "tool_execution_start":
				toolStart[marker.ToolCallID] = time.Now()
				emitPiChunk(ctx, streamChan, llmtypes.StreamChunk{
					Type:       llmtypes.StreamChunkTypeToolCallStart,
					ToolName:   marker.ToolName,
					ToolCallID: marker.ToolCallID,
					Metadata:   piChunkMetadata(session),
				})
			case "tool_execution_end":
				duration := time.Duration(0)
				if start, ok := toolStart[marker.ToolCallID]; ok {
					duration = time.Since(start)
				}
				result := "ok"
				if marker.IsError != nil && *marker.IsError {
					result = "error"
				}
				emitPiChunk(ctx, streamChan, llmtypes.StreamChunk{
					Type:         llmtypes.StreamChunkTypeToolCallEnd,
					ToolName:     marker.ToolName,
					ToolCallID:   marker.ToolCallID,
					ToolResult:   result,
					ToolDuration: duration,
					Metadata:     piChunkMetadata(session),
				})
			case "agent_end":
				return strings.TrimSpace(content.String()), nil
			}
		}

		select {
		case <-ctx.Done():
			return content.String(), ctx.Err()
		case <-terminalTicker.C:
			if tmuxcontrol.ConsumeForceComplete(session.tmuxSessionName) {
				return content.String(), tmuxcontrol.ErrForceComplete
			}
			if streamPiTerminalSnapshotChanged(ctx, session.tmuxSessionName, streamChan, &lastTerminal) {
				continue
			}
		case <-ticker.C:
			if !piTmuxSessionExists(ctx, session.tmuxSessionName) {
				return content.String(), fmt.Errorf("Pi tmux session %q is no longer running", session.tmuxSessionName)
			}
		}
	}
}

func readPiMarkersSince(path string, offset int64) ([]piMarker, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, err
		}
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var markers []piMarker
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var marker piMarker
		if err := json.Unmarshal(line, &marker); err == nil && marker.Type != "" {
			markers = append(markers, marker)
		}
	}
	if err := scanner.Err(); err != nil {
		return markers, offset, err
	}
	nextOffset, err := f.Seek(0, 1)
	if err != nil {
		return markers, offset, err
	}
	return markers, nextOffset, nil
}

func waitForPiMarkerType(ctx context.Context, path, markerType string, offset int64, timeout time.Duration) error {
	waitCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	ticker := time.NewTicker(piInteractiveMarkerPollInterval)
	defer ticker.Stop()
	currentOffset := offset
	for {
		markers, nextOffset, err := readPiMarkersSince(path, currentOffset)
		if err == nil {
			currentOffset = nextOffset
			for _, marker := range markers {
				if marker.Type == markerType {
					return nil
				}
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func piMarkerFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func streamPiTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) {
	var last string
	streamPiTerminalSnapshotChanged(ctx, sessionName, streamChan, &last)
}

func streamPiTerminalSnapshotChanged(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, last *string) bool {
	if streamChan == nil {
		return false
	}
	snapshot, err := capturePiPane(ctx, sessionName)
	if err != nil || strings.TrimSpace(snapshot) == "" || snapshot == *last {
		return false
	}
	*last = snapshot
	emitPiChunk(ctx, streamChan, llmtypes.StreamChunk{
		Type:    llmtypes.StreamChunkTypeTerminal,
		Content: snapshot,
		Metadata: map[string]interface{}{
			"tmux_session": sessionName,
			"provider":     "pi-cli",
		},
	})
	return true
}

func streamPiStatusLine(ctx context.Context, session *piInteractiveSession, streamChan chan<- llmtypes.StreamChunk) bool {
	if streamChan == nil || session == nil {
		return false
	}
	status := piStatusLine(session)
	if status == nil {
		return false
	}
	emitPiChunk(ctx, streamChan, llmtypes.StreamChunk{
		Type:       llmtypes.StreamChunkTypeStatusLine,
		StatusLine: status,
		Metadata:   status.Metadata,
	})
	return true
}

func piStatusLine(session *piInteractiveSession) *llmtypes.StatusLine {
	if session == nil {
		return nil
	}
	metadata := map[string]interface{}{
		"tmux_session":              session.tmuxSessionName,
		"pi_interactive_session":    session.tmuxSessionName,
		"pi_session_id":             session.nativeSessionID,
		"pi_persistent_interactive": session.persistent,
		"pi_token_usage_source":     piTokenUsageSource(session),
	}
	if session.workingDir != "" {
		metadata["working_dir"] = session.workingDir
	}
	if session.transcriptPath != "" {
		metadata["pi_transcript_file"] = session.transcriptPath
	}
	if session.costUSD > 0 {
		metadata["cost_usd"] = session.costUSD
	}
	return &llmtypes.StatusLine{
		Provider:             "pi-cli",
		Model:                session.modelID,
		InputTokens:          session.inputTokens,
		OutputTokens:         session.outputTokens,
		CacheReadInputTokens: session.cacheReadTokens,
		TotalInputTokens:     session.totalInputTokens,
		TotalOutputTokens:    session.totalOutputTokens,
		CostUSD:              session.costUSD,
		Metadata:             metadata,
	}
}

func piTokenUsageSource(session *piInteractiveSession) string {
	if session == nil || strings.TrimSpace(session.tokenUsageSource) == "" {
		return "estimated"
	}
	return session.tokenUsageSource
}

func capturePiPane(ctx context.Context, sessionName string) (string, error) {
	return tmuxexec.CapturePane(ctx, sessionName, piInteractiveTerminalScrollbackLine)
}

func emitPiChunk(ctx context.Context, streamChan chan<- llmtypes.StreamChunk, chunk llmtypes.StreamChunk) {
	if streamChan == nil {
		return
	}
	select {
	case streamChan <- chunk:
	case <-ctx.Done():
	default:
	}
}

func piResponseAdditional(session *piInteractiveSession, persistent bool) map[string]interface{} {
	additional := map[string]interface{}{
		"provider":                  "pi-cli",
		"pi_mode":                   "tmux",
		"pi_interactive_session":    session.tmuxSessionName,
		"pi_session_id":             session.nativeSessionID,
		"pi_native_session_id":      session.nativeSessionID,
		"pi_persistent_interactive": persistent,
		"pi_working_dir":            session.workingDir,
		"pi_model":                  session.modelID,
		"pi_marker_file":            session.markerPath,
	}
	if !persistent {
		additional["pi_interactive_retention_seconds"] = int(piInteractiveRetention().Seconds())
	}
	if session.tokenUsageSource != "" {
		additional["pi_token_usage_source"] = session.tokenUsageSource
	}
	if session.transcriptPath != "" {
		additional["pi_transcript_file"] = session.transcriptPath
	}
	if session.cacheReadTokens > 0 {
		additional["cache_read_input_tokens"] = session.cacheReadTokens
	}
	if session.cacheWriteTokens > 0 {
		additional["cache_creation_input_tokens"] = session.cacheWriteTokens
	}
	if session.costUSD > 0 {
		additional["cost_usd"] = session.costUSD
		additional["input_cost_usd"] = session.inputCostUSD
		additional["output_cost_usd"] = session.outputCostUSD
		additional["cache_cost_usd"] = session.cacheReadCostUSD + session.cacheWriteCostUSD
		additional["pi_cost_input_usd"] = session.inputCostUSD
		additional["pi_cost_output_usd"] = session.outputCostUSD
		additional["pi_cost_cache_read_usd"] = session.cacheReadCostUSD
		additional["pi_cost_cache_write_usd"] = session.cacheWriteCostUSD
	}
	return additional
}

func piSessionHandle(session *piInteractiveSession, status string) llmtypes.CodingProviderSessionHandle {
	return llmtypes.CodingProviderSessionHandle{
		Provider:        "pi-cli",
		Transport:       llmtypes.CodingProviderTransportTmux,
		NativeSessionID: session.nativeSessionID,
		TmuxSession:     session.tmuxSessionName,
		WorkingDir:      session.workingDir,
		Model:           session.modelID,
		Status:          status,
	}
}

func piChunkMetadata(session *piInteractiveSession) map[string]interface{} {
	return map[string]interface{}{
		"provider":                  "pi-cli",
		"tmux_session":              session.tmuxSessionName,
		"pi_interactive_session":    session.tmuxSessionName,
		"pi_session_id":             session.nativeSessionID,
		"pi_persistent_interactive": session.persistent,
		"pi_model":                  session.modelID,
	}
}

func releasePiInteractiveSession(session *piInteractiveSession) {
	session.lastUsed = time.Now()
	session.mu.Unlock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
	}
	session.idleTimer = time.AfterFunc(piInteractiveIdleTimeout(), func() {
		piInteractiveRegistry.Lock()
		if current := piInteractiveRegistry.sessions[session.ownerSessionID]; current == session {
			delete(piInteractiveRegistry.sessions, session.ownerSessionID)
		}
		piInteractiveRegistry.Unlock()
		cleanupPiInteractiveSession(session)
	})
}

func releasePiBoundedInteractiveSession(session *piInteractiveSession) {
	session.mu.Unlock()
	time.AfterFunc(piInteractiveRetention(), func() {
		cleanupPiInteractiveSession(session)
	})
}

func cleanupPiInteractiveSession(session *piInteractiveSession) {
	if session == nil {
		return
	}
	piInteractiveRegistry.Lock()
	if current := piInteractiveRegistry.sessions[session.ownerSessionID]; current == session {
		delete(piInteractiveRegistry.sessions, session.ownerSessionID)
	}
	piInteractiveRegistry.Unlock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
	}
	_ = tmuxexec.RunCommand(context.Background(), nil, piRedactArgs, "tmux", "kill-session", "-t", session.tmuxSessionName)
	if session.tempDir != "" {
		_ = os.RemoveAll(session.tempDir)
	}
	if session.cleanupFiles != nil {
		session.cleanupFiles()
		session.cleanupFiles = nil
	}
	if session.releaseMCPLease != nil {
		session.releaseMCPLease()
		session.releaseMCPLease = nil
	}
}

// CleanupPiCLIInteractiveSessions removes Pi CLI tmux sessions registered by
// this process.
func CleanupPiCLIInteractiveSessions(ctx context.Context) error {
	piInteractiveRegistry.Lock()
	sessions := make([]*piInteractiveSession, 0, len(piInteractiveRegistry.sessions))
	for _, session := range piInteractiveRegistry.sessions {
		sessions = append(sessions, session)
	}
	piInteractiveRegistry.sessions = map[string]*piInteractiveSession{}
	piInteractiveRegistry.Unlock()

	var errs []error
	for _, session := range sessions {
		if session.idleTimer != nil {
			session.idleTimer.Stop()
		}
		if err := tmuxexec.RunCommand(ctx, nil, piRedactArgs, "tmux", "kill-session", "-t", session.tmuxSessionName); err != nil && !isPiTmuxSessionLostError(err) {
			errs = append(errs, err)
		}
		if session.tempDir != "" {
			_ = os.RemoveAll(session.tempDir)
		}
		if session.cleanupFiles != nil {
			session.cleanupFiles()
			session.cleanupFiles = nil
		}
		if session.releaseMCPLease != nil {
			session.releaseMCPLease()
			session.releaseMCPLease = nil
		}
	}
	return errors.Join(errs...)
}

// ClosePiCLIInteractiveSessionForOwner closes the persistent Pi session for
// the given owner, if one exists.
func ClosePiCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	piInteractiveRegistry.Lock()
	session := piInteractiveRegistry.sessions[ownerSessionID]
	delete(piInteractiveRegistry.sessions, ownerSessionID)
	piInteractiveRegistry.Unlock()
	cleanupPiInteractiveSession(session)
}

// ClosePiCLIInteractiveSessionByTmux closes a persistent Pi session by tmux
// session name.
func ClosePiCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if tmuxSessionName == "" {
		return
	}
	var session *piInteractiveSession
	piInteractiveRegistry.Lock()
	for owner, candidate := range piInteractiveRegistry.sessions {
		if candidate != nil && candidate.tmuxSessionName == tmuxSessionName {
			session = candidate
			delete(piInteractiveRegistry.sessions, owner)
			break
		}
	}
	piInteractiveRegistry.Unlock()
	if session != nil {
		cleanupPiInteractiveSession(session)
		return
	}
	_ = tmuxexec.RunCommand(context.Background(), nil, piRedactArgs, "tmux", "kill-session", "-t", tmuxSessionName)
}

func activePiInteractiveSession(ownerSessionID string) (*piInteractiveSession, bool) {
	piInteractiveRegistry.Lock()
	defer piInteractiveRegistry.Unlock()
	session := piInteractiveRegistry.sessions[strings.TrimSpace(ownerSessionID)]
	return session, session != nil && strings.TrimSpace(session.tmuxSessionName) != ""
}

// SendPiInteractiveInput sends user input to a live Pi interactive session.
func SendPiInteractiveInput(ctx context.Context, ownerSessionID, message string) error {
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Pi interactive session registered for owner session %s", ownerSessionID)
	}
	return sendPiInputToTmux(ctx, session.tmuxSessionName, message)
}

// GetStatusLine retrieves the latest Pi statusline snapshot for an active
// persistent session. The sessionID is the owning app session id, matching the
// live-input routing key.
func (p *PiCLIAdapter) GetStatusLine(ctx context.Context, sessionID string) (*llmtypes.StatusLine, error) {
	session, ok := activePiInteractiveSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("no active Pi interactive session registered for owner session %s", sessionID)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if strings.TrimSpace(session.tmuxSessionName) != "" && !piTmuxSessionExists(ctx, session.tmuxSessionName) {
		return nil, fmt.Errorf("Pi tmux session %q is no longer running", session.tmuxSessionName)
	}
	status := piStatusLine(session)
	if status == nil {
		return nil, fmt.Errorf("Pi statusline is unavailable")
	}
	return status, nil
}

func piTmuxSessionExists(ctx context.Context, sessionName string) bool {
	return tmuxexec.RunCommand(ctx, nil, nil, "tmux", "has-session", "-t", sessionName) == nil
}

func isPiTmuxSessionLostError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"can't find pane",
		"can't find session",
		"no server running on",
		"target pane not found",
		"tmux session",
		"failed to capture",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func runPiCommand(ctx context.Context, stdin *bytes.Buffer, name string, args ...string) error {
	if stdin != nil {
		return tmuxexec.RunCommand(ctx, stdin, piRedactArgs, name, args...)
	}
	return tmuxexec.RunCommand(ctx, nil, piRedactArgs, name, args...)
}

func piRedactArgs(args []string) string {
	redacted := make([]string, len(args))
	copy(redacted, args)
	for i, arg := range redacted {
		for _, key := range []string{
			"GEMINI_API_KEY=",
			"GOOGLE_API_KEY=",
			"OPENAI_API_KEY=",
			"ANTHROPIC_API_KEY=",
			"OPENROUTER_API_KEY=",
			"PI_API_KEY=",
			"DEEPSEEK_API_KEY=",
			"NVIDIA_API_KEY=",
			"MISTRAL_API_KEY=",
			"GROQ_API_KEY=",
			"CEREBRAS_API_KEY=",
			"XAI_API_KEY=",
			"ZAI_API_KEY=",
			"ZAI_CODING_CN_API_KEY=",
			"OPENCODE_API_KEY=",
			"FIREWORKS_API_KEY=",
			"TOGETHER_API_KEY=",
			"KIMI_API_KEY=",
			"MINIMAX_API_KEY=",
			"MINIMAX_CN_API_KEY=",
			"AI_GATEWAY_API_KEY=",
		} {
			if strings.HasPrefix(arg, key) {
				redacted[i] = key + "<redacted>"
			}
		}
	}
	return strings.Join(redacted, " ")
}

func resolvePiProviderModel(modelID, providerOverride string) (string, string) {
	provider := strings.TrimSpace(providerOverride)
	model := strings.TrimSpace(modelID)
	if model == "" || model == "pi-cli" {
		model = DefaultModelID
	}
	if slash := strings.Index(model, "/"); slash > 0 {
		if provider == "" {
			provider = strings.TrimSpace(model[:slash])
		}
		model = strings.TrimSpace(model[slash+1:])
	}
	if provider == "" {
		provider = defaultPiProvider
	}
	if model == "" {
		model = defaultPiModel
	}
	return provider, model
}

func estimatePiTmuxTokens(prompt, content string) (int, int) {
	estimate := func(s string) int {
		n := len(s)
		if n == 0 {
			return 0
		}
		return (n + 3) / 4
	}
	return estimate(prompt), estimate(content)
}

func intPtr(v int) *int {
	return &v
}

func piInteractiveSessionPrefix() string {
	prefix := strings.TrimSpace(os.Getenv(EnvPiInteractiveSessionPrefix))
	if prefix == "" {
		prefix = "mlp-pi-cli-int"
	}
	return sanitizePiTmuxSessionName(prefix)
}

func sanitizePiTmuxSessionName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "mlp-pi-cli-int"
	}
	return out
}

func piInteractiveIdleTimeout() time.Duration {
	if parsed, ok := piDurationFromEnv(EnvPiInteractiveIdleTimeoutSeconds); ok {
		return parsed
	}
	return defaultPiInteractiveIdleTimeout
}

func piPromptWait() time.Duration {
	return tmuxlaunch.PromptWait(EnvPiInteractivePromptWaitSeconds)
}

func piInteractiveRetention() time.Duration {
	if parsed, ok := piDurationFromEnv(EnvPiInteractiveRetentionSeconds); ok {
		return parsed
	}
	return defaultPiInteractiveRetention
}

func piDurationFromEnv(key string) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	seconds, err := time.ParseDuration(raw + "s")
	if err != nil || seconds <= 0 {
		return 0, false
	}
	return seconds, true
}

func generatePiNativeSessionID() string {
	return "mlp-pi-" + piRandomHex(8)
}

func isValidPiNativeSessionID(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	if !isPiSessionIDEdgeChar(sessionID[0]) || !isPiSessionIDEdgeChar(sessionID[len(sessionID)-1]) {
		return false
	}
	for i := 0; i < len(sessionID); i++ {
		ch := sessionID[i]
		if isPiSessionIDEdgeChar(ch) || ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func isPiSessionIDEdgeChar(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
}

func piRandomHex(n int) string {
	if n <= 0 {
		n = 4
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func piMarkerExtensionSource() string {
	return `import { appendFileSync } from "node:fs";

function emit(type: string, fields: Record<string, unknown> = {}) {
	const markerFile = process.env.MLP_PI_MARKER_FILE;
	if (!markerFile) return;
	appendFileSync(markerFile, JSON.stringify({ type, ts: Date.now(), ...fields }) + "\n");
}

function role(message: unknown): string | undefined {
	if (!message || typeof message !== "object") return undefined;
	const value = (message as { role?: unknown }).role;
	return typeof value === "string" ? value : undefined;
}

export default function mlpMarkerExtension(pi: any) {
	emit("extension_loaded");
	pi.on("session_start", async (event: any, ctx: any) => {
		emit("session_start", { reason: event?.reason, mode: ctx?.mode, cwd: ctx?.cwd });
	});
	pi.on("agent_start", async () => emit("agent_start"));
	pi.on("turn_start", async () => emit("turn_start"));
	pi.on("message_start", async (event: any) => emit("message_start", { role: role(event?.message) }));
	pi.on("message_update", async (event: any) => {
		const streamEvent = event?.assistantMessageEvent;
		emit("message_update", {
			updateType: streamEvent?.type,
			delta: typeof streamEvent?.delta === "string" ? streamEvent.delta : undefined
		});
	});
	pi.on("message_end", async (event: any) => emit("message_end", { role: role(event?.message) }));
	pi.on("tool_execution_start", async (event: any) => {
		emit("tool_execution_start", { toolCallId: event?.toolCallId, toolName: event?.toolName });
	});
	pi.on("tool_execution_update", async (event: any) => {
		emit("tool_execution_update", { toolCallId: event?.toolCallId, toolName: event?.toolName });
	});
	pi.on("tool_execution_end", async (event: any) => {
		emit("tool_execution_end", {
			toolCallId: event?.toolCallId,
			toolName: event?.toolName,
			isError: event?.isError
		});
	});
	pi.on("turn_end", async (event: any) => emit("turn_end", { role: role(event?.message) }));
	pi.on("agent_end", async () => emit("agent_end"));
}
`
}
