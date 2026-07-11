# Changelog

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
