package moirai

import (
	"encoding/json"
	"fmt"
	"strings"
)

type CodexCodec struct{}

func (CodexCodec) Format() Format { return FormatCodex }
func (CodexCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatCodex, DisplayName: "Codex", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

func (CodexCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	records, warnings, err := decodeJSONLines(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	t := &Transcript{SchemaVersion: SchemaVersion}
	var pending []string
	var fallback []Message
	responseItems := 0
	currentModel := ""
	omittedKinds := map[string]bool{}
	for _, record := range records {
		kind := stringValue(record["type"])
		payload := object(record["payload"])
		stamp := timestampValue(record["timestamp"])
		switch kind {
		case "session_meta":
			t.Meta.ID = firstNonEmpty(t.Meta.ID, stringValue(payload["id"]))
			t.Meta.Timestamp = firstNonEmpty(t.Meta.Timestamp, timestampValue(payload["timestamp"]), stamp)
			t.Meta.CWD = firstNonEmpty(t.Meta.CWD, stringValue(payload["cwd"]))
			t.Meta.CLIVersion = firstNonEmpty(t.Meta.CLIVersion, stringValue(payload["cli_version"]))
			t.Meta.Model = firstNonEmpty(t.Meta.Model, stringValue(payload["model"]))
			t.Meta.ModelProvider = firstNonEmpty(t.Meta.ModelProvider, stringValue(payload["model_provider"]))
			t.Meta.GitBranch = firstNonEmpty(t.Meta.GitBranch, stringValue(object(payload["git"])["branch"]))
		case "turn_context":
			currentModel = firstNonEmpty(stringValue(payload["model"]), currentModel)
			t.Meta.Model = firstNonEmpty(t.Meta.Model, currentModel)
		case "response_item":
			message, ok, itemWarnings := parseCodexResponse(payload, stamp, currentModel, len(t.Messages), &pending)
			warnings = append(warnings, itemWarnings...)
			if ok {
				t.Messages = append(t.Messages, message)
				responseItems++
			}
		case "event_msg":
			message, ok := parseCodexEvent(payload, stamp, currentModel)
			if ok {
				fallback = append(fallback, message)
			}
		case "token_count", "compacted":
			omittedKinds[kind] = true
		default:
			if kind != "" {
				omittedKinds[kind] = true
			}
		}
	}
	if responseItems == 0 {
		t.Messages = fallback
	} else if len(fallback) > 0 {
		omittedKinds["event_msg"] = true
	}
	for kind := range omittedKinds {
		warnings = append(warnings, Warning{Code: "native_record_omitted", Message: fmt.Sprintf("Codex %s record omitted from portable context", kind)})
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

func parseCodexResponse(payload map[string]any, stamp, model string, index int, pending *[]string) (Message, bool, []Warning) {
	kind := stringValue(payload["type"])
	switch kind {
	case "message":
		role := Role(stringValue(payload["role"]))
		if role != RoleUser && role != RoleAssistant {
			return Message{}, false, nil
		}
		blocks, warnings := parseAnthropicContent(payload["content"], index, pending)
		if role == RoleUser && codexSetupBlocks(blocks) {
			return Message{}, false, []Warning{{Path: fmt.Sprintf("response_item[%d]", index), Code: "setup_prelude_omitted", Message: "Codex setup prelude omitted from portable context"}}
		}
		return Message{ID: stringValue(payload["id"]), Role: role, Content: blocks, Timestamp: stamp, Model: model}, len(blocks) > 0, warnings
	case "reasoning":
		var textParts []string
		for _, part := range array(payload["summary"]) {
			entry := object(part)
			if text := firstNonEmpty(stringValue(entry["text"]), stringValue(entry["summary_text"])); text != "" {
				textParts = append(textParts, text)
			}
		}
		if text := stringValue(payload["text"]); text != "" {
			textParts = append(textParts, text)
		}
		encrypted := stringValue(payload["encrypted_content"])
		if len(textParts) == 0 && encrypted == "" {
			return Message{}, false, nil
		}
		return Message{ID: stringValue(payload["id"]), Role: RoleAssistant, Content: []Block{{Type: BlockThinking, Text: strings.Join(textParts, "\n"), Encrypted: encrypted}}, Timestamp: stamp, Model: model}, true, nil
	case "function_call", "custom_tool_call":
		id := firstNonEmpty(stringValue(payload["call_id"]), stringValue(payload["id"]), syntheticToolID(index, 0))
		arguments := payload["arguments"]
		if kind == "custom_tool_call" {
			arguments = payload["input"]
		}
		if text, ok := arguments.(string); ok && json.Valid([]byte(text)) {
			var decoded any
			_ = json.Unmarshal([]byte(text), &decoded)
			arguments = decoded
		}
		*pending = append(*pending, id)
		return Message{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: id, Name: stringValue(payload["name"]), Input: rawJSON(arguments)}}, Timestamp: stamp, Model: model}, true, nil
	case "function_call_output", "custom_tool_call_output":
		id := stringValue(payload["call_id"])
		if id == "" && len(*pending) > 0 {
			id, *pending = (*pending)[0], (*pending)[1:]
		}
		if id == "" {
			return Message{}, false, []Warning{{Path: fmt.Sprintf("response_item[%d]", index), Code: "unpaired_tool_result", Message: "result omitted"}}
		}
		return Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(payload["output"])}}, Timestamp: stamp}, true, nil
	}
	return Message{}, false, nil
}

