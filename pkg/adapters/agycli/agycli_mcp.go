package agycli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
)

// agyMCPServer is one parsed stdio mcpServers entry.
type agyMCPServer struct {
	name    string
	command string
	args    []string
	env     map[string]string
}

// agyMCPMountMu guards the mount manager state below and serializes
// settings.json read-modify-write cycles. It is never held across model
// turns; agyHoldMounts/agyReleaseMounts own mount lifecycles instead.
var agyMCPMountMu sync.Mutex

var (
	agyMountActiveFingerprint = ""
	agyMountActiveNames       []string
	agyMountHolders           = 0
)

// agyHoldMounts shares one fingerprint's global mounts across holders.
// Mounts land in global user config with no per-run scope, so turns and
// sessions with the SAME tool surface run concurrently on one shared mount
// set, while a DIFFERENT surface waits (ctx-bounded) for release. The
// first holder mounts and installs mcp(<mount>/*) permission entries; the
// last release unmounts and removes them. Unmounted callers must not call
// this; fingerprint "unmounted" carries no mounts.
func agyHoldMounts(ctx context.Context, fingerprint string, servers []agyMCPServer) (names []string, release func(), err error) {
	for {
		agyMCPMountMu.Lock()
		switch {
		case agyMountActiveFingerprint == "":
			agyMountActiveFingerprint = fingerprint
			agyMountHolders = 1
			agyMCPMountMu.Unlock()
			names, err := agyMountMCPServers(ctx, servers)
			agyMCPMountMu.Lock()
			if err != nil {
				agyMountActiveFingerprint = ""
				agyMountHolders = 0
				agyMCPMountMu.Unlock()
				return nil, nil, err
			}
			if err := agyAllowMountedTools(names); err != nil {
				agyMountActiveFingerprint = ""
				agyMountHolders = 0
				agyMCPMountMu.Unlock()
				agyUnmountMCPServers(ctx, names, nil)
				return nil, nil, err
			}
			agyMountActiveNames = names
			agyMCPMountMu.Unlock()
			return names, func() { agyReleaseMounts(fingerprint) }, nil
		case agyMountActiveFingerprint == fingerprint && agyMountActiveNames != nil:
			agyMountHolders++
			names := agyMountActiveNames
			agyMCPMountMu.Unlock()
			return names, func() { agyReleaseMounts(fingerprint) }, nil
		default:
			// A different surface is mounted, or a mount is in
			// progress: wait for release.
			agyMCPMountMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

// agyReleaseMounts drops one hold; the last holder unmounts and removes
// the shared permission entries. Safe to call with a stale fingerprint.
func agyReleaseMounts(fingerprint string) {
	agyMCPMountMu.Lock()
	defer agyMCPMountMu.Unlock()
	if agyMountActiveFingerprint != fingerprint || agyMountHolders == 0 {
		return
	}
	agyMountHolders--
	if agyMountHolders > 0 {
		return
	}
	names := agyMountActiveNames
	agyMountActiveFingerprint = ""
	agyMountActiveNames = nil
	_ = agyRemoveAllowedTools(names)
	agyUnmountMCPServers(context.Background(), names, nil)
}

// agyParseMCPServers decodes the mcpServers document. v1 supports stdio
// entries (command/args/env) only; http/url entries fail loudly —
// `agy mcp add` takes them positionally, but no caller needs them yet.
func agyParseMCPServers(configJSON string) ([]agyMCPServer, error) {
	var doc struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(configJSON), &doc); err != nil {
		return nil, fmt.Errorf("agy MCP config not JSON: %w", err)
	}
	if len(doc.MCPServers) == 0 {
		return nil, fmt.Errorf("agy MCP config has no mcpServers")
	}
	var names []string
	for name := range doc.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	servers := make([]agyMCPServer, 0, len(names))
	for _, name := range names {
		entry := doc.MCPServers[name]
		if strings.TrimSpace(entry.URL) != "" || strings.EqualFold(strings.TrimSpace(entry.Type), "http") {
			return nil, fmt.Errorf("agy MCP server %q is http/url: v1 mounts stdio entries only", name)
		}
		if strings.TrimSpace(entry.Command) == "" {
			return nil, fmt.Errorf("agy MCP server %q has no command", name)
		}
		servers = append(servers, agyMCPServer{name: name, command: entry.Command, args: entry.Args, env: entry.Env})
	}
	return servers, nil
}

func agySanitizeServerName(base string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(base)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 24 {
		name = name[:24]
	}
	if name == "" {
		name = "server"
	}
	return name
}

func agyMountRandomHex() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// agyMountMCPServers adds each server under a unique agentworks- name and
// returns the mounted names for removal. A mid-mount failure rolls back the
// servers already added.
func agyMountMCPServers(ctx context.Context, servers []agyMCPServer) ([]string, error) {
	var mounted []string
	for _, srv := range servers {
		name := fmt.Sprintf("agentworks-%s-%d-%s", agySanitizeServerName(srv.name), os.Getpid(), agyMountRandomHex())
		args := []string{"mcp", "add"}
		var keys []string
		for k := range srv.env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "-e", k+"="+srv.env[k])
		}
		args = append(args, name, srv.command)
		args = append(args, srv.args...)
		if out, err := exec.CommandContext(ctx, "agy", args...).CombinedOutput(); err != nil {
			agyUnmountMCPServers(ctx, mounted, nil)
			return nil, fmt.Errorf("agy mcp add %s: %w\n%s", srv.name, err, out)
		}
		mounted = append(mounted, name)
	}
	return mounted, nil
}

// agyUnmountMCPServers removes mounted names best-effort; failures go to
// the logger (when non-nil) since cleanup must not fail the turn.
func agyUnmountMCPServers(ctx context.Context, names []string, logger interfaces.Logger) {
	for _, name := range names {
		if out, err := exec.CommandContext(ctx, "agy", "mcp", "remove", name).CombinedOutput(); err != nil {
			if logger != nil {
				logger.Errorf("agy mcp remove %s: %v\n%s", name, err, out)
			}
		}
	}
}
