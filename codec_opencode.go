package moirai

import (
	"encoding/json"
	"fmt"
	"strings"
)

type OpenCodeCodec struct{}

func (OpenCodeCodec) Format() Format { return FormatOpenCode }
func (OpenCodeCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatOpenCode, DisplayName: "OpenCode", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

func (OpenCodeCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	var document map[string]any
	if err := decodeJSONDocument(data, &document, opts.Limits); err != nil {
		return nil, err
	}
	info := object(document["info"])
	timeInfo := object(info["time"])
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: stringValue(info["id"]), Timestamp: timestampValue(timeInfo["created"]), UpdatedAt: timestampValue(timeInfo["updated"]), CWD: stringValue(info["directory"]), Title: stringValue(info["title"]), CLIVersion: stringValue(info["version"]), Model: firstNonEmpty(stringValue(object(info["model"])["id"]), stringValue(info["modelID"]))}}
	if strings.HasPrefix(t.Meta.Title, "New session - ") {
		t.Meta.Title = ""
	}
	var warnings []Warning
	for ri, rawRecord := range array(document["messages"]) {
		record := object(rawRecord)
		messageInfo := object(record["info"])
		role := stringValue(messageInfo["role"])
		stamp := timestampValue(object(messageInfo["time"])["created"])
		parts := array(record["parts"])
		if role == "user" {
			var blocks []Block
			for _, rawPart := range parts {
				part := object(rawPart)
				switch stringValue(part["type"]) {
				case "text":
					if !boolValue(part["synthetic"]) && stringValue(part["text"]) != "" {
						blocks = append(blocks, Block{Type: BlockText, Text: stringValue(part["text"])})
					}
				case "file":
					if image := parseDataImage(part); image != nil {
						blocks = append(blocks, Block{Type: BlockImage, Source: image})
					}
				}
			}
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{ID: stringValue(messageInfo["id"]), Role: RoleUser, Content: blocks, Timestamp: stamp})
			}
			continue
		}
		if role != "assistant" {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d]", ri), Code: "unknown_role", Message: "message omitted"})
			continue
		}
		model := stringValue(messageInfo["modelID"])
		var assistantIndices []int
		var pending []Block
		flush := func() {
			if len(pending) == 0 {
				return
			}
			t.Messages = append(t.Messages, Message{Role: RoleAssistant, Content: pending, Timestamp: stamp, Model: model})
			assistantIndices = append(assistantIndices, len(t.Messages)-1)
			pending = nil
		}
		for pi, rawPart := range parts {
			part := object(rawPart)
			switch stringValue(part["type"]) {
			case "text":
				if !boolValue(part["synthetic"]) && stringValue(part["text"]) != "" {
					pending = append(pending, Block{Type: BlockText, Text: stringValue(part["text"])})
				}
			case "reasoning":
				if text := stringValue(part["text"]); text != "" {
					pending = append(pending, Block{Type: BlockThinking, Text: text})
				}
			case "file":
				if image := parseDataImage(part); image != nil {
					pending = append(pending, Block{Type: BlockImage, Source: image})
				}
			case "tool":
				flush()
				id := firstNonEmpty(stringValue(part["callID"]), syntheticToolID(ri, pi))
				state := object(part["state"])
				name, input := normalizeOpenCodeTool(stringValue(part["tool"]), state["input"])
				t.Messages = append(t.Messages, Message{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: id, Name: name, Input: rawJSON(input)}}, Timestamp: stamp, Model: model})
				assistantIndices = append(assistantIndices, len(t.Messages)-1)
				status := stringValue(state["status"])
				if status == "completed" || status == "error" {
					content := state["output"]
					if status == "error" {
						content = firstNonNil(state["error"], "Tool call failed")
					}
					t.Messages = append(t.Messages, Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(content), IsError: status == "error"}}, Timestamp: stamp})
				}
			}
		}
		flush()
		if len(assistantIndices) > 0 {
			last := &t.Messages[assistantIndices[len(assistantIndices)-1]]
			last.StopReason = stringValue(messageInfo["finish"])
			last.Usage = usageFromMap(messageInfo["tokens"], []string{"input"}, []string{"output"})
		}
		t.Meta.Model = firstNonEmpty(t.Meta.Model, model)
	}
	if len(t.Messages) == 0 {
		return nil, fmt.Errorf("%w: no messages", ErrInvalidTranscript)
	}
	if err := normalizeTranscript(t, opts.SourceID, ""); err != nil {
		return nil, err
	}
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	return &ParseResult{Transcript: t, Warnings: warnings}, nil
}

