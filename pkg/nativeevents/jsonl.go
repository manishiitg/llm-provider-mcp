package nativeevents

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func decodeClaude(b *Batch, session string, row map[string]any) {
	if id := text(row, "sessionId", "session_id"); id != "" && id != session {
		b.Gaps++
		b.Next.Rejected = true
		return
	}
	if side, _ := row["isSidechain"].(bool); side {
		return
	}
	kind, id := text(row, "type"), text(row, "uuid")
	parent := text(row, "parentUuid")
	lineage := b.Next.Lineage[parent]
	r := llmtypes.NativeRecord{Provider: "claude-code", SessionID: session, ID: id, ParentID: parent, Timestamp: at(row), TurnID: lineage.TurnID, Origin: lineage.Origin, TaskID: lineage.TaskID}
	defer func() {
		if id != "" {
			b.Next.Lineage[id] = lineage
		}
	}()
	switch kind {
	case "user":
		origin := text(object(row["origin"]), "kind")
		if origin == "human" || text(row, "turnOrigin") == "sdk" {
			if id == "" {
				b.Gaps++
				return
			}
			b.Next.TurnID, b.Next.Origin, b.Next.TaskID = id, "human", ""
			lineage = Lineage{TurnID: id, Origin: "human"}
			r.TurnID, r.Origin, r.TaskID = id, "human", ""
			r.Kind, r.State = "turn", "started"
			emit(b, r)
		} else if origin == "task-notification" {
			notices := claudeTaskNotices(contentText(object(row["message"])["content"]))
			linked := false
			for _, notice := range notices {
				out := r
				out.ID, out.Kind, out.TaskID = id+":"+notice.TaskID, "helper", notice.TaskID
				out.ToolID = b.Next.Tasks[notice.TaskID]
				queued, _ := row["queueTranscriptOnly"].(bool)
				if !queued && text(row, "turnOrigin") == "task_notification" {
					out.FollowupID = id
				}
				if out.ToolID == "" && (b.Next.Tools[notice.ToolID] == "Agent" || b.Next.Tools[notice.ToolID] == "Task") {
					out.ToolID = notice.ToolID
					b.Next.Tasks[notice.TaskID] = notice.ToolID
				}
				if id == "" || notice.TaskID == "" || out.ToolID == "" {
					b.Gaps++
					continue
				}
				switch notice.Status {
				case "completed":
					out.State = "finished"
				case "failed":
					out.State = "failed"
				case "killed", "stopped":
					out.State = "stopped"
				default:
					b.Unknown["task-status:"+notice.Status]++
					continue
				}
				emit(b, out)
				linked = true
			}
			queued, _ := row["queueTranscriptOnly"].(bool)
			if linked && !queued && text(row, "turnOrigin") == "task_notification" {
				b.Next.TurnID, b.Next.Origin, b.Next.TaskID = id, "helper_finished", ""
				lineage = Lineage{TurnID: id, Origin: "helper_finished"}
				r.TurnID, r.Origin, r.TaskID = id, "helper_finished", ""
				r.ID, r.Kind, r.State = id+":turn", "turn", "started"
				emit(b, r)
			}
		}
		blocks, _ := object(row["message"])["content"].([]any)
		for _, v := range blocks {
			block := object(v)
			if text(block, "type") != "tool_result" {
				continue
			}
			tool := text(block, "tool_use_id")
			name := b.Next.Tools[tool]
			if name == "" {
				continue
			}
			out := r
			out.ID = id + ":" + tool
			out.Kind, out.State, out.ToolID, out.Name = "tool", "finished", tool, name
			if failed, _ := block["is_error"].(bool); failed {
				out.State = "failed"
			}
			if name == "Agent" || name == "Task" {
				// The CLI's structured launch result supplies the task identity.
				result := object(row["toolUseResult"])
				task := text(result, "agentId", "taskId", "task_id")
				if task != "" {
					b.Next.Tasks[task] = tool
					if asynchronous, _ := result["isAsync"].(bool); asynchronous {
						out.Kind, out.State, out.TaskID = "helper", "started", task
					}
				}
			}
			emit(b, out)
		}
	case "assistant":
		if r.TurnID == "" {
			b.Gaps++
			return
		}
		msg := object(row["message"])
		blocks, _ := msg["content"].([]any)
		for i, v := range blocks {
			block := object(v)
			out := r
			out.ID = id + ":" + strconv.Itoa(i)
			if id == "" {
				b.Gaps++
				continue
			}
			switch text(block, "type") {
			case "text":
				out.Kind, out.State, out.Text = "reply", "committed", text(block, "text")
				emit(b, out)
			case "tool_use":
				out.Kind, out.State, out.ToolID, out.Name = "tool", "started", text(block, "id"), text(block, "name")
				b.Next.Tools[out.ToolID] = out.Name
				if out.Name == "AskUserQuestion" {
					out.Kind, out.State = "question", "requested"
				}
				if out.Name == "Edit" || out.Name == "Write" {
					out.Kind = "file"
				}
				// Arguments, file content and tool results are intentionally absent.
				emit(b, out)
			}
		}
	case "system":
		if r.TurnID == "" {
			r.TurnID, r.Origin = b.Next.TurnID, b.Next.Origin
		}
		switch text(row, "subtype") {
		case "turn_duration":
			r.Kind, r.State, r.DurationMS = "turn", "observed_end", number(row, "durationMs")
			emit(b, r)
		case "agents_killed":
			// This record has no task identities. The following committed
			// notifications identify which helpers actually stopped.
			r.Kind, r.State = "notice", "helpers_stopped"
			emit(b, r)
		case "compact_boundary":
			r.Kind, r.State = "compaction", "finished"
			emit(b, r)
		case "init":
			r.Kind, r.State = "runtime", "started"
			emit(b, r)
		default:
			b.Unknown[kind+":"+text(row, "subtype")]++
		}
	case "cost-state":
		// Cost snapshots are scoped to the session and inclusive of child work.
		r.Kind, r.State = "usage", "snapshot"
		if r.ID == "" {
			r.ID = fmt.Sprintf("cost:%d", b.Next.Offset)
		}
		for model, v := range object(row["modelUsage"]) {
			m := object(v)
			out := r
			out.ID += ":" + model
			out.Model = model
			out.Usage = &llmtypes.NativeUsage{Scope: "session_tree", Cumulative: true, Input: number(m, "inputTokens"), Output: number(m, "outputTokens"), CacheRead: number(m, "cacheReadInputTokens"), CacheWrite: number(m, "cacheCreationInputTokens", "cacheWriteInputTokens")}
			emit(b, out)
		}
	case "queue-operation", "attachment", "progress", "file-history-snapshot", "api-request", "api-request-blob", "last-prompt", "custom-title", "agent-name", "mode", "permission-mode":
		// Context, reasoning and control bookkeeping are not chat or analytics.
	default:
		b.Unknown[kind]++
	}
}

