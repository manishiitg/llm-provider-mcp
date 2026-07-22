#!/usr/bin/env bash
#
# Agentic P0 suite for coding-agent STREAMING OUTPUT (Claude Code, Codex).
#
# Why this is agentic, not just deterministic: coding CLIs change behavior with
# every release, and deterministic assertions can pass on visibly-degraded output
# (a Codex build once streamed every assistant line TWICE yet satisfied every
# assert). So this suite also records the REAL streamed output and REQUIRES an
# agent to look at it and sign off.
#
#   capture : run the live tests, record the real output, and RESET every review
#             to pending (verdict ""). A fresh run always starts unreviewed.
#   verify  : cheap gate (no live CLI) — fails until EVERY record is agent-approved.
#   (none)  : capture then verify.
#
# Between capture and verify, the agent running the tests MUST open each
#   pkg/adapters/*/testdata/agent-reviews/*.json
# read `output` (incl. `content_chunks`) against `review_criteria`
# (no duplication, proper formatting, human-readable, correct text<->tool
# interleaving, real work done), and set:
#   agent_review.verdict              = "good"   (or "bad" with issues)
#   agent_review.reviewed_fingerprint = <the record's "fingerprint">
#   agent_review.reviewer             = "<who>"
#
# Requires: a real authenticated `claude` and `codex` CLI, `tmux`, `node`.
set -euo pipefail
cd "$(dirname "$0")/.."

# Every coding-agent adapter whose contract sets SupportsStructuredStreaming must
# have its live streaming P0 tests captured + agent-approved here. Add a provider
# to STREAM_PKGS the moment it streams structured chunks + has a streaming E2E
# (and its contract flag flips on) — see coding_agent_certification.go
# CertStructuredStreaming. claude/codex/cursor stream by tailing the CLI
# transcript; pi streams via its injected marker hook. Only agy (deprecated) is
# absent.
STREAM_PKGS="./pkg/adapters/claudecode/ ./pkg/adapters/codexcli/ ./pkg/adapters/cursorcli/ ./pkg/adapters/picli/"
PKGS="$STREAM_PKGS"
# Matches every provider's streaming test: the transcript adapters'
# Transcript…BridgeLive/RealWorldLive/DisabledControl, and pi's Structured…RealWorldLive.
LIVE='(Transcript|Structured)Streaming'

capture() {
  echo ">> capture: running live agentic P0 tests; resetting reviews to pending ..."
  MLP_AGENT_REVIEW_CAPTURE=1 go test $PKGS -run "$LIVE" -coding-cli-p0-live -count=1 -timeout 1200s
  echo ">> capture done. Records under pkg/adapters/*/testdata/agent-reviews/ now have verdict=\"\" (pending)."
  echo ">> An agent must review each record and set agent_review.verdict=\"good\", then run: $0 verify"
}

verify() {
  echo ">> verify: enforcing agent sign-off (cheap gate, no live CLI) ..."
  go test $PKGS -run TestAgentReviewsApproved -count=1
  echo ">> verify passed: every recorded output is agent-approved for its current fingerprint."
}

case "${1:-all}" in
  capture) capture ;;
  verify)  verify ;;
  all)     capture; verify ;;
  *) echo "usage: $0 [capture|verify|all]"; exit 2 ;;
esac