func parseDataImage(part map[string]any) *MediaSource {
	mime := stringValue(part["mime"])
	value := stringValue(part["url"])
	if !strings.HasPrefix(mime, "image/") || !strings.HasPrefix(value, "data:") {
		return nil
	}
	pieces := strings.SplitN(value, ",", 2)
	if len(pieces) != 2 {
		return nil
	}
	return &MediaSource{Type: "base64", MediaType: mime, Data: pieces[1]}
}

func normalizeOpenCodeTool(name string, input any) (string, any) {
	names := map[string]string{"bash": "Bash", "edit": "Edit", "write": "Write", "read": "Read", "glob": "Glob", "grep": "Grep", "list": "LS", "todowrite": "TodoWrite", "todoread": "TodoRead", "webfetch": "WebFetch", "task": "Task", "question": "Question"}
	if normalized := names[strings.ToLower(name)]; normalized != "" {
		name = normalized
	}
	values := object(input)
	renames := map[string]string{"filePath": "file_path", "oldString": "old_string", "newString": "new_string", "replaceAll": "replace_all"}
	for old, replacement := range renames {
		if value, ok := values[old]; ok {
			delete(values, old)
			values[replacement] = value
		}
	}
	if len(values) > 0 {
		input = values
	}
	return name, input
}

func denormalizeOpenCodeTool(name string, input any) (string, any) {
	names := map[string]string{"Bash": "bash", "Edit": "edit", "Write": "write", "Read": "read", "Glob": "glob", "Grep": "grep", "LS": "list", "TodoWrite": "todowrite", "TodoRead": "todoread", "WebFetch": "webfetch", "Task": "task", "Question": "question"}
	if native := names[name]; native != "" {
		name = native
	} else {
		name = strings.ToLower(name)
	}
	values := object(input)
	renames := map[string]string{"file_path": "filePath", "old_string": "oldString", "new_string": "newString", "replace_all": "replaceAll"}
	for old, replacement := range renames {
		if value, ok := values[old]; ok {
			delete(values, old)
			values[replacement] = value
		}
	}
	if len(values) > 0 {
		input = values
	}
	return name, input
}

