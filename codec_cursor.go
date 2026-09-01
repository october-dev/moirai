package moirai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type CursorCodec struct{}

func (CursorCodec) Format() Format { return FormatCursor }
func (CursorCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatCursor, DisplayName: "Cursor Agent", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true, Continue: true}}
}

func (CursorCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	body, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	metaValue := cursorMetaValue(body)
	sessionMeta := object(body["session_meta"])
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: stringValue(metaValue["agentId"]), Timestamp: timestampValue(firstNonNil(sessionMeta["createdAtMs"], metaValue["createdAt"])), UpdatedAt: timestampValue(sessionMeta["updatedAtMs"]), CWD: stringValue(metaValue["workspacePath"]), Title: firstNonEmpty(stringValue(sessionMeta["title"]), stringValue(metaValue["name"])), Model: stringValue(metaValue["lastUsedModel"])}}
	known := map[string]bool{}
	var warnings []Warning
	for bi, rawBlob := range array(body["blobs"]) {
		blob := object(rawBlob)
		decoded, err := hex.DecodeString(stringValue(blob["data"]))
		if err != nil {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("blobs[%d]", bi), Code: "invalid_blob", Message: "blob omitted"})
			continue
		}
		var message map[string]any
		if json.Unmarshal(decoded, &message) != nil {
			continue
		}
		stamp := timestampValue(firstNonNil(message["createdAt"], message["timestamp"]))
		switch stringValue(message["role"]) {
		case "user":
			var blocks []Block
			if text, ok := message["content"].(string); ok && text != "" {
				blocks = append(blocks, Block{Type: BlockText, Text: stripCursorPrompt(text)})
			} else {
				for _, raw := range array(message["content"]) {
					part := object(raw)
					if stringValue(part["type"]) == "text" && stringValue(part["text"]) != "" {
						blocks = append(blocks, Block{Type: BlockText, Text: stripCursorPrompt(stringValue(part["text"]))})
					}
				}
			}
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{Role: RoleUser, Content: blocks, Timestamp: stamp})
			}
		case "assistant":
			var blocks []Block
			for pi, raw := range array(message["content"]) {
				part := object(raw)
				switch stringValue(part["type"]) {
				case "text":
					if text := stringValue(part["text"]); text != "" {
						blocks = append(blocks, Block{Type: BlockText, Text: text})
					}
				case "reasoning", "thinking":
					blocks = append(blocks, Block{Type: BlockThinking, Text: firstNonEmpty(stringValue(part["text"]), stringValue(part["thinking"])), Signature: stringValue(part["signature"]), Encrypted: stringValue(part["data"])})
				case "redacted-reasoning":
					blocks = append(blocks, Block{Type: BlockThinking, Encrypted: stringValue(part["data"])})
				case "tool-call":
					id := firstNonEmpty(stringValue(part["toolCallId"]), syntheticToolID(bi, pi))
					blocks = append(blocks, Block{Type: BlockToolUse, ID: id, Name: stringValue(part["toolName"]), Input: rawJSON(part["args"])})
					known[id] = true
				}
			}
			if len(blocks) > 0 {
				model := firstNonEmpty(stringValue(object(object(message["providerOptions"])["cursor"])["modelName"]), t.Meta.Model)
				t.Messages = append(t.Messages, Message{Role: RoleAssistant, Content: blocks, Timestamp: stamp, Model: model})
			}
		case "tool":
			var blocks []Block
			for pi, raw := range array(message["content"]) {
				part := object(raw)
				if stringValue(part["type"]) != "tool-result" {
					continue
				}
				id := stringValue(part["toolCallId"])
				if !known[id] {
					warnings = append(warnings, Warning{Path: fmt.Sprintf("blobs[%d].content[%d]", bi, pi), Code: "unpaired_tool_result", Message: "result omitted"})
					continue
				}
				blocks = append(blocks, Block{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(part["result"]), IsError: boolValue(message["isError"]) || boolValue(part["isError"])})
			}
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{Role: RoleUser, Content: blocks, Timestamp: stamp})
			}
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

