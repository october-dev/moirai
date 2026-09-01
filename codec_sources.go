package moirai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type HermesCodec struct{}

func (HermesCodec) Format() Format { return FormatHermes }
func (HermesCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatHermes, DisplayName: "Hermes Agent", Capability: Capability{Read: true, Write: true, Discover: true, SourceOnly: true}}
}

func (HermesCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	document, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: stringValue(document["id"]), Timestamp: timestampValue(document["started_at"]), CWD: stringValue(document["cwd"]), GitBranch: stringValue(document["git_branch"]), Title: stringValue(document["title"]), Model: stringValue(document["model"])}}
	var warnings []Warning
	var knownTools = map[string]bool{}
	for i, raw := range array(document["messages"]) {
		row := object(raw)
		if integerValue(row["active"]) == 0 && row["active"] != nil {
			continue
		}
		stamp := timestampValue(row["timestamp"])
		switch stringValue(row["role"]) {
		case "user":
			blocks := looseContentBlocks(row["content"])
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{ID: fmt.Sprint(row["id"]), Role: RoleUser, Content: blocks, Timestamp: stamp})
			}
		case "assistant":
			var blocks []Block
			thinking := firstNonEmpty(stringValue(row["reasoning_content"]), stringValue(row["reasoning"]))
			encrypted := ""
			if value := firstNonNil(row["codex_reasoning_items"], row["reasoning_details"]); value != nil {
				encrypted = string(rawJSON(value))
			}
			if thinking != "" || encrypted != "" {
				blocks = append(blocks, Block{Type: BlockThinking, Text: thinking, Encrypted: encrypted})
			}
			blocks = append(blocks, looseContentBlocks(row["content"])...)
			calls := row["tool_calls"]
			if encoded, ok := calls.(string); ok {
				var decoded any
				if json.Unmarshal([]byte(encoded), &decoded) == nil {
					calls = decoded
				}
			}
			for ci, rawCall := range array(calls) {
				call := object(rawCall)
				function := object(call["function"])
				name := stringValue(function["name"])
				if name == "" {
					continue
				}
				id := firstNonEmpty(stringValue(call["id"]), stringValue(call["call_id"]), syntheticToolID(i, ci))
				input := function["arguments"]
				if encoded, ok := input.(string); ok && json.Valid([]byte(encoded)) {
					var decoded any
					_ = json.Unmarshal([]byte(encoded), &decoded)
					input = decoded
				}
				blocks = append(blocks, Block{Type: BlockToolUse, ID: id, Name: name, Input: rawJSON(input)})
				knownTools[id] = true
			}
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{ID: fmt.Sprint(row["id"]), Role: RoleAssistant, Content: blocks, Timestamp: stamp, Model: t.Meta.Model, StopReason: stringValue(row["finish_reason"])})
			}
		case "tool":
			id := stringValue(row["tool_call_id"])
			if !knownTools[id] {
				warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d]", i), Code: "unpaired_tool_result", Message: "result omitted"})
				continue
			}
			content := row["content"]
			if encoded, ok := content.(string); ok && json.Valid([]byte(encoded)) {
				var decoded any
				_ = json.Unmarshal([]byte(encoded), &decoded)
				content = decoded
			}
			isError := stringValue(row["effect_disposition"]) == "denied"
			if result := object(content); boolValue(result["success"]) == false && result["success"] != nil || integerValue(result["exit_code"]) != 0 || result["error"] != nil {
				isError = true
			}
			t.Messages = append(t.Messages, Message{ID: fmt.Sprint(row["id"]), Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(content), IsError: isError}}, Timestamp: stamp})
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