func parseCodexEvent(payload map[string]any, stamp, model string) (Message, bool) {
	var role Role
	var text string
	switch stringValue(payload["type"]) {
	case "user_message":
		role, text = RoleUser, firstNonEmpty(stringValue(payload["message"]), stringValue(payload["text"]))
	case "agent_message":
		role, text = RoleAssistant, firstNonEmpty(stringValue(payload["message"]), stringValue(payload["text"]))
	case "agent_reasoning":
		role, text = RoleAssistant, firstNonEmpty(stringValue(payload["text"]), stringValue(payload["message"]))
		if text != "" {
			return Message{Role: role, Content: []Block{{Type: BlockThinking, Text: text}}, Timestamp: stamp, Model: model}, true
		}
	}
	if text == "" {
		return Message{}, false
	}
	return Message{Role: role, Content: []Block{{Type: BlockText, Text: text}}, Timestamp: stamp, Model: model}, true
}

func (CodexCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	sessionID := firstNonEmpty(opts.ID, t.Meta.ID)
	header := map[string]any{"timestamp": t.Meta.Timestamp, "type": "session_meta", "payload": map[string]any{"id": sessionID, "timestamp": t.Meta.Timestamp, "cwd": t.Meta.CWD, "originator": "moirai", "cli_version": t.Meta.CLIVersion, "source": "cli", "model_provider": firstNonEmpty(t.Meta.ModelProvider, "openai"), "model": t.Meta.Model, "base_instructions": nil, "git": map[string]any{"branch": t.Meta.GitBranch}}}
	records := []any{header}
	for _, message := range t.Messages {
		stamp := firstNonEmpty(message.Timestamp, t.Meta.Timestamp)
		var content []any
		for _, block := range message.Content {
			switch block.Type {
			case BlockText:
				contentType := "input_text"
				if message.Role == RoleAssistant {
					contentType = "output_text"
				}
				content = append(content, map[string]any{"type": contentType, "text": block.Text})
			case BlockImage:
				if block.Source != nil && block.Source.URL != "" {
					content = append(content, map[string]any{"type": "input_image", "image_url": block.Source.URL})
				}
			case BlockThinking:
				payload := map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": block.Text}}}
				if block.Encrypted != "" {
					payload["encrypted_content"] = block.Encrypted
				}
				records = append(records, codexRecord(stamp, "response_item", payload))
			case BlockToolUse:
				records = append(records, codexRecord(stamp, "response_item", map[string]any{"type": "function_call", "name": block.Name, "arguments": string(block.Input), "call_id": block.ID}))
			case BlockToolResult:
				records = append(records, codexRecord(stamp, "response_item", map[string]any{"type": "function_call_output", "call_id": block.ToolUseID, "output": codexToolOutput(block.Content)}))
			}
		}
		if len(content) > 0 {
			records = append(records, codexRecord(stamp, "response_item", map[string]any{"type": "message", "role": message.Role, "content": content}))
		}
	}
	data, err := encodeJSONLines(records)
	return finalizeRender(t, FormatCodex, &RenderResult{Data: data}, err)
}

func codexRecord(stamp, kind string, payload any) map[string]any {
	return map[string]any{"timestamp": stamp, "type": kind, "payload": payload}
}

func codexSetupBlocks(blocks []Block) bool {
	if len(blocks) == 0 {
		return false
	}
	var text strings.Builder
	for _, block := range blocks {
		if block.Type != BlockText {
			return false
		}
		text.WriteString(block.Text)
	}
	value := strings.TrimSpace(text.String())
	for _, prefix := range []string{"<environment_context>", "<user_instructions>", "<permissions instructions>", "<collaboration_mode>", "<sandbox_mode>", "<approval_policy>"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func codexToolOutput(raw json.RawMessage) string {
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
		parts := make([]string, 0, len(entries))
		for _, entry := range entries {
			if item, ok := entry.(map[string]any); ok {
				if text := firstNonEmpty(stringValue(item["text"]), stringValue(item["content"])); text != "" {
					parts = append(parts, text)
					continue
				}
			}
			encoded, _ := json.Marshal(entry)
			parts = append(parts, string(encoded))
		}
		return strings.Join(parts, "\n")
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func init() { _ = Register(CodexCodec{}) }
