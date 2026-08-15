package codexcli

import (
	"strings"
	"time"
)

// Rollout binding pins an interactive Codex session to the exact rollout file
// that records ITS conversation.
//
// Why this exists (PLAT-106): retained-answer lookup previously resolved a
// transcript by working directory + newest modification time. A workflow's
// interactive Chat and its scheduled run execute in the SAME directory, so the
// newest rollout in that directory frequently belongs to the other session. The
// wrong final answer was then returned and stamped with the requesting
// session's IDs, which made it undetectable downstream — no frontend guard can
// recognise a foreign event that already carries the local session's identity.
//
// Binding resolves in order:
//  1. a thread ID already pinned to this session — exact, and the steady state;
//  2. otherwise a directory/recency match that EXCLUDES rollouts already bound
//     to other live sessions, which is what stops two sessions in one directory
//     from claiming the same transcript.
//
// Once a rollout is resolved its thread ID is recorded, so every later lookup
// takes branch 1.

// boundCodexRolloutPaths returns rollout paths already claimed by sessions
// other than the caller, so a directory match cannot steal another
// conversation's transcript.
func boundCodexRolloutPaths(exclude *codexInteractiveSession) map[string]bool {
	claimed := make(map[string]bool)
	for _, session := range codexPersistentRegistry.Snapshot() {
		if session == nil || session == exclude {
			continue
		}
		session.mu.Lock()
		path := session.rolloutPath
		session.mu.Unlock()
		if strings.TrimSpace(path) != "" {
			claimed[path] = true
		}
	}
	return claimed
}

// resolveCodexRolloutPath returns the rollout file for this session, binding it
// on first use. The caller must NOT hold session.mu.
func resolveCodexRolloutPath(session *codexInteractiveSession, turnStart time.Time) string {
	if session == nil {
		return ""
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return resolveCodexRolloutPathLocked(session, turnStart)
}

// resolveCodexRolloutPathLocked is the same resolution with the caller already
// holding session.mu.
//
// This variant exists because the interactive adapter holds session.mu for the
// entire GenerateContent body — it is acquired when the session is claimed and
// released by releaseCodexInteractiveSession in a defer. Go mutexes are not
// reentrant, so calling the locking variant from inside a turn deadlocks the
// live path. Unit tests never reach it (they do not drive a real tmux Codex
// session), so the compiler and the suite are both silent about it.
func resolveCodexRolloutPathLocked(session *codexInteractiveSession, turnStart time.Time) string {
	if session == nil {
		return ""
	}

	// A pinned thread ID is authoritative. Re-resolving from it (rather than
	// trusting a cached path) survives Codex rotating or compacting the file.
	if session.threadID != "" {
		if path := findCodexRolloutForThread(session.threadID); path != "" {
			session.rolloutPath = path
			return path
		}
		// The thread is known but its file is momentarily unreadable. Returning
		// the last known path is still correct for THIS conversation; falling
		// back to a directory scan would risk another session's transcript.
		return session.rolloutPath
	}

	if session.rolloutPath != "" {
		return session.rolloutPath
	}

	// boundCodexRolloutPaths locks OTHER sessions' mutexes and skips this one,
	// so it is safe to call while holding ours.
	path := findCodexRolloutByWorkingDirExcluding(turnStart, session.workingDir, boundCodexRolloutPaths(session))
	if path == "" {
		return ""
	}

	session.rolloutPath = path
	if threadID := readCodexRolloutThreadID(path); threadID != "" {
		session.threadID = threadID
	}
	return path
}

// codexRolloutResolverForSession snapshots this session's identity and returns
// a lock-free resolver usable from inside the turn loop.
//
// The completion tracker and transcript stream poll while session.mu is held by
// GenerateContent, so they cannot take the lock themselves. Snapshotting the
// thread ID (and the set of rollouts other sessions have claimed) gives them an
// exact, allocation-free lookup with no locking at all. The caller must hold
// session.mu when calling this.
func codexRolloutResolverForSession(session *codexInteractiveSession) func(time.Time) string {
	if session == nil {
		return nil
	}
	threadID := session.threadID
	knownPath := session.rolloutPath
	workingDir := session.workingDir
	claimed := boundCodexRolloutPaths(session)

	return func(turnStart time.Time) string {
		if threadID != "" {
			if path := findCodexRolloutForThread(threadID); path != "" {
				return path
			}
			return knownPath
		}
		if knownPath != "" {
			return knownPath
		}
		return findCodexRolloutByWorkingDirExcluding(turnStart, workingDir, claimed)
	}
}