func (HermesCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	var rows []any
	rowID := 1
	for _, message := range t.Messages {
		stamp := float64(epochMillis(firstNonEmpty(message.Timestamp, t.Meta.Timestamp))) / 1000
		if message.Role == RoleUser {
			for _, block := range message.Content {
				if block.Type == BlockToolResult {
					var content any
					_ = json.Unmarshal(block.Content, &content)
					rows = append(rows, map[string]any{"id": rowID, "session_id": t.Meta.ID, "role": "tool", "content": content, "tool_call_id": block.ToolUseID, "effect_disposition": map[bool]string{true: "denied", false: "allowed"}[block.IsError], "timestamp": stamp, "active": 1})
					rowID++
				}
			}
			text := joinedBlocks(message.Content, BlockText)
			if text != "" {
				rows = append(rows, map[string]any{"id": rowID, "session_id": t.Meta.ID, "role": "user", "content": text, "timestamp": stamp, "active": 1})
				rowID++
			}
			continue
		}
		row := map[string]any{"id": rowID, "session_id": t.Meta.ID, "role": "assistant", "content": joinedBlocks(message.Content, BlockText), "reasoning_content": joinedBlocks(message.Content, BlockThinking), "finish_reason": message.StopReason, "timestamp": stamp, "active": 1}
		var calls []any
		for _, block := range message.Content {
			if block.Type != BlockToolUse {
				continue
			}
			var input any
			_ = json.Unmarshal(block.Input, &input)
			calls = append(calls, map[string]any{"id": block.ID, "type": "function", "function": map[string]any{"name": block.Name, "arguments": input}})
		}
		if len(calls) > 0 {
			row["tool_calls"] = calls
		}
		rows = append(rows, row)
		rowID++
	}
	document := map[string]any{"id": firstNonEmpty(opts.ID, t.Meta.ID), "source": "moirai", "model": t.Meta.Model, "started_at": float64(epochMillis(t.Meta.Timestamp)) / 1000, "cwd": t.Meta.CWD, "git_branch": t.Meta.GitBranch, "title": t.Meta.Title, "message_count": len(rows), "messages": rows}
	result, err := encodeObject(document)
	return finalizeRender(t, FormatHermes, result, err)
}

type CoworkCodec struct{}

func (CoworkCodec) Format() Format { return FormatCowork }
func (CoworkCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatCowork, DisplayName: "Claude Cowork", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

func (CoworkCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	body, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	header := object(body["header"])
	lines, err := encodeJSONLines(arrayToAny(body["transcript"]))
	if err != nil {
		return nil, err
	}
	parsed, err := (ClaudeCodeCodec{}).Parse(lines, opts)
	if err != nil {
		return nil, err
	}
	parsed.Transcript.Meta.ID = firstNonEmpty(stringValue(header["sessionId"]), parsed.Transcript.Meta.ID)
	parsed.Transcript.Meta.Timestamp = firstNonEmpty(timestampValue(header["createdAt"]), parsed.Transcript.Meta.Timestamp)
	parsed.Transcript.Meta.UpdatedAt = timestampValue(header["lastActivityAt"])
	parsed.Transcript.Meta.CWD = firstNonEmpty(stringValue(header["cwd"]), parsed.Transcript.Meta.CWD)
	parsed.Transcript.Meta.Title = firstNonEmpty(stringValue(header["title"]), parsed.Transcript.Meta.Title)
	parsed.Transcript.Meta.Model = firstNonEmpty(stringValue(header["model"]), parsed.Transcript.Meta.Model)
	if err := Validate(parsed.Transcript, opts.Limits); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (CoworkCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, t.Meta.ID)
	if !strings.HasPrefix(id, "local_") {
		id = "local_" + id
	}
	cliID := uuidFromSeed(id, "cli")
	copy := *t
	copy.Meta = t.Meta
	copy.Meta.ID = cliID
	native, err := (ClaudeCodeCodec{}).Render(&copy, RenderOptions{Limits: opts.Limits, ID: cliID})
	if err != nil {
		return nil, err
	}
	records, _, err := decodeJSONLines(native.Data, opts.Limits)
	if err != nil {
		return nil, err
	}
	last := t.Meta.Timestamp
	if len(t.Messages) > 0 {
		last = firstNonEmpty(t.Messages[len(t.Messages)-1].Timestamp, last)
	}
	body := map[string]any{"header": map[string]any{"sessionId": id, "cliSessionId": cliID, "processName": "claude", "cwd": t.Meta.CWD, "createdAt": epochMillis(t.Meta.Timestamp), "lastActivityAt": epochMillis(last), "model": t.Meta.Model, "title": t.Meta.Title, "isArchived": false}, "transcript": records, "audit": []any{}}
	result, err := encodeObject(body)
	return finalizeRender(t, FormatCowork, result, err)
}

type ClaudeChatCodec struct{}

func (ClaudeChatCodec) Format() Format { return FormatClaudeChat }
func (ClaudeChatCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatClaudeChat, DisplayName: "Claude Chat", Capability: Capability{Read: true, Remote: true, SourceOnly: true}}
}
func (ClaudeChatCodec) Render(*Transcript, RenderOptions) (*RenderResult, error) {
	return nil, ErrSourceOnly
}

