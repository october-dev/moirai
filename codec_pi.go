package moirai

import (
	"encoding/json"
	"fmt"
)

type PiCodec struct {
	format Format
	name   string
}

func NewPiCodec(format Format, name string) PiCodec { return PiCodec{format: format, name: name} }
func (c PiCodec) Format() Format                    { return c.format }
func (c PiCodec) Info() HarnessInfo {
	return HarnessInfo{Format: c.format, DisplayName: c.name, Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

func (c PiCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	records, warnings, err := decodeJSONLines(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	t := &Transcript{SchemaVersion: SchemaVersion}
	var pending []string
	for _, record := range records {
		switch stringValue(record["type"]) {
		case "session":
			t.Meta.ID = firstNonEmpty(t.Meta.ID, stringValue(record["id"]))
			t.Meta.Timestamp = firstNonEmpty(t.Meta.Timestamp, timestampValue(record["timestamp"]))
			t.Meta.CWD = firstNonEmpty(t.Meta.CWD, stringValue(record["cwd"]))
		case "session_info":
			t.Meta.Title = firstNonEmpty(stringValue(record["name"]), t.Meta.Title)
		case "model_change":
			t.Meta.Model = firstNonEmpty(stringValue(record["modelId"]), t.Meta.Model)
		case "custom_message":
			content := firstNonEmpty(stringValue(record["content"]), stringValue(record["message"]))
			if content != "" {
				t.Messages = append(t.Messages, Message{ID: stringValue(record["id"]), Role: RoleUser, Content: []Block{{Type: BlockText, Text: content}}, Timestamp: timestampValue(record["timestamp"])})
			}
		case "message":
			payload := object(record["message"])
			role := stringValue(payload["role"])
			stamp := firstNonEmpty(timestampValue(record["timestamp"]), timestampValue(payload["timestamp"]))
			switch role {
			case "user", "assistant":
				blocks, blockWarnings := parseAnthropicContent(payload["content"], len(t.Messages), &pending)
				warnings = append(warnings, blockWarnings...)
				if len(blocks) > 0 {
					message := Message{ID: stringValue(record["id"]), Role: Role(role), Content: blocks, Timestamp: stamp, Model: stringValue(payload["model"]), StopReason: stringValue(payload["stopReason"]), Usage: usageFromMap(payload["usage"], []string{"input"}, []string{"output"})}
					t.Messages = append(t.Messages, message)
					t.Meta.Model = firstNonEmpty(message.Model, t.Meta.Model)
				}
			case "toolResult":
				id := stringValue(payload["toolCallId"])
				if id == "" && len(pending) > 0 {
					id, pending = pending[0], pending[1:]
				}
				content := payload["content"]
				if content == nil {
					content = payload["output"]
				}
				t.Messages = append(t.Messages, Message{ID: stringValue(record["id"]), Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(content), IsError: boolValue(payload["isError"])}}, Timestamp: stamp})
			case "bashExecution":
				if boolValue(payload["excludeFromContext"]) {
					continue
				}
				id := stableID("bash_exec_", t.Meta.ID, fmt.Sprint(len(t.Messages)))
				input := rawJSON(map[string]any{"command": stringValue(payload["command"])})
				t.Messages = append(t.Messages,
					Message{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: id, Name: "Bash", Input: input}}, Timestamp: stamp},
					Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(payload["output"]), IsError: integerValue(payload["exitCode"]) != 0}}, Timestamp: stamp},
				)
			}
		}
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

func (c PiCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	sessionID := firstNonEmpty(opts.ID, t.Meta.ID)
	records := []any{map[string]any{"type": "session", "version": 3, "id": sessionID, "timestamp": t.Meta.Timestamp, "cwd": t.Meta.CWD}}
	parent := ""
	for mi, message := range t.Messages {
		baseID := stableID("", sessionID, fmt.Sprint(mi))[:8]
		stamp := firstNonEmpty(message.Timestamp, t.Meta.Timestamp)
		regular := make([]Block, 0, len(message.Content))
		for bi, block := range message.Content {
			if block.Type != BlockToolResult {
				regular = append(regular, block)
				continue
			}
			id := baseID
			if len(regular) > 0 || bi > 0 {
				id = stableID("", baseID, fmt.Sprint(bi))[:8]
			}
			var content any
			_ = json.Unmarshal(block.Content, &content)
			payload := map[string]any{"role": "toolResult", "toolCallId": block.ToolUseID, "content": content, "isError": block.IsError, "timestamp": epochMillis(stamp)}
			record := map[string]any{"type": "message", "id": id, "parentId": parent, "timestamp": stamp, "message": payload}
			records = append(records, record)
			parent = id
		}
		if len(regular) == 0 {
			continue
		}
		content := renderPiContent(regular)
		payload := map[string]any{"role": message.Role, "content": content, "timestamp": epochMillis(stamp)}
		if message.Role == RoleAssistant {
			payload["model"] = firstNonEmpty(message.Model, t.Meta.Model, "unknown")
			payload["provider"] = "unknown"
			payload["api"] = "unknown"
			payload["stopReason"] = firstNonEmpty(message.StopReason, "stop")
			if message.Usage != nil {
				payload["usage"] = map[string]any{"input": message.Usage.InputTokens, "output": message.Usage.OutputTokens, "cacheRead": message.Usage.CacheReadInputTokens, "cacheWrite": message.Usage.CacheCreationInputTokens, "cost": map[string]any{"total": 0}}
			}
		}
		record := map[string]any{"type": "message", "id": baseID, "parentId": parent, "timestamp": stamp, "message": payload}
		records = append(records, record)
		parent = baseID
	}
	if t.Meta.Title != "" {
		records = append(records, map[string]any{"type": "session_info", "id": stableID("", sessionID, "info")[:8], "parentId": parent, "timestamp": t.Meta.Timestamp, "name": t.Meta.Title})
	}
	data, err := encodeJSONLines(records)
	return finalizeRender(t, c.format, &RenderResult{Data: data}, err)
}

func renderPiContent(blocks []Block) []any {
	var result []any
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			result = append(result, map[string]any{"type": "text", "text": block.Text})
		case BlockThinking:
			result = append(result, map[string]any{"type": "thinking", "thinking": block.Text, "signature": block.Signature})
		case BlockToolUse:
			var args any
			_ = json.Unmarshal(block.Input, &args)
			result = append(result, map[string]any{"type": "toolCall", "id": block.ID, "name": block.Name, "arguments": args})
		case BlockImage:
			result = append(result, map[string]any{"type": "image", "source": block.Source})
		}
	}
	return result
}

func init() {
	_ = Register(NewPiCodec(FormatPi, "pi"))
	_ = Register(NewPiCodec(FormatCampfire, "Campfire"))
}
