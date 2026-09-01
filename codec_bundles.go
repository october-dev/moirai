package moirai

import (
	"encoding/json"
	"fmt"
	"strings"
)

type GrokCodec struct{}

func (GrokCodec) Format() Format { return FormatGrok }
func (GrokCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatGrok, DisplayName: "Grok CLI", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

func (GrokCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	body, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	summary := object(body["summary"])
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: stringValue(object(summary["info"])["id"]), Timestamp: timestampValue(summary["created_at"]), UpdatedAt: timestampValue(summary["updated_at"]), CWD: stringValue(object(summary["info"])["cwd"]), GitBranch: stringValue(summary["head_branch"]), Title: firstNonEmpty(stringValue(summary["generated_title"]), stringValue(summary["session_summary"])), Model: stringValue(summary["current_model_id"])}}
	var warnings []Warning
	known := map[string]bool{}
	for i, raw := range array(body["chat_history"]) {
		record := object(raw)
		switch stringValue(record["type"]) {
		case "user":
			var pending []string
			blocks, blockWarnings := parseAnthropicContent(record["content"], len(t.Messages), &pending)
			warnings = append(warnings, blockWarnings...)
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{Role: RoleUser, Content: blocks, Timestamp: t.Meta.Timestamp})
			}
		case "reasoning":
			text := ""
			for _, part := range array(record["summary"]) {
				text = firstNonEmpty(text, stringValue(object(part)["text"]))
			}
			if text != "" || stringValue(record["encrypted_content"]) != "" {
				t.Messages = append(t.Messages, Message{Role: RoleAssistant, Content: []Block{{Type: BlockThinking, Text: text, Encrypted: stringValue(record["encrypted_content"])}}, Timestamp: t.Meta.Timestamp, Model: t.Meta.Model})
			}
		case "assistant":
			var blocks []Block
			if text := stringValue(record["content"]); text != "" {
				blocks = append(blocks, Block{Type: BlockText, Text: text})
			}
			for ci, rawCall := range array(record["tool_calls"]) {
				call := object(rawCall)
				id := firstNonEmpty(stringValue(call["id"]), syntheticToolID(i, ci))
				arguments := call["arguments"]
				if encoded, ok := arguments.(string); ok && json.Valid([]byte(encoded)) {
					var decoded any
					_ = json.Unmarshal([]byte(encoded), &decoded)
					arguments = decoded
				}
				blocks = append(blocks, Block{Type: BlockToolUse, ID: id, Name: stringValue(call["name"]), Input: rawJSON(arguments)})
				known[id] = true
			}
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{Role: RoleAssistant, Content: blocks, Timestamp: t.Meta.Timestamp, Model: firstNonEmpty(stringValue(record["model_id"]), t.Meta.Model)})
			}
		case "tool_result":
			id := stringValue(record["tool_call_id"])
			if !known[id] {
				warnings = append(warnings, Warning{Path: fmt.Sprintf("chat_history[%d]", i), Code: "unpaired_tool_result", Message: "result omitted"})
				continue
			}
			t.Messages = append(t.Messages, Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(record["content"])}}, Timestamp: t.Meta.Timestamp})
		}
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

