# Changelog

## Unreleased

### Added

- Added a real Claude Code to Codex CLI GPT-5.6 Sol delegation demo, sanitized
  transcript, GIF, and MP4.

### Documentation

- Documented the Codex 0.145 rollout requirement for GPT-5.6 Sol, Terra, and
  Luna, including multiple-installation troubleshooting.

### Fixed

- Fixed macOS release installs being terminated at launch because Darwin
  binaries produced on Linux had invalid ad-hoc code signatures.

### Breaking changes

- Removed the deprecated Gemini CLI provider, adapter package, public options,
  test commands, and compatibility contract. Use Pi CLI for Gemini models or
  Vertex for direct Gemini API access.

## 0.6.1 - 2026-07-11

### Security

- Updated the Go release toolchain floor to 1.25.12.
- Patched advisories in `golang.org/x/crypto`, `golang.org/x/net`,
  OpenTelemetry, and gRPC.
- Added reachable-code vulnerability scanning to CI.
- Replaced the Gitleaks action wrapper with a pinned CLI invocation that uses
  the repository configuration and produces SARIF output.

### Changed

- Updated GitHub Actions to current Node 24-based major versions.

### Fixed

- Replaced a fixed Pi/tmux test startup delay with a bounded readiness check.

## 0.6.0 - 2026-07-11

### Added

- Added the `llm-provider-mcp` launch documentation, real Claude-to-Cursor demo,
  social preview, security model, privacy statement, contribution guidance,
  roadmap, issue forms, and channel-specific launch kit.
- Added downstream compatibility compilation for MCP Agent and MCP Agent
  Builder.
- Added platform-specific MCP bundles and official MCP Registry publication to
  the release workflow.

### Changed

- Repositioned the repository around asynchronous coding-agent delegation.
- Renamed the GitHub product repository to `llm-provider-mcp` while retaining
  the existing Go module path for downstream compatibility.
- Split the historical provider implementation into registry, media,
  initialization, catalog, runtime, options, and management files without
  changing the package API.
- Renamed Z.AI CLI test-command source files so they are not mistaken for Go
  `_test.go` files.
- Limited the standalone scheduled security scan to schedule/manual triggers;
  push and pull-request scans remain in the main CI workflow.

### Removed

- Removed confirmed unreachable private/internal helpers and an unused Z.AI
  image fixture.

### Breaking changes

- Removed the deprecated `vertex.VertexImagenAdapter` and
  `vertex.NewVertexImagenAdapter`. Use `vertex.GeminiImageAdapter` with
  `vertex.NewGeminiImageAdapter` for Google image generation.