func (ClaudeChatCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	document, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	if document["uuid"] == nil || document["chat_messages"] == nil {
		return nil, fmt.Errorf("%w: expected one live conversation detail object", ErrInvalidTranscript)
	}
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: stringValue(document["uuid"]), Timestamp: timestampValue(firstNonNil(document["created_at"], document["updated_at"])), Title: firstNonEmpty(stringValue(document["name"]), stringValue(document["summary"])), Model: modelValue(document["model"])}}
	messages := activeClaudeMessages(document)
	var pending []string
	var warnings []Warning
	for i, native := range messages {
		role := RoleUser
		sender := stringValue(native["sender"])
		if sender == "assistant" {
			role = RoleAssistant
		} else if sender != "human" && sender != "user" {
			continue
		}
		stamp := timestampValue(native["created_at"])
		content := native["content"]
		if content == nil && stringValue(native["text"]) != "" {
			content = stringValue(native["text"])
		}
		blocks, blockWarnings := parseAnthropicContent(content, i, &pending)
		warnings = append(warnings, blockWarnings...)
		if len(blocks) == 0 {
			continue
		}
		for _, block := range blocks {
			blockRole := role
			if block.Type == BlockThinking || block.Type == BlockToolUse {
				blockRole = RoleAssistant
			} else if block.Type == BlockToolResult {
				blockRole = RoleUser
			}
			t.Messages = appendBlockMessage(t.Messages, blockRole, block, stamp, firstNonEmpty(modelValue(native["model"]), t.Meta.Model))
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

type ChatGPTCodec struct{}

func (ChatGPTCodec) Format() Format { return FormatChatGPT }
func (ChatGPTCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatChatGPT, DisplayName: "ChatGPT", Capability: Capability{Read: true, Remote: true, SourceOnly: true}}
}
func (ChatGPTCodec) Render(*Transcript, RenderOptions) (*RenderResult, error) {
	return nil, ErrSourceOnly
}

func (ChatGPTCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	document, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	if object(document["mapping"]) == nil || firstNonEmpty(stringValue(document["conversation_id"]), stringValue(document["id"])) == "" {
		return nil, fmt.Errorf("%w: expected one live conversation detail object", ErrInvalidTranscript)
	}
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: firstNonEmpty(stringValue(document["conversation_id"]), stringValue(document["id"])), Timestamp: timestampValue(firstNonNil(document["create_time"], document["update_time"])), Title: stringValue(document["title"]), Model: stringValue(document["default_model_slug"])}}
	var warnings []Warning
	var lastTool string
	knownTools := map[string]bool{}
	for _, node := range activeChatGPTNodes(document) {
		message := object(node["message"])
		if len(message) == 0 || boolValue(object(message["metadata"])["is_visually_hidden_from_conversation"]) {
			continue
		}
		role := stringValue(object(message["author"])["role"])
		stamp := timestampValue(message["create_time"])
		if role == "tool" {
			id := firstNonEmpty(parentChatGPTToolID(document, node), lastTool)
			if !knownTools[id] {
				warnings = append(warnings, Warning{Code: "unpaired_tool_result", Message: "tool result omitted"})
				continue
			}
			content := chatGPTContent(message["content"])
			t.Messages = append(t.Messages, Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(content), IsError: boolValue(object(message["metadata"])["is_error"])}}, Timestamp: stamp})
			continue
		}
		if role != "user" && role != "assistant" {
			continue
		}
		metadata := object(message["metadata"])
		contentType := stringValue(object(message["content"])["content_type"])
		recipient := stringValue(message["recipient"])
		var block Block
		if role == "assistant" && recipient != "" && recipient != "all" && recipient != "assistant" {
			id := firstNonEmpty(stringValue(message["id"]), stableID("call_", t.Meta.ID, fmt.Sprint(len(t.Messages))))
			input := chatGPTContent(message["content"])
			if encoded, ok := input.(string); ok && json.Valid([]byte(encoded)) {
				var decoded any
				_ = json.Unmarshal([]byte(encoded), &decoded)
				input = decoded
			}
			block = Block{Type: BlockToolUse, ID: id, Name: recipient, Input: rawJSON(input)}
			lastTool, knownTools[id] = id, true
		} else if contentType == "thoughts" || contentType == "reasoning_recap" || stringValue(metadata["channel"]) == "commentary" || boolValue(metadata["is_reasoning"]) {
			text := fmt.Sprint(chatGPTContent(message["content"]))
			if text == "" {
				continue
			}
			block = Block{Type: BlockThinking, Text: text}
		} else {
			text := fmt.Sprint(chatGPTContent(message["content"]))
			if text == "" {
				continue
			}
			block = Block{Type: BlockText, Text: text}
		}
		canonicalRole := RoleUser
		if role == "assistant" {
			canonicalRole = RoleAssistant
		}
		model := firstNonEmpty(stringValue(metadata["model_slug"]), stringValue(metadata["resolved_model_slug"]), t.Meta.Model)
		t.Messages = append(t.Messages, Message{ID: stringValue(message["id"]), Role: canonicalRole, Content: []Block{block}, Timestamp: stamp, Model: model})
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

