package sessiontransfer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// ForkJSONL copies a validated native conversation into independent identities.
// Only native routing fields change; user text and message/tool ancestry remain
// intact. The source bundle and the provider's original store are never written.
func ForkJSONL(source *Bundle, out, workspace string) (*Bundle, error) {
	if err := source.ValidateFiles(); err != nil {
		return nil, err
	}
	if err := source.ValidateJournals(); err != nil {
		return nil, err
	}
	if source.Format != "claude-jsonl" && source.Format != "codex-rollout" {
		return nil, fmt.Errorf("this native format cannot be forked")
	}
	if !filepath.IsAbs(workspace) {
		return nil, fmt.Errorf("fork needs an absolute workspace")
	}
	if filepath.Clean(out) == filepath.Clean(source.Root) {
		return nil, fmt.Errorf("fork needs a separate destination")
	}
	ids := map[string]string{source.SessionID: uuid.NewString()}
	for name := range source.Files {
		if strings.HasPrefix(name, "children/") {
			ids[strings.TrimSuffix(filepath.Base(name), ".jsonl")] = uuid.NewString()
		}
	}
	fork := *source
	fork.Root, fork.SessionID, fork.Workspace, fork.Files = out, ids[source.SessionID], workspace, map[string]File{}
	for name := range source.Files {
		destName := name
		if strings.HasPrefix(name, "children/") {
			destName = "children/" + ids[strings.TrimSuffix(filepath.Base(name), ".jsonl")] + ".jsonl"
		}
		input, err := Path(source.Root, name, false)
		if err != nil {
			return nil, err
		}
		output, err := Path(out, destName, true)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(name, ".jsonl") {
			if err = forkJournal(input, output, source.Provider, workspace, ids); err != nil {
				return nil, err
			}
		} else if err = Copy(out, output, input); err != nil {
			return nil, err
		}
		info, err := Inspect(output)
		if err != nil {
			return nil, err
		}
		fork.Files[destName] = info
	}
	if err := fork.ValidateFiles(); err != nil {
		return nil, err
	}
	if err := fork.ValidateJournals(); err != nil {
		return nil, err
	}
	return &fork, nil
}

func forkJournal(input, output, provider, workspace string, ids map[string]string) error {
	in, err := os.Open(input)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	reader, writer := bufio.NewReader(in), bufio.NewWriter(out)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil {
			return err
		}
		var row map[string]json.RawMessage
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		if provider == "claude-code" {
			replaceNativeID(row, "sessionId", ids)
			if _, ok := row["cwd"]; ok {
				row["cwd"], _ = json.Marshal(workspace)
			}
		} else {
			var kind string
			_ = json.Unmarshal(row["type"], &kind)
			if kind == "session_meta" {
				var meta map[string]json.RawMessage
				if err := json.Unmarshal(row["payload"], &meta); err != nil {
					return err
				}
				replaceNativeID(meta, "id", ids)
				meta["cwd"], _ = json.Marshal(workspace)
				// Child sessions must point to the copied parent, never the original.
				if raw := meta["source"]; len(raw) > 0 {
					var source map[string]json.RawMessage
					if json.Unmarshal(raw, &source) == nil {
						var sub, spawn map[string]json.RawMessage
						if json.Unmarshal(source["subagent"], &sub) == nil && json.Unmarshal(sub["thread_spawn"], &spawn) == nil {
							replaceNativeID(spawn, "parent_thread_id", ids)
							sub["thread_spawn"], _ = json.Marshal(spawn)
							source["subagent"], _ = json.Marshal(sub)
							meta["source"], _ = json.Marshal(source)
						}
					}
				}
				row["payload"], _ = json.Marshal(meta)
			}
		}
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err = writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	return out.Close()
}

func replaceNativeID(fields map[string]json.RawMessage, key string, ids map[string]string) {
	var value string
	if json.Unmarshal(fields[key], &value) == nil && ids[value] != "" {
		fields[key], _ = json.Marshal(ids[value])
	}
}