func (GrokCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, t.Meta.ID)
	var chat []any
	var updates []any
	prompt := 0
	for mi, message := range t.Messages {
		stamp := firstNonEmpty(message.Timestamp, t.Meta.Timestamp)
		if message.Role == RoleUser {
			var texts []string
			for _, block := range message.Content {
				switch block.Type {
				case BlockText:
					texts = append(texts, block.Text)
				case BlockToolResult:
					var content any
					_ = json.Unmarshal(block.Content, &content)
					chat = append(chat, map[string]any{"type": "tool_result", "tool_call_id": block.ToolUseID, "content": fmt.Sprint(content)})
					status := "completed"
					if block.IsError {
						status = "failed"
					}
					updates = append(updates, grokUpdate(id, stamp, "tool_call_update", map[string]any{"toolCallId": block.ToolUseID, "status": status, "content": fmt.Sprint(content)}))
				}
			}
			if len(texts) > 0 {
				text := strings.Join(texts, "\n\n")
				chat = append(chat, map[string]any{"type": "user", "content": []any{map[string]any{"type": "text", "text": text}}})
				updates = append(updates, grokUpdate(id, stamp, "user_message_chunk", map[string]any{"content": map[string]any{"type": "text", "text": text}, "_meta": map[string]any{"promptIndex": prompt}}))
				prompt++
			}
			continue
		}
		var texts []string
		var calls []any
		for bi, block := range message.Content {
			switch block.Type {
			case BlockText:
				texts = append(texts, block.Text)
			case BlockThinking:
				chat = append(chat, map[string]any{"type": "reasoning", "id": stableID("rs_", id, fmt.Sprint(mi), fmt.Sprint(bi)), "summary": []any{map[string]any{"type": "summary_text", "text": block.Text}}, "encrypted_content": block.Encrypted, "status": "completed"})
				updates = append(updates, grokUpdate(id, stamp, "agent_thought_chunk", map[string]any{"content": map[string]any{"type": "text", "text": block.Text}}))
			case BlockToolUse:
				calls = append(calls, map[string]any{"id": block.ID, "name": block.Name, "arguments": string(block.Input)})
				var input any
				_ = json.Unmarshal(block.Input, &input)
				updates = append(updates, grokUpdate(id, stamp, "tool_call", map[string]any{"toolCallId": block.ID, "title": block.Name, "kind": "other", "rawInput": input}))
			}
		}
		if len(texts) > 0 || len(calls) > 0 {
			text := strings.Join(texts, "\n\n")
			chat = append(chat, map[string]any{"type": "assistant", "content": text, "tool_calls": calls, "model_id": firstNonEmpty(message.Model, t.Meta.Model)})
			if text != "" {
				updates = append(updates, grokUpdate(id, stamp, "agent_message_chunk", map[string]any{"content": map[string]any{"type": "text", "text": text}}))
			}
		}
		updates = append(updates, grokUpdate(id, stamp, "turn_completed", map[string]any{"stop_reason": firstNonEmpty(message.StopReason, "end_turn")}))
	}
	body := map[string]any{"chat_history": chat, "updates": updates, "summary": map[string]any{"info": map[string]any{"id": id, "cwd": t.Meta.CWD}, "session_summary": t.Meta.Title, "generated_title": t.Meta.Title, "created_at": t.Meta.Timestamp, "updated_at": firstNonEmpty(t.Meta.UpdatedAt, t.Meta.Timestamp), "num_messages": len(updates), "num_chat_messages": len(chat), "current_model_id": t.Meta.Model, "head_branch": t.Meta.GitBranch, "chat_format_version": 1}}
	result, err := encodeObject(body)
	return finalizeRender(t, FormatGrok, result, err)
}

func grokUpdate(id, stamp, kind string, fields map[string]any) map[string]any {
	update := map[string]any{"sessionUpdate": kind}
	for key, value := range fields {
		update[key] = value
	}
	return map[string]any{"timestamp": epochMillis(stamp) / 1000, "method": "session/update", "params": map[string]any{"sessionId": id, "update": update, "_meta": map[string]any{"agentTimestampMs": epochMillis(stamp)}}}
}

type FXCodec struct{}