func decodeObject(data []byte, limits Limits) (map[string]any, error) {
	var object map[string]any
	if err := decodeJSONDocument(data, &object, limits); err != nil {
		return nil, err
	}
	return object, nil
}

func encodeObject(value any) (*RenderResult, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return &RenderResult{Data: append(data, '\n')}, nil
}

func looseContentBlocks(value any) []Block {
	if text, ok := value.(string); ok {
		if text == "" {
			return nil
		}
		return []Block{{Type: BlockText, Text: text}}
	}
	var blocks []Block
	for _, raw := range array(value) {
		part := object(raw)
		if stringValue(part["type"]) == "text" && stringValue(part["text"]) != "" {
			blocks = append(blocks, Block{Type: BlockText, Text: stringValue(part["text"])})
		} else if stringValue(part["type"]) == "image_url" {
			url := stringValue(object(part["image_url"])["url"])
			if strings.HasPrefix(url, "data:") {
				pieces := strings.SplitN(strings.TrimPrefix(url, "data:"), ";base64,", 2)
				if len(pieces) == 2 {
					blocks = append(blocks, Block{Type: BlockImage, Source: &MediaSource{Type: "base64", MediaType: pieces[0], Data: pieces[1]}})
				}
			}
		}
	}
	return blocks
}

func joinedBlocks(blocks []Block, kind BlockType) string {
	var values []string
	for _, block := range blocks {
		if block.Type == kind && block.Text != "" {
			values = append(values, block.Text)
		}
	}
	return strings.Join(values, "\n")
}

func arrayToAny(v any) []any { return array(v) }

func modelValue(v any) string {
	if value, ok := v.(string); ok {
		return value
	}
	return stringValue(object(v)["id"])
}

func appendBlockMessage(messages []Message, role Role, block Block, stamp, model string) []Message {
	if len(messages) > 0 && messages[len(messages)-1].Role == role && messages[len(messages)-1].Timestamp == stamp {
		messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, block)
		return messages
	}
	return append(messages, Message{Role: role, Content: []Block{block}, Timestamp: stamp, Model: model})
}

func activeClaudeMessages(document map[string]any) []map[string]any {
	raw := array(document["chat_messages"])
	byID := map[string]map[string]any{}
	for _, value := range raw {
		entry := object(value)
		byID[stringValue(entry["uuid"])] = entry
	}
	leaf := stringValue(document["current_leaf_message_uuid"])
	if leaf == "" {
		result := make([]map[string]any, 0, len(raw))
		for _, value := range raw {
			result = append(result, object(value))
		}
		return result
	}
	seen := map[string]bool{}
	var reverse []map[string]any
	for leaf != "" && !seen[leaf] {
		seen[leaf] = true
		entry := byID[leaf]
		if entry == nil {
			return activeClaudeMessages(map[string]any{"chat_messages": raw})
		}
		reverse = append(reverse, entry)
		leaf = stringValue(entry["parent_message_uuid"])
	}
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	return reverse
}

func activeChatGPTNodes(document map[string]any) []map[string]any {
	mapping := object(document["mapping"])
	current := stringValue(document["current_node"])
	seen := map[string]bool{}
	var reverse []map[string]any
	for current != "" && !seen[current] {
		seen[current] = true
		node := object(mapping[current])
		if node == nil {
			reverse = nil
			break
		}
		reverse = append(reverse, node)
		current = stringValue(node["parent"])
	}
	if len(reverse) == 0 {
		keys := make([]string, 0, len(mapping))
		for key := range mapping {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			reverse = append(reverse, object(mapping[key]))
		}
		return reverse
	}
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	return reverse
}

func parentChatGPTToolID(document map[string]any, node map[string]any) string {
	parent := object(object(document["mapping"])[stringValue(node["parent"])])
	message := object(parent["message"])
	recipient := stringValue(message["recipient"])
	if recipient == "" || recipient == "all" || recipient == "assistant" {
		return ""
	}
	return stringValue(message["id"])
}

func chatGPTContent(value any) any {
	content := object(value)
	if text := stringValue(content["text"]); text != "" {
		return text
	}
	parts := array(content["parts"])
	if len(parts) == 1 {
		return parts[0]
	}
	var stringsOnly []string
	for _, part := range parts {
		if text, ok := part.(string); ok {
			stringsOnly = append(stringsOnly, text)
		} else {
			return parts
		}
	}
	return strings.Join(stringsOnly, "\n")
}

func init() {
	_ = Register(HermesCodec{})
	_ = Register(CoworkCodec{})
	_ = Register(ClaudeChatCodec{})
	_ = Register(ChatGPTCodec{})
}