func cursorMetaValue(body map[string]any) map[string]any {
	for _, raw := range array(body["meta"]) {
		entry := object(raw)
		if stringValue(entry["key"]) != "0" {
			continue
		}
		value := stringValue(entry["value"])
		var result map[string]any
		if json.Unmarshal([]byte(value), &result) == nil {
			return result
		}
		if decoded, err := hex.DecodeString(value); err == nil && json.Unmarshal(decoded, &result) == nil {
			return result
		}
	}
	return map[string]any{}
}

func stripCursorPrompt(text string) string {
	if start := strings.Index(text, "<user_query>"); start >= 0 {
		text = text[start+len("<user_query>"):]
		if end := strings.Index(text, "</user_query>"); end >= 0 {
			text = text[:end]
		}
	}
	return strings.TrimSpace(text)
}

func (CursorCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, t.Meta.ID)
	var blobs []any
	toolNames := map[string]string{}
	for _, message := range t.Messages {
		var native map[string]any
		if message.Role == RoleUser {
			var content []any
			for _, block := range message.Content {
				if block.Type == BlockToolResult {
					var result any
					_ = json.Unmarshal(block.Content, &result)
					native = map[string]any{"role": "tool", "content": []any{map[string]any{"type": "tool-result", "toolCallId": block.ToolUseID, "toolName": firstNonEmpty(toolNames[block.ToolUseID], "tool"), "result": result}}, "createdAt": epochMillis(firstNonEmpty(message.Timestamp, t.Meta.Timestamp)), "isError": block.IsError}
					appendCursorBlob(&blobs, native)
					continue
				}
				if block.Type == BlockText {
					content = append(content, map[string]any{"type": "text", "text": block.Text})
				}
			}
			if len(content) > 0 {
				native = map[string]any{"role": "user", "content": content, "createdAt": epochMillis(firstNonEmpty(message.Timestamp, t.Meta.Timestamp))}
				appendCursorBlob(&blobs, native)
			}
			continue
		}
		var content []any
		for _, block := range message.Content {
			switch block.Type {
			case BlockText:
				content = append(content, map[string]any{"type": "text", "text": block.Text})
			case BlockThinking:
				if block.Encrypted != "" {
					content = append(content, map[string]any{"type": "redacted-reasoning", "data": block.Encrypted})
				} else {
					content = append(content, map[string]any{"type": "reasoning", "text": block.Text, "signature": block.Signature})
				}
			case BlockToolUse:
				var input any
				_ = json.Unmarshal(block.Input, &input)
				content = append(content, map[string]any{"type": "tool-call", "toolCallId": block.ID, "toolName": block.Name, "args": input})
				toolNames[block.ID] = block.Name
			}
		}
		if len(content) > 0 {
			native = map[string]any{"role": "assistant", "content": content, "createdAt": epochMillis(firstNonEmpty(message.Timestamp, t.Meta.Timestamp)), "providerOptions": map[string]any{"cursor": map[string]any{"modelName": firstNonEmpty(message.Model, t.Meta.Model)}}}
			appendCursorBlob(&blobs, native)
		}
	}
	graph := cursorGraphBlobs(t, id)
	blobs = append(blobs, graph...)
	latest := ""
	if len(blobs) > 0 {
		latest = stringValue(object(blobs[len(blobs)-1])["id"])
	}
	meta := map[string]any{"agentId": id, "latestRootBlobId": latest, "name": firstNonEmpty(t.Meta.Title, "Imported Session"), "createdAt": epochMillis(t.Meta.Timestamp), "mode": "default", "workspacePath": t.Meta.CWD}
	if t.Meta.Model != "" {
		meta["lastUsedModel"] = t.Meta.Model
	}
	metaBytes, _ := json.Marshal(meta)
	body := map[string]any{"blobs": blobs, "meta": []any{map[string]any{"key": "0", "value": hex.EncodeToString(metaBytes)}}, "session_meta": map[string]any{"schemaVersion": 1, "createdAtMs": epochMillis(t.Meta.Timestamp), "hasConversation": len(blobs) > 0, "title": t.Meta.Title, "updatedAtMs": epochMillis(firstNonEmpty(t.Meta.UpdatedAt, t.Meta.Timestamp))}}
	result, err := encodeObject(body)
	return finalizeRender(t, FormatCursor, result, err)
}