func (OpenCodeCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, t.Meta.ID)
	if !strings.HasPrefix(id, "ses") {
		id = "ses_" + strings.ReplaceAll(id, "-", "")
	}
	var records []any
	lastUserID := ""
	for i := 0; i < len(t.Messages); i++ {
		message := t.Messages[i]
		messageID := "msg_" + stableID("", id, fmt.Sprint(i))
		stamp := epochMillis(firstNonEmpty(message.Timestamp, t.Meta.Timestamp))
		info := map[string]any{"id": messageID, "sessionID": id, "role": message.Role, "time": map[string]any{"created": stamp, "completed": stamp}}
		var parts []any
		if message.Role == RoleUser {
			info["agent"] = "build"
			info["model"] = map[string]any{"providerID": "unknown", "modelID": firstNonEmpty(message.Model, t.Meta.Model, "unknown")}
			lastUserID = messageID
			for j, block := range message.Content {
				partID := "prt_" + stableID("", messageID, fmt.Sprint(j))
				switch block.Type {
				case BlockText:
					parts = append(parts, map[string]any{"id": partID, "sessionID": id, "messageID": messageID, "type": "text", "text": block.Text, "time": map[string]any{"start": stamp, "end": stamp}})
				case BlockImage:
					if block.Source != nil {
						parts = append(parts, map[string]any{"id": partID, "sessionID": id, "messageID": messageID, "type": "file", "mime": block.Source.MediaType, "url": fmt.Sprintf("data:%s;%s,%s", block.Source.MediaType, block.Source.Type, block.Source.Data)})
					}
				}
			}
		} else {
			info["modelID"] = firstNonEmpty(message.Model, t.Meta.Model, "unknown")
			info["providerID"] = "unknown"
			info["mode"] = "build"
			info["agent"] = "build"
			info["parentID"] = firstNonEmpty(lastUserID, messageID)
			info["path"] = map[string]any{"cwd": t.Meta.CWD, "root": t.Meta.CWD}
			info["finish"] = openCodeFinish(message.StopReason)
			usage := message.Usage
			if usage == nil {
				usage = &Usage{}
			}
			info["tokens"] = map[string]any{"input": usage.InputTokens, "output": usage.OutputTokens, "reasoning": 0, "cache": map[string]any{"read": usage.CacheReadInputTokens, "write": usage.CacheCreationInputTokens}}
			parts = append(parts, map[string]any{"id": "prt_" + stableID("", messageID, "start"), "sessionID": id, "messageID": messageID, "type": "step-start"})
			for j, block := range message.Content {
				partID := "prt_" + stableID("", messageID, fmt.Sprint(j))
				switch block.Type {
				case BlockText:
					parts = append(parts, map[string]any{"id": partID, "sessionID": id, "messageID": messageID, "type": "text", "text": block.Text, "time": map[string]any{"start": stamp, "end": stamp}})
				case BlockThinking:
					parts = append(parts, map[string]any{"id": partID, "sessionID": id, "messageID": messageID, "type": "reasoning", "text": block.Text, "time": map[string]any{"start": stamp, "end": stamp}})
				case BlockToolUse:
					var input any
					_ = json.Unmarshal(block.Input, &input)
					name, input := denormalizeOpenCodeTool(block.Name, input)
					state := map[string]any{"status": "running", "input": input, "title": name, "metadata": map[string]any{}, "time": map[string]any{"start": stamp}}
					if i+1 < len(t.Messages) && len(t.Messages[i+1].Content) == 1 {
						result := t.Messages[i+1].Content[0]
						if result.Type == BlockToolResult && result.ToolUseID == block.ID {
							var output any
							_ = json.Unmarshal(result.Content, &output)
							if result.IsError {
								state["status"], state["error"] = "error", fmt.Sprint(output)
							} else {
								state["status"], state["output"] = "completed", output
							}
							i++
						}
					}
					parts = append(parts, map[string]any{"id": partID, "sessionID": id, "messageID": messageID, "type": "tool", "tool": name, "callID": block.ID, "state": state})
				}
			}
		}
		records = append(records, map[string]any{"info": info, "parts": parts})
	}
	document := map[string]any{"info": map[string]any{"id": id, "slug": "moirai-" + stableID("", id)[:8], "directory": t.Meta.CWD, "title": t.Meta.Title, "version": t.Meta.CLIVersion, "time": map[string]any{"created": epochMillis(t.Meta.Timestamp), "updated": epochMillis(firstNonEmpty(t.Meta.UpdatedAt, t.Meta.Timestamp))}, "model": map[string]any{"id": t.Meta.Model, "providerID": "unknown"}}, "messages": records}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return finalizeRender(t, FormatOpenCode, &RenderResult{Data: append(data, '\n')}, nil)
}

func openCodeFinish(reason string) string {
	switch reason {
	case "max_tokens", "length":
		return "length"
	case "tool_use", "tool-calls", "tool_calls":
		return "tool_use"
	case "error":
		return "error"
	default:
		return "stop"
	}
}

func init() { _ = Register(OpenCodeCodec{}) }
