package moirai

import (
	"encoding/json"
	"fmt"
)

type ClaudeCodeCodec struct{}

func (ClaudeCodeCodec) Format() Format { return FormatClaudeCode }
func (ClaudeCodeCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatClaudeCode, DisplayName: "Claude Code", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

func (ClaudeCodeCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	records, warnings, err := decodeJSONLines(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	t := &Transcript{SchemaVersion: SchemaVersion}
	var pending []string
	omittedKinds := map[string]bool{}
	omittedFields := false
	for _, record := range records {
		kind := stringValue(record["type"])
		if kind == "summary" {
			t.Meta.Title = firstNonEmpty(t.Meta.Title, stringValue(record["summary"]))
			continue
		}
		if kind != "user" && kind != "assistant" {
			if kind != "" {
				omittedKinds[kind] = true
			}
			continue
		}
		if boolValue(record["isSidechain"]) {
			omittedKinds["sidechain"] = true
			continue
		}
		for _, field := range []string{"isMeta", "userType", "requestId", "agentId", "toolUseResult"} {
			if record[field] != nil {
				omittedFields = true
			}
		}
		t.Meta.ID = firstNonEmpty(t.Meta.ID, stringValue(record["sessionId"]))
		t.Meta.CWD = firstNonEmpty(t.Meta.CWD, stringValue(record["cwd"]))
		t.Meta.GitBranch = firstNonEmpty(t.Meta.GitBranch, stringValue(record["gitBranch"]))
		t.Meta.CLIVersion = firstNonEmpty(t.Meta.CLIVersion, stringValue(record["version"]))
		stamp := timestampValue(record["timestamp"])
		t.Meta.Timestamp = firstNonEmpty(t.Meta.Timestamp, stamp)
		payload := object(record["message"])
		blocks, blockWarnings := parseAnthropicContent(payload["content"], len(t.Messages), &pending)
		warnings = append(warnings, blockWarnings...)
		if len(blocks) == 0 {
			continue
		}
		role := Role(kind)
		if payloadRole := Role(stringValue(payload["role"])); payloadRole == RoleUser || payloadRole == RoleAssistant {
			role = payloadRole
		}
		message := Message{ID: stringValue(record["uuid"]), Role: role, Content: blocks, Timestamp: stamp, Model: stringValue(payload["model"]), StopReason: stringValue(payload["stop_reason"]), Usage: usageFromMap(payload["usage"], []string{"input_tokens"}, []string{"output_tokens"})}
		t.Messages = append(t.Messages, message)
		t.Meta.Model = firstNonEmpty(t.Meta.Model, message.Model)
	}
	for kind := range omittedKinds {
		warnings = append(warnings, Warning{Code: "native_record_omitted", Message: fmt.Sprintf("Claude Code %s record omitted from portable context", kind)})
	}
	if omittedFields {
		warnings = append(warnings, Warning{Code: "native_fields_omitted", Message: "Claude Code bookkeeping fields omitted from portable context"})
	}
	if len(t.Messages) == 0 {
		return nil, fmt.Errorf("%w: no conversational records", ErrInvalidTranscript)
	}
	if err := normalizeTranscript(t, opts.SourceID, ""); err != nil {
		return nil, err
	}
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	return &ParseResult{Transcript: t, Warnings: warnings}, nil
}

func (ClaudeCodeCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	sessionID := firstNonEmpty(opts.ID, t.Meta.ID)
	var records []any
	parent := ""
	last := ""
	for i, message := range t.Messages {
		id := firstNonEmpty(message.ID, uuidFromSeed(sessionID, fmt.Sprint(i), string(message.Role)))
		payload := map[string]any{"role": message.Role, "content": renderClaudeContent(message.Content)}
		if message.Role == RoleAssistant {
			payload["id"] = "msg_" + stableID("", sessionID, fmt.Sprint(i))
			payload["type"] = "message"
			payload["model"] = firstNonEmpty(message.Model, t.Meta.Model, "unknown")
			stopReason := message.StopReason
			if stopReason == "" && hasBlockType(message, BlockToolUse) {
				stopReason = "tool_use"
			}
			payload["stop_reason"] = firstNonEmpty(stopReason, "end_turn")
			if message.Usage != nil {
				payload["usage"] = message.Usage
			}
		}
		record := map[string]any{"type": message.Role, "uuid": id, "parentUuid": nil, "sessionId": sessionID, "timestamp": firstNonEmpty(message.Timestamp, t.Meta.Timestamp), "cwd": t.Meta.CWD, "gitBranch": t.Meta.GitBranch, "version": t.Meta.CLIVersion, "message": payload}
		if parent != "" {
			record["parentUuid"] = parent
		}
		records = append(records, record)
		parent, last = id, id
	}
	if t.Meta.Title != "" {
		records = append(records, map[string]any{"type": "summary", "summary": t.Meta.Title, "leafUuid": last})
	}
	data, err := encodeJSONLines(records)
	return finalizeRender(t, FormatClaudeCode, &RenderResult{Data: data}, err)
}

func renderClaudeContent(blocks []Block) []any {
	result := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			result = append(result, map[string]any{"type": "text", "text": block.Text})
		case BlockThinking:
			switch {
			case block.Signature != "":
				result = append(result, map[string]any{"type": "thinking", "thinking": block.Text, "signature": block.Signature})
			case block.Encrypted != "":
				result = append(result, map[string]any{"type": "redacted_thinking", "data": block.Encrypted})
			case block.Text != "":
				result = append(result, map[string]any{"type": "text", "text": "[Reasoning]\n" + block.Text})
			}
		case BlockToolUse:
			var input any
			_ = json.Unmarshal(block.Input, &input)
			result = append(result, map[string]any{"type": "tool_use", "id": block.ID, "name": block.Name, "input": input})
		case BlockToolResult:
			result = append(result, map[string]any{"type": "tool_result", "tool_use_id": block.ToolUseID, "content": claudeToolResultContent(block.Content), "is_error": block.IsError})
		case BlockImage:
			if block.Source != nil {
				result = append(result, map[string]any{"type": "image", "source": block.Source})
			}
		}
	}
	return result
}

func claudeToolResultContent(raw json.RawMessage) any {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	if text, ok := value.(string); ok {
		return text
	}
	if entries, ok := value.([]any); ok {
		valid := true
		for _, entry := range entries {
			kind := stringValue(object(entry)["type"])
			if kind != "text" && kind != "image" {
				valid = false
				break
			}
		}
		if valid {
			return entries
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func init() { _ = Register(ClaudeCodeCodec{}) }