func appendCursorBlob(blobs *[]any, value any) {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	*blobs = append(*blobs, map[string]any{"id": hex.EncodeToString(digest[:]), "data": hex.EncodeToString(data)})
}

func appendCursorBinary(blobs *[]any, data []byte) [32]byte {
	digest := sha256.Sum256(data)
	*blobs = append(*blobs, map[string]any{"id": hex.EncodeToString(digest[:]), "data": hex.EncodeToString(data)})
	return digest
}

func cursorGraphBlobs(t *Transcript, sessionID string) []any {
	var blobs []any
	type graphTurn struct {
		prompt string
		steps  []string
	}
	var turns []graphTurn
	var current *graphTurn
	for _, message := range t.Messages {
		if message.Role == RoleUser && hasBlockType(message, BlockText) {
			turns = append(turns, graphTurn{prompt: joinedBlocks(message.Content, BlockText)})
			current = &turns[len(turns)-1]
			continue
		}
		if current == nil {
			turns = append(turns, graphTurn{prompt: firstNonEmpty(t.Meta.Title, "Continue.")})
			current = &turns[len(turns)-1]
		}
		for _, block := range message.Content {
			switch block.Type {
			case BlockText:
				current.steps = append(current.steps, block.Text)
			case BlockThinking:
				current.steps = append(current.steps, "Reasoning:\n"+block.Text)
			case BlockToolUse:
				current.steps = append(current.steps, fmt.Sprintf("Tool call %s (%s):\n%s", block.Name, block.ID, string(block.Input)))
			case BlockToolResult:
				current.steps = append(current.steps, fmt.Sprintf("Tool result for %s:\n%s", block.ToolUseID, string(block.Content)))
			}
		}
	}
	var turnIDs [][32]byte
	for i, turn := range turns {
		var user []byte
		protoWriteString(&user, 1, turn.prompt)
		protoWriteString(&user, 2, uuidFromSeed(sessionID, fmt.Sprint(i), turn.prompt))
		protoWriteUint(&user, 4, 1)
		userID := appendCursorBinary(&blobs, user)
		var stepIDs [][32]byte
		for _, text := range turn.steps {
			if strings.TrimSpace(text) == "" {
				continue
			}
			var assistant []byte
			protoWriteString(&assistant, 1, text)
			var step []byte
			protoWriteBytes(&step, 1, assistant)
			stepIDs = append(stepIDs, appendCursorBinary(&blobs, step))
		}
		var agentTurn []byte
		protoWriteBytes(&agentTurn, 1, userID[:])
		for _, stepID := range stepIDs {
			protoWriteBytes(&agentTurn, 2, stepID[:])
		}
		var turnData []byte
		protoWriteBytes(&turnData, 1, agentTurn)
		turnIDs = append(turnIDs, appendCursorBinary(&blobs, turnData))
	}
	var root []byte
	for _, turnID := range turnIDs {
		protoWriteBytes(&root, 8, turnID[:])
	}
	protoWriteUint(&root, 10, 1)
	protoWriteUint(&root, 26, uint64(epochMillis(t.Meta.Timestamp)))
	appendCursorBinary(&blobs, root)
	return blobs
}

type CursorDesktopCodec struct{}

func (CursorDesktopCodec) Format() Format { return FormatCursorDesktop }
func (CursorDesktopCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatCursorDesktop, DisplayName: "Cursor", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true}}
}