func (FXCodec) Format() Format { return FormatFX }
func (FXCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatFX, DisplayName: "fx", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

func (FXCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	body, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	header := object(body["session"])
	display := object(body["display"])
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: stringValue(header["id"]), Timestamp: timestampValue(header["created_at_ms"]), UpdatedAt: timestampValue(header["updated_at_ms"]), CWD: firstNonEmpty(stringValue(header["workspace_root"]), stringValue(header["origin_workspace_root"])), Title: stringValue(display["title"]), Model: stringValue(object(header["preferences"])["model"])}}
	known := map[string]bool{}
	var warnings []Warning
	for ei, rawEvent := range array(body["events"]) {
		event := object(rawEvent)
		if stringValue(event["kind"]) != "history_turn_committed" {
			continue
		}
		stamp := timestampValue(event["timestamp_ms"])
		turn := object(object(event["payload"])["turn"])
		user := object(turn["user"])
		if text := stringValue(user["text"]); text != "" {
			t.Messages = append(t.Messages, Message{Role: RoleUser, Content: []Block{{Type: BlockText, Text: text}}, Timestamp: stamp})
		}
		if stringValue(turn["kind"]) == "interrupted" {
			blocks := fxAssistantBlocks(turn["assistant"], []any{turn["tool_call"]}, known, ei)
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{Role: RoleAssistant, Content: blocks, Timestamp: stamp, Model: t.Meta.Model, StopReason: "aborted"})
			}
			continue
		}
		for si, rawStep := range array(object(turn["execution"])["tool_steps"]) {
			step := object(rawStep)
			blocks := fxAssistantBlocks(step["assistant"], array(step["tool_calls"]), known, ei*100+si)
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{Role: RoleAssistant, Content: blocks, Timestamp: stamp, Model: t.Meta.Model})
			}
			var results []Block
			for ri, rawResult := range array(step["tool_results"]) {
				result := object(rawResult)
				id := stringValue(result["tool_call_id"])
				if !known[id] {
					warnings = append(warnings, Warning{Path: fmt.Sprintf("events[%d].steps[%d].results[%d]", ei, si, ri), Code: "unpaired_tool_result", Message: "result omitted"})
					continue
				}
				results = append(results, Block{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(result["output"]), IsError: stringValue(result["status"]) == "failure"})
			}
			if len(results) > 0 {
				t.Messages = append(t.Messages, Message{Role: RoleUser, Content: results, Timestamp: stamp})
			}
		}
		if text := stringValue(turn["assistant"]); text != "" {
			t.Messages = append(t.Messages, Message{Role: RoleAssistant, Content: []Block{{Type: BlockText, Text: text}}, Timestamp: stamp, Model: t.Meta.Model, StopReason: "end_turn"})
		}
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

func fxAssistantBlocks(textValue any, calls []any, known map[string]bool, seed int) []Block {
	var blocks []Block
	if text := stringValue(textValue); text != "" {
		blocks = append(blocks, Block{Type: BlockText, Text: text})
	}
	for i, rawCall := range calls {
		call := object(rawCall)
		if len(call) == 0 {
			continue
		}
		id := firstNonEmpty(stringValue(call["id"]), syntheticToolID(seed, i))
		arguments := call["arguments_json"]
		if encoded, ok := arguments.(string); ok && json.Valid([]byte(encoded)) {
			var decoded any
			_ = json.Unmarshal([]byte(encoded), &decoded)
			arguments = decoded
		}
		blocks = append(blocks, Block{Type: BlockToolUse, ID: id, Name: firstNonEmpty(stringValue(call["name"]), "tool"), Input: rawJSON(arguments)})
		known[id] = true
	}
	return blocks
}

