package agycli

import (
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// AGY 1.2.14 stores invoke_subagent's child references in the tool result's
// embedded gemini_coder.Step: 140.2.6.2.143.10.1. Invocation completion only
// means the children were launched; the parent composer can already be idle.
func agyNativeSubagentIDs(payload []byte) []string {
	section := payload
	for _, field := range []protowire.Number{140, 2, 6, 2, 143} {
		var ok bool
		section, ok = agyProtoSubmessage(section, field)
		if !ok {
			return nil
		}
	}
	var ids []string
	for len(section) > 0 {
		field, typ, n := protowire.ConsumeTag(section)
		if n < 0 {
			return nil
		}
		section = section[n:]
		size := protowire.ConsumeFieldValue(field, typ, section)
		if size < 0 {
			return nil
		}
		if field == 10 && typ == protowire.BytesType {
			child, _ := protowire.ConsumeBytes(section)
			if id, ok := agyProtoStringField(child, 1); ok && id != "" && !strings.ContainsAny(id, `/\`) {
				ids = append(ids, id)
			}
		}
		section = section[size:]
	}
	return ids
}

// Wait for both a finished child trail and its message to reach the parent.
// That message must precede the parent's settled final assistant row. Missing
// or interrupted child records never turn an interim answer into completion.
func agyPendingNativeSubagents(record agyTurnRecord, depth int) bool {
	if depth >= 16 && len(record.subagents) > 0 {
		return true
	}
	for id, notified := range record.subagents {
		if !notified {
			return true
		}
		child, err := agyReadTurnRecord(id, -1, "", record.transcriptHome)
		if err != nil || child.lastType != agyStepAssistant || child.lastStatus != 3 || child.finalAnswer == "" || agyPendingNativeSubagents(child, depth+1) {
			return true
		}
	}
	return false
}