func (CursorDesktopCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	body, err := decodeObject(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	var header map[string]any
	_ = json.Unmarshal([]byte(stringValue(body["header"])), &header)
	var composer map[string]any
	_ = json.Unmarshal([]byte(stringValue(body["composer_data"])), &composer)
	repositoryPath := ""
	if repositories := array(header["trackedGitRepos"]); len(repositories) > 0 {
		repositoryPath = stringValue(object(repositories[0])["repoPath"])
	}
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: stringValue(header["composerId"]), Timestamp: timestampValue(body["created_at"]), UpdatedAt: timestampValue(body["last_updated_at"]), CWD: firstNonEmpty(stringValue(object(object(header["workspaceIdentifier"])["uri"])["fsPath"]), repositoryPath), Title: firstNonEmpty(stringValue(header["name"]), stringValue(header["subtitle"])), Model: stringValue(object(composer["modelConfig"])["modelName"])}}
	if t.Meta.Model == "default" {
		t.Meta.Model = ""
	}
	known := map[string]bool{}
	var warnings []Warning
	for i, rawRow := range array(body["bubbles"]) {
		row := object(rawRow)
		var bubble map[string]any
		if json.Unmarshal([]byte(stringValue(row["value"])), &bubble) != nil {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("bubbles[%d]", i), Code: "invalid_bubble", Message: "bubble omitted"})
			continue
		}
		stamp := timestampValue(bubble["createdAt"])
		switch integerValue(bubble["type"]) {
		case 1:
			if text := stringValue(bubble["text"]); text != "" {
				t.Messages = append(t.Messages, Message{ID: stringValue(bubble["bubbleId"]), Role: RoleUser, Content: []Block{{Type: BlockText, Text: text}}, Timestamp: stamp})
			}
		case 2:
			var blocks []Block
			if text := stringValue(object(bubble["thinking"])["text"]); text != "" {
				blocks = append(blocks, Block{Type: BlockThinking, Text: text, Signature: stringValue(object(bubble["thinking"])["signature"])})
			}
			if text := stringValue(bubble["text"]); text != "" {
				blocks = append(blocks, Block{Type: BlockText, Text: text})
			}
			call := object(bubble["toolFormerData"])
			if len(call) > 0 {
				id := sanitizeID(firstNonEmpty(stringValue(call["toolCallId"]), syntheticToolID(i, 0)))
				var input any
				if json.Unmarshal([]byte(stringValue(call["params"])), &input) != nil {
					input = map[string]any{}
				}
				blocks = append(blocks, Block{Type: BlockToolUse, ID: id, Name: firstNonEmpty(stringValue(call["name"]), "tool"), Input: rawJSON(input)})
				known[id] = true
				status := stringValue(call["status"])
				if status == "completed" || status == "error" {
					result := stringValue(call["result"])
					var output any = result
					if json.Valid([]byte(result)) {
						_ = json.Unmarshal([]byte(result), &output)
					}
					if len(blocks) > 0 {
						t.Messages = append(t.Messages, Message{ID: stringValue(bubble["bubbleId"]), Role: RoleAssistant, Content: blocks, Timestamp: stamp, Model: firstNonEmpty(stringValue(object(bubble["modelInfo"])["modelName"]), t.Meta.Model), Usage: usageFromMap(bubble["tokenCount"], []string{"inputTokens"}, []string{"outputTokens"})})
						blocks = nil
					}
					t.Messages = append(t.Messages, Message{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(output), IsError: status == "error"}}, Timestamp: stamp})
				}
			}
			if len(blocks) > 0 {
				t.Messages = append(t.Messages, Message{ID: stringValue(bubble["bubbleId"]), Role: RoleAssistant, Content: blocks, Timestamp: stamp, Model: firstNonEmpty(stringValue(object(bubble["modelInfo"])["modelName"]), t.Meta.Model), Usage: usageFromMap(bubble["tokenCount"], []string{"inputTokens"}, []string{"outputTokens"})})
			}
		}
	}
	_ = known
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

func sanitizeID(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, value)
}