func decodeCodex(b *Batch, session string, row map[string]any) {
	kind := text(row, "type")
	p := object(row["payload"])
	r := llmtypes.NativeRecord{Provider: "codex-cli", SessionID: session, Timestamp: at(row), TurnID: text(p, "turn_id")}
	if kind == "session_meta" {
		if id := text(p, "id"); id != "" && id != session {
			b.Gaps++
			b.Next.Rejected = true
		}
		return
	}
	if kind != "event_msg" {
		// item_completed is the committed source; response_item mirrors it.
		if kind != "response_item" && kind != "turn_context" && kind != "thread_settings_applied" && kind != "token_usage_record" && kind != "world_state" && kind != "compacted" {
			b.Unknown[kind]++
		}
		return
	}
	switch event := text(p, "type"); event {
	case "task_started":
		if r.TurnID == "" {
			b.Gaps++
			return
		}
		b.Next.TurnID, b.Next.Origin = r.TurnID, "human"
		r.ID, r.Kind, r.State = r.TurnID+":start", "turn", "started"
		emit(b, r)
	case "task_complete", "turn_aborted":
		if r.TurnID == "" {
			b.Gaps++
			return
		}
		r.ID, r.Kind, r.State = r.TurnID+":end", "turn", "completed"
		if event == "turn_aborted" {
			r.State = "interrupted"
		}
		if p["error"] != nil {
			r.State = "failed"
		}
		r.DurationMS = number(p, "duration_ms")
		emit(b, r)
	case "item_completed":
		item := object(p["item"])
		if item == nil {
			item = p
		}
		r.ID = text(item, "id", "item_id")
		r.State = "completed"
		switch strings.ToLower(text(item, "type")) {
		case "agentmessage", "agent_message":
			r.Kind, r.State, r.Text = "reply", "committed", text(item, "text", "message")
			if r.Text == "" {
				r.Text = contentText(item["content"])
			}
		case "commandexecution", "command_execution":
			r.Kind, r.Name = "tool", "Command"
			if number(item, "exit_code", "exitCode") != 0 {
				r.State = "failed"
			}
		case "mcptoolcall", "mcp_tool_call":
			r.Kind, r.Name = "tool", text(item, "tool", "tool_name")
		case "filechange", "file_change":
			r.Kind = "file"
		case "contextcompaction", "context_compaction":
			r.Kind = "compaction"
		case "reasoning", "usermessage", "user_message", "functioncalloutput", "extension", "imageview", "hookprompt":
			return
		default:
			b.Unknown["item:"+text(item, "type")]++
			return
		}
		emit(b, r)
	case "token_count":
		u := object(object(p["info"])["total_token_usage"])
		if u == nil {
			return
		}
		r.ID, r.Kind, r.State = fmt.Sprintf("usage:%d", b.Next.Offset), "usage", "snapshot"
		r.Usage = &llmtypes.NativeUsage{Scope: "session", Cumulative: true, Input: number(u, "input_tokens"), Output: number(u, "output_tokens"), CacheRead: number(u, "cached_input_tokens")}
		// Personal account limits, credits and plan fields are not projected.
		emit(b, r)
	case "request_user_input":
		r.ID = text(p, "id", "call_id")
		r.Kind, r.State = "question", "requested"
		emit(b, r)
	case "error":
		r.ID = text(p, "id")
		r.Kind, r.State = "error", "failed"
		emit(b, r)
	case "agent_message", "agent_reasoning", "agent_reasoning_raw_content", "agent_reasoning_section_break", "user_message", "exec_command_begin", "exec_command_end", "mcp_tool_call_begin", "mcp_tool_call_end":
		// Mirror/stream records are already represented by committed item IDs.
	default:
		b.Unknown["event:"+event]++
	}
}