func (FXCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, t.Meta.ID)
	generation := stableID("", id, "generation")
	var events []any
	events = append(events, map[string]any{"schema_version": 1, "log_generation": generation, "seq": 1, "event_id": stableID("", id, "event:1"), "timestamp_ms": epochMillis(t.Meta.Timestamp), "kind": "session_started", "payload": map[string]any{"id": id, "created_at_ms": epochMillis(t.Meta.Timestamp), "workspace_root": t.Meta.CWD}})
	turns := canonicalTurns(t.Messages)
	for i, turn := range turns {
		stamp := firstNonEmpty(turn.stamp, t.Meta.Timestamp)
		events = append(events, map[string]any{"schema_version": 1, "log_generation": generation, "seq": i + 2, "event_id": stableID("", id, fmt.Sprintf("event:%d", i+2)), "timestamp_ms": epochMillis(stamp), "kind": "history_turn_committed", "payload": map[string]any{"conversation_language": "und", "total_input_tokens": 0, "total_output_tokens": 0, "turn": turn.value}})
	}
	last := t.Meta.Timestamp
	if len(t.Messages) > 0 {
		last = firstNonEmpty(t.Messages[len(t.Messages)-1].Timestamp, last)
	}
	authority := stableID("", id, "authority")
	body := map[string]any{"events": events, "session": map[string]any{"schema_version": 3, "storage_format": "event_log_v1", "id": id, "authority_id": authority, "log_generation": generation, "created_at_ms": epochMillis(t.Meta.Timestamp), "updated_at_ms": epochMillis(last), "origin_workspace_root": t.Meta.CWD, "workspace_root": t.Meta.CWD, "history_len": len(turns), "preferences": map[string]any{"model": t.Meta.Model}}, "authority": map[string]any{"schema_version": 1, "session_id": id, "authority_id": authority, "storage_format": "event_log_v1", "source": "native_create"}, "commit": map[string]any{"schema_version": 1, "session_id": id, "log_generation": generation, "through_seq": len(events)}, "display": map[string]any{"schema_version": 1, "title": t.Meta.Title, "preview": t.Meta.Title, "origin_workspace_root": t.Meta.CWD}}
	result, err := encodeObject(body)
	return finalizeRender(t, FormatFX, result, err)
}

type fxTurn struct {
	stamp string
	value any
}

func canonicalTurns(messages []Message) []fxTurn {
	var turns []fxTurn
	for i := 0; i < len(messages); {
		var prompt *Message
		if messages[i].Role == RoleUser && hasBlockType(messages[i], BlockText) {
			prompt = &messages[i]
			i++
		}
		stamp := ""
		userText := ""
		if prompt != nil {
			stamp, userText = prompt.Timestamp, joinedBlocks(prompt.Content, BlockText)
		}
		var steps []any
		final := ""
		for i < len(messages) && !(messages[i].Role == RoleUser && hasBlockType(messages[i], BlockText)) {
			message := messages[i]
			if stamp == "" {
				stamp = message.Timestamp
			}
			if message.Role == RoleAssistant {
				var calls []any
				for _, block := range message.Content {
					if block.Type == BlockToolUse {
						calls = append(calls, map[string]any{"id": block.ID, "name": block.Name, "arguments_json": string(block.Input)})
					}
				}
				text := joinedBlocks(message.Content, BlockText)
				if len(calls) == 0 {
					final = text
					i++
					continue
				}
				var results []any
				if i+1 < len(messages) && messages[i+1].Role == RoleUser {
					for _, block := range messages[i+1].Content {
						if block.Type == BlockToolResult {
							var output any
							_ = json.Unmarshal(block.Content, &output)
							status := "success"
							if block.IsError {
								status = "failure"
							}
							results = append(results, map[string]any{"tool_call_id": block.ToolUseID, "output": fmt.Sprint(output), "status": status})
						}
					}
					i++
				}
				steps = append(steps, map[string]any{"assistant": text, "tool_calls": calls, "tool_results": results})
			}
			i++
		}
		turns = append(turns, fxTurn{stamp: stamp, value: map[string]any{"kind": "assistant", "user": map[string]any{"text": userText, "images": []any{}}, "assistant": final, "execution": map[string]any{"schema_version": 3, "tool_steps": steps, "files": []any{}}}})
	}
	return turns
}

func hasBlockType(message Message, kind BlockType) bool {
	for _, block := range message.Content {
		if block.Type == kind {
			return true
		}
	}
	return false
}

func init() {
	_ = Register(GrokCodec{})
	_ = Register(FXCodec{})
}