func (CursorDesktopCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, t.Meta.ID)
	var bubbleValues []map[string]any
	pending := map[string]map[string]any{}
	for mi, message := range t.Messages {
		stamp := firstNonEmpty(message.Timestamp, t.Meta.Timestamp)
		if message.Role == RoleUser {
			for _, block := range message.Content {
				if block.Type == BlockToolResult {
					if call := pending[block.ToolUseID]; call != nil {
						var output any
						_ = json.Unmarshal(block.Content, &output)
						call["result"] = fmt.Sprint(output)
						if block.IsError {
							call["status"] = "error"
						} else {
							call["status"] = "completed"
						}
					}
				}
			}
			text := joinedBlocks(message.Content, BlockText)
			if text != "" {
				bubbleID := uuidFromSeed(id, fmt.Sprint(mi), "user")
				value := map[string]any{"_v": 3, "type": 1, "bubbleId": bubbleID, "createdAt": stamp, "text": text, "richText": "", "tokenCount": map[string]any{"inputTokens": 0, "outputTokens": 0}}
				bubbleValues = append(bubbleValues, value)
			}
			continue
		}
		for bi, block := range message.Content {
			bubbleID := uuidFromSeed(id, fmt.Sprint(mi), fmt.Sprint(bi))
			value := map[string]any{"_v": 3, "type": 2, "bubbleId": bubbleID, "createdAt": stamp, "text": "", "tokenCount": map[string]any{"inputTokens": 0, "outputTokens": 0}, "modelInfo": map[string]any{"modelName": firstNonEmpty(message.Model, t.Meta.Model, "default")}}
			switch block.Type {
			case BlockText:
				value["text"] = block.Text
			case BlockThinking:
				value["thinking"] = map[string]any{"text": block.Text, "signature": block.Signature}
				value["capabilityType"] = 30
			case BlockToolUse:
				var input any
				_ = json.Unmarshal(block.Input, &input)
				params, _ := json.Marshal(input)
				call := map[string]any{"name": block.Name, "toolCallId": block.ID, "params": string(params), "rawArgs": "", "status": "started", "result": ""}
				value["toolFormerData"] = call
				value["capabilityType"] = 15
				pending[block.ID] = call
			default:
				continue
			}
			if message.Usage != nil {
				value["tokenCount"] = map[string]any{"inputTokens": message.Usage.InputTokens, "outputTokens": message.Usage.OutputTokens}
			}
			bubbleValues = append(bubbleValues, value)
		}
	}
	var bubbles []any
	for _, value := range bubbleValues {
		bubbleID := stringValue(value["bubbleId"])
		encoded, _ := json.Marshal(value)
		bubbles = append(bubbles, map[string]any{"key": fmt.Sprintf("bubbleId:%s:%s", id, bubbleID), "value": string(encoded)})
	}
	created := epochMillis(t.Meta.Timestamp)
	header := map[string]any{"type": "head", "composerId": id, "name": firstNonEmpty(t.Meta.Title, "Imported session"), "createdAt": created, "lastUpdatedAt": epochMillis(firstNonEmpty(t.Meta.UpdatedAt, t.Meta.Timestamp)), "unifiedMode": "agent", "forceMode": "edit", "isDraft": false, "isArchived": false, "workspaceIdentifier": map[string]any{"id": "", "uri": map[string]any{"fsPath": t.Meta.CWD, "external": "file://" + t.Meta.CWD, "path": t.Meta.CWD, "scheme": "file"}}}
	headerData, _ := json.Marshal(header)
	composer := map[string]any{"_v": 17, "composerId": id, "name": t.Meta.Title, "createdAt": created, "lastUpdatedAt": created, "modelConfig": map[string]any{"maxMode": false, "modelName": firstNonEmpty(t.Meta.Model, "default"), "selectedModels": []any{map[string]any{"modelId": firstNonEmpty(t.Meta.Model, "default"), "parameters": []any{}}}}, "fullConversationHeadersOnly": []any{}, "conversationMap": map[string]any{}, "conversationState": "~", "unifiedMode": "agent", "isAgentic": true, "isDraft": false, "hasLoaded": true}
	composerData, _ := json.Marshal(composer)
	body := map[string]any{"header": string(headerData), "created_at": created, "last_updated_at": created, "recency": created, "composer_data": string(composerData), "bubbles": bubbles}
	result, err := encodeObject(body)
	return finalizeRender(t, FormatCursorDesktop, result, err)
}

func init() {
	_ = Register(CursorCodec{})
	_ = Register(CursorDesktopCodec{})
}