func decodePi(b *Batch, session string, row map[string]any) {
	kind := text(row, "type")
	if kind == "session" {
		if id := text(row, "id"); id != "" && id != session {
			b.Gaps++
			b.Next.Rejected = true
		}
		return
	}
	r := llmtypes.NativeRecord{ID: text(row, "id"), Provider: "pi-cli", SessionID: session, Timestamp: at(row), ParentID: text(row, "parentId")}
	switch kind {
	case "message":
		m := object(row["message"])
		switch text(m, "role") {
		case "user":
			b.Next.TurnID, b.Next.Origin = r.ID, "human"
			return
		case "assistant":
			r.Kind, r.State, r.Text = "reply", "committed", contentText(m["content"])
			if r.Text != "" {
				emit(b, r)
			}
			blocks, _ := m["content"].([]any)
			for _, v := range blocks {
				tool := object(v)
				if text(tool, "type") != "toolCall" {
					continue
				}
				out := r
				out.ID = r.ID + ":" + text(tool, "id")
				out.Text = ""
				out.Kind, out.State, out.ToolID, out.Name = "tool", "started", text(tool, "id"), text(tool, "name")
				emit(b, out)
			}
			if u := object(m["usage"]); u != nil {
				out := r
				out.ID += ":usage"
				out.Text = ""
				out.Kind, out.State, out.Model = "usage", "snapshot", text(m, "model")
				out.Usage = &llmtypes.NativeUsage{Scope: "message", Input: number(u, "input"), Output: number(u, "output"), CacheRead: number(u, "cacheRead"), CacheWrite: number(u, "cacheWrite")}
				emit(b, out)
			}
		case "toolResult":
			r.Kind, r.State, r.ToolID, r.Name = "tool", "finished", text(m, "toolCallId"), text(m, "toolName")
			if failed, _ := m["isError"].(bool); failed {
				r.State = "failed"
			}
			emit(b, r)
		}
	case "compaction":
		r.Kind, r.State = "compaction", "finished"
		emit(b, r)
	case "model_change", "thinking_level_change", "custom", "branch_summary", "label":
		return
	default:
		b.Unknown[kind]++
	}
}

