package moirai

import (
	"encoding/json"
	"fmt"
)

type AmpCodec struct{}

func (AmpCodec) Format() Format { return FormatAmp }
func (AmpCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatAmp, DisplayName: "Amp", Capability: Capability{Read: true, Write: true, Discover: true, SourceOnly: true}}
}

func (AmpCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	limits := opts.Limits.normalized()
	var document map[string]any
	if err := decodeJSONDocument(data, &document, limits); err != nil {
		return nil, err
	}
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: stringValue(document["id"]), Timestamp: timestampValue(document["created"]), Title: stringValue(document["title"])}}
	initial := object(object(document["env"])["initial"])
	if trees := array(initial["trees"]); len(trees) > 0 {
		tree := object(trees[0])
		t.Meta.CWD = decodeFileURI(stringValue(tree["uri"]))
		t.Meta.GitBranch = stringValue(object(tree["repository"])["ref"])
	}
	t.Meta.CLIVersion = stringValue(object(initial["platform"])["clientVersion"])
	var pending []string
	var warnings []Warning
	for mi, rawMessage := range array(document["messages"]) {
		entry := object(rawMessage)
		role := Role(stringValue(entry["role"]))
		if role != RoleUser && role != RoleAssistant {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d]", mi), Code: "unknown_role", Message: "message omitted"})
			continue
		}
		content := normalizeAmpContent(array(entry["content"]))
		blocks, blockWarnings := parseAnthropicContent(content, mi, &pending)
		warnings = append(warnings, blockWarnings...)
		if len(blocks) == 0 {
			continue
		}
		usageMap := object(entry["usage"])
		stamp := firstNonEmpty(timestampValue(object(entry["meta"])["sentAt"]), timestampValue(usageMap["timestamp"]))
		state := object(entry["state"])
		message := Message{ID: fmt.Sprint(entry["messageId"]), Role: role, Content: blocks, Timestamp: stamp, Model: stringValue(usageMap["model"]), StopReason: stringValue(state["stopReason"]), Usage: usageFromMap(usageMap, []string{"inputTokens"}, []string{"outputTokens"})}
		t.Messages = append(t.Messages, message)
		t.Meta.Model = firstNonEmpty(t.Meta.Model, message.Model)
	}
	if len(t.Messages) == 0 {
		return nil, fmt.Errorf("%w: no messages", ErrInvalidTranscript)
	}
	if err := normalizeTranscript(t, opts.SourceID, ""); err != nil {
		return nil, err
	}
	if err := Validate(t, limits); err != nil {
		return nil, err
	}
	return &ParseResult{Transcript: t, Warnings: warnings}, nil
}

func normalizeAmpContent(content []any) []any {
	result := make([]any, 0, len(content))
	for _, raw := range content {
		entry := object(raw)
		if stringValue(entry["type"]) != "tool_result" {
			result = append(result, entry)
			continue
		}
		run := object(entry["run"])
		status := stringValue(run["status"])
		value := firstNonNil(run["result"], run["error"], run["reason"])
		result = append(result, map[string]any{"type": "tool_result", "tool_use_id": entry["toolUseID"], "content": value, "is_error": status != "" && status != "done"})
	}
	return result
}

func (AmpCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, t.Meta.ID)
	var messages []any
	for i, message := range t.Messages {
		entry := map[string]any{"role": message.Role, "messageId": i, "content": renderAmpContent(message.Content)}
		if message.Role == RoleUser {
			entry["meta"] = map[string]any{"sentAt": epochMillis(firstNonEmpty(message.Timestamp, t.Meta.Timestamp))}
		} else {
			entry["state"] = map[string]any{"type": "complete", "stopReason": firstNonEmpty(message.StopReason, "end_turn")}
			if message.Usage != nil {
				entry["usage"] = map[string]any{"model": firstNonEmpty(message.Model, t.Meta.Model), "inputTokens": message.Usage.InputTokens, "outputTokens": message.Usage.OutputTokens, "cacheReadInputTokens": message.Usage.CacheReadInputTokens, "cacheCreationInputTokens": message.Usage.CacheCreationInputTokens, "timestamp": firstNonEmpty(message.Timestamp, t.Meta.Timestamp)}
			}
		}
		messages = append(messages, entry)
	}
	document := map[string]any{"v": 1, "id": id, "created": epochMillis(t.Meta.Timestamp), "title": t.Meta.Title, "agentMode": "smart", "env": map[string]any{"initial": map[string]any{"trees": []any{map[string]any{"uri": "file://" + t.Meta.CWD, "repository": map[string]any{"ref": t.Meta.GitBranch, "type": "git"}}}, "platform": map[string]any{"client": "CLI", "clientVersion": t.Meta.CLIVersion}}}, "messages": messages}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return finalizeRender(t, FormatAmp, &RenderResult{Data: append(data, '\n')}, nil)
}

func renderAmpContent(blocks []Block) []any {
	var result []any
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			result = append(result, map[string]any{"type": "text", "text": block.Text})
		case BlockThinking:
			result = append(result, map[string]any{"type": "thinking", "thinking": block.Text, "signature": block.Signature})
		case BlockToolUse:
			var input any
			_ = json.Unmarshal(block.Input, &input)
			result = append(result, map[string]any{"type": "tool_use", "complete": true, "id": block.ID, "name": block.Name, "input": input})
		case BlockToolResult:
			var content any
			_ = json.Unmarshal(block.Content, &content)
			status := "done"
			if block.IsError {
				status = "error"
			}
			result = append(result, map[string]any{"type": "tool_result", "toolUseID": block.ToolUseID, "run": map[string]any{"status": status, "result": content}})
		case BlockImage:
			if block.Source != nil {
				result = append(result, map[string]any{"type": "image", "source": map[string]any{"type": block.Source.Type, "mediaType": block.Source.MediaType, "data": block.Source.Data}})
			}
		}
	}
	return result
}

func init() { _ = Register(AmpCodec{}) }