func decodeMuse(b *Batch, session string, row map[string]any) {
	frame := row
	if payload := object(row["payload"]); payload != nil {
		if event := object(payload["event"]); event != nil {
			row = event
			if text(row, "run_id") == "" {
				row["run_id"] = text(payload, "run_id")
			}
		}
	}
	// The journal wraps committed events; only unwrap explicit event containers.
	for i := 0; i < 4; i++ {
		nested := object(row["event"])
		if nested == nil {
			break
		}
		row = nested
	}
	kind := text(row, "kind", "type")
	p := object(row["payload"])
	if p == nil {
		p = row
	}
	r := llmtypes.NativeRecord{Provider: "muse-cli", SessionID: session, ID: text(row, "id", "transaction_id"), TurnID: text(p, "run_id", "turn_id"), TaskID: text(p, "task_id"), Timestamp: at(row)}
	if r.Timestamp.IsZero() {
		r.Timestamp = at(frame)
		if us := number(frame, "recorded_at"); us > 0 {
			r.Timestamp = time.UnixMicro(us)
		}
	}
	if r.ID == "" {
		r.ID = text(row, "message_id", "provider_item_id")
	}
	if r.ID == "" {
		r.ID = text(frame, "transaction_id")
	}
	if seq := number(frame, "sequence", "outer_log_ordinal"); r.ID == "" && seq > 0 {
		r.ID = fmt.Sprintf("seq:%d", seq)
	}
	switch kind {
	case "assistant_message_committed":
		r.Kind, r.State, r.Text = "reply", "committed", text(p, "text")
		if r.Text == "" {
			r.Text = contentText(p["content"])
		}
		emit(b, r)
	case "task_proposed", "task_scheduled", "task_started", "task_completed":
		r.Kind, r.State = "helper", strings.TrimPrefix(kind, "task_")
		emit(b, r)
	case "terminal":
		r.Kind, r.State = "turn", text(p, "status", "outcome")
		emit(b, r)
	case "status":
		r.Kind, r.State = "retry", text(p, "status")
		emit(b, r)
	case "model_completed":
		r.Kind, r.State, r.Model = "usage", "snapshot", text(p, "model", "model_id")
		r.Usage = &llmtypes.NativeUsage{Scope: "model_request", Input: number(p, "input_tokens"), Output: number(p, "output_tokens"), CacheRead: number(p, "cache_read_tokens")}
		emit(b, r)
	case "context_compaction":
		r.Kind, r.State = "compaction", text(p, "phase")
		emit(b, r)
	case "assistant_tool_calls_committed":
		calls, _ := p["tool_calls"].([]any)
		for _, v := range calls {
			call := object(v)
			out := r
			out.ID += ":" + text(call, "call_id")
			out.Kind, out.State, out.ToolID, out.Name = "tool", "started", text(call, "call_id"), text(call, "name")
			emit(b, out)
		}
	case "tool_result_batch_committed":
		results, _ := p["results"].([]any)
		for _, v := range results {
			result := object(v)
			out := r
			out.ID += ":" + text(result, "tool_call_id")
			out.Kind, out.State, out.ToolID = "tool", "finished", text(result, "tool_call_id")
			emit(b, out)
		}
	case "context_compaction_candidate", "context_compaction_installed":
		r.Kind, r.State = "compaction", strings.TrimPrefix(kind, "context_compaction_")
		emit(b, r)
	case "user_input_prompt_requested", "user_input_prompt_settled":
		r.Kind, r.State = "question", strings.TrimPrefix(kind, "user_input_prompt_")
		emit(b, r)
	case "command_intake", "received", "settled", "goal_usage_attribution", "resource_usage_sampled", "context_block_diagnostic", "memory_reminder_child_session_linked", "reminder_proposal", "reasoning_committed", "side_effect_intent", "task_stream_linked":
		return
	default:
		b.Unknown[kind]++
	}
}

type claudeTaskNotice struct {
	TaskID string `xml:"task-id"`
	ToolID string `xml:"tool-use-id"`
	Status string `xml:"status"`
}

// The stopped form can contain several sibling task-notification elements.
func claudeTaskNotices(value string) []claudeTaskNotice {
	d := xml.NewDecoder(strings.NewReader(value))
	var out []claudeTaskNotice
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "task-notification" {
			var n claudeTaskNotice
			if d.DecodeElement(&n, &start) == nil {
				out = append(out, n)
			}
		}
	}
	return out
}
