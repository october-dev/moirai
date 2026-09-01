package moirai

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func decodeJSONLines(data []byte, limits Limits) ([]map[string]any, []Warning, error) {
	limits = limits.normalized()
	if int64(len(data)) > limits.MaxInputBytes {
		return nil, nil, ErrLimitExceeded
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), int(min(limits.MaxInputBytes, int64(^uint(0)>>1))))
	var records []map[string]any
	var warnings []Warning
	line := 0
	for scanner.Scan() {
		line++
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var record map[string]any
		if err := decodeJSONDocument(scanner.Bytes(), &record, limits); err != nil {
			if err == ErrLimitExceeded {
				return nil, warnings, err
			}
			warnings = append(warnings, Warning{Path: fmt.Sprintf("line %d", line), Code: "invalid_json", Message: "record omitted"})
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("%w: jsonl: %v", ErrInvalidTranscript, err)
	}
	return records, warnings, nil
}

func decodeJSONDocument(data []byte, target any, limits Limits) error {
	limits = limits.normalized()
	if int64(len(data)) > limits.MaxInputBytes {
		return ErrLimitExceeded
	}
	if err := checkJSONDepth(data, limits.MaxNestingDepth); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTranscript, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON documents", ErrInvalidTranscript)
		}
		return fmt.Errorf("%w: %v", ErrInvalidTranscript, err)
	}
	return nil
}

func checkJSONDepth(data []byte, maximum int) error {
	if maximum <= 0 {
		maximum = DefaultLimits().MaxNestingDepth
	}
	depth := 0
	inString := false
	escaped := false
	for _, character := range data {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maximum {
				return ErrLimitExceeded
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return nil
}

func checkJSONDepthAt(data []byte, maximum, structuralDepth int) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	allowed := maximum - structuralDepth
	if allowed <= 0 {
		return ErrLimitExceeded
	}
	return checkJSONDepth(data, allowed)
}

func encodeJSONLines(records []any) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	for _, record := range records {
		if err := enc.Encode(record); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func finalizeRender(t *Transcript, format Format, result *RenderResult, err error) (*RenderResult, error) {
	if err != nil || result == nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, renderLossWarnings(t, format)...)
	return result, nil
}

func renderLossWarnings(t *Transcript, format Format) []Warning {
	if t == nil || format == FormatSimple {
		return nil
	}
	var warnings []Warning
	for messageIndex, message := range t.Messages {
		for blockIndex, block := range message.Content {
			if renderSupportsBlock(format, message.Role, block) {
				continue
			}
			warnings = append(warnings, Warning{
				Path:    fmt.Sprintf("messages[%d].content[%d]", messageIndex, blockIndex),
				Code:    "unsupported_block",
				Message: fmt.Sprintf("%s cannot represent %s content; block omitted", format, block.Type),
			})
		}
	}
	return warnings
}

func renderSupportsBlock(format Format, role Role, block Block) bool {
	switch block.Type {
	case BlockArtifact, BlockUnknown:
		return false
	case BlockImage:
		switch format {
		case FormatClaudeCode, FormatCowork, FormatPi, FormatCampfire, FormatAmp:
			return true
		case FormatCodex:
			return block.Source != nil && block.Source.URL != ""
		case FormatOpenCode, FormatAntigravity:
			return role == RoleUser
		default:
			return false
		}
	case BlockThinking:
		return role == RoleAssistant && format != FormatFX
	case BlockToolUse:
		return role == RoleAssistant
	case BlockToolResult:
		return role == RoleUser
	case BlockText:
		return true
	default:
		return false
	}
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func array(v any) []any {
	a, _ := v.([]any)
	return a
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func integerValue(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	default:
		return 0
	}
}

func timestampValue(v any) string {
	switch value := v.(type) {
	case string:
		if value == "" {
			return ""
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			return unixTimestamp(n)
		}
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return unixTimestamp(n)
		}
	case float64:
		seconds, fraction := mathModf(value)
		if value > 1e11 {
			return time.UnixMilli(int64(value)).UTC().Format(time.RFC3339Nano)
		}
		return time.Unix(int64(seconds), int64(fraction*1e9)).UTC().Format(time.RFC3339Nano)
	}
	return ""
}

func mathModf(v float64) (float64, float64) {
	i := float64(int64(v))
	return i, v - i
}

func unixTimestamp(n int64) string {
	if n > 100_000_000_000 {
		return time.UnixMilli(n).UTC().Format(time.RFC3339Nano)
	}
	return time.Unix(n, 0).UTC().Format(time.RFC3339Nano)
}

func epochMillis(stamp string) int64 {
	if parsed, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		return parsed.UnixMilli()
	}
	return time.Now().UTC().UnixMilli()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stableID(prefix string, values ...string) string {
	h := sha256.New()
	for _, value := range values {
		h.Write([]byte{0})
		h.Write([]byte(value))
	}
	return prefix + hex.EncodeToString(h.Sum(nil)[:8])
}

func uuidFromSeed(values ...string) string {
	raw := strings.TrimPrefix(stableID("", values...), "")
	more := strings.TrimPrefix(stableID("", append(values, "uuid")...), "")
	raw += more
	raw = raw[:32]
	return raw[:8] + "-" + raw[8:12] + "-4" + raw[13:16] + "-a" + raw[17:20] + "-" + raw[20:32]
}

func rawJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

func normalizeTranscript(t *Transcript, sourceID, stamp string) error {
	if t.SchemaVersion == "" {
		t.SchemaVersion = SchemaVersion
	}
	if t.Meta.ID == "" {
		t.Meta.ID = sourceID
	}
	if t.Meta.ID == "" {
		id, err := NewID()
		if err != nil {
			return err
		}
		t.Meta.ID = id
	}
	if t.Meta.Timestamp == "" {
		t.Meta.Timestamp = firstNonEmpty(stamp, nowRFC3339())
	}
	last := t.Meta.Timestamp
	for i := range t.Messages {
		if t.Messages[i].Timestamp == "" {
			t.Messages[i].Timestamp = last
		} else {
			last = t.Messages[i].Timestamp
		}
	}
	return nil
}

func parseAnthropicContent(v any, messageIndex int, pending *[]string) ([]Block, []Warning) {
	if text, ok := v.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []Block{{Type: BlockText, Text: text}}, nil
	}
	var blocks []Block
	var warnings []Warning
	for bi, item := range array(v) {
		entry := object(item)
		kind := stringValue(entry["type"])
		var block Block
		switch kind {
		case "text", "input_text", "output_text":
			block = Block{Type: BlockText, Text: firstNonEmpty(stringValue(entry["text"]), stringValue(entry["content"]))}
		case "thinking", "reasoning", "summary_text":
			block = Block{Type: BlockThinking, Text: firstNonEmpty(stringValue(entry["thinking"]), stringValue(entry["text"])), Signature: stringValue(entry["signature"]), Encrypted: firstNonEmpty(stringValue(entry["encrypted"]), stringValue(entry["encrypted_content"]))}
		case "tool_use", "toolCall", "function_call", "custom_tool_call":
			id := firstNonEmpty(stringValue(entry["id"]), stringValue(entry["call_id"]), stringValue(entry["callID"]))
			if id == "" {
				id = syntheticToolID(messageIndex, bi)
			}
			input := entry["input"]
			if input == nil {
				input = entry["arguments"]
			}
			if args, ok := input.(string); ok && json.Valid([]byte(args)) {
				var decoded any
				_ = json.Unmarshal([]byte(args), &decoded)
				input = decoded
			}
			block = Block{Type: BlockToolUse, ID: id, Name: firstNonEmpty(stringValue(entry["name"]), stringValue(entry["tool"])), Input: rawJSON(input)}
			*pending = append(*pending, id)
		case "tool_result", "toolResult", "function_call_output", "custom_tool_call_output":
			id := firstNonEmpty(stringValue(entry["tool_use_id"]), stringValue(entry["toolUseID"]), stringValue(entry["toolCallId"]), stringValue(entry["call_id"]), stringValue(entry["callID"]))
			if id == "" && len(*pending) > 0 {
				id = (*pending)[0]
				*pending = (*pending)[1:]
			}
			content := entry["content"]
			if content == nil {
				content = firstNonNil(entry["output"], entry["result"])
			}
			block = Block{Type: BlockToolResult, ToolUseID: id, Content: rawJSON(content), IsError: boolValue(entry["is_error"]) || boolValue(entry["isError"])}
		case "image", "input_image":
			source := object(entry["source"])
			block = Block{Type: BlockImage, Source: &MediaSource{Type: firstNonEmpty(stringValue(source["type"]), "base64"), MediaType: firstNonEmpty(stringValue(source["media_type"]), stringValue(source["mediaType"])), Data: stringValue(source["data"]), URL: firstNonEmpty(stringValue(source["url"]), stringValue(entry["image_url"]))}}
		default:
			warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d].content[%d]", messageIndex, bi), Code: "unknown_block", Message: "block omitted"})
			continue
		}
		if block.Type == BlockText && block.Text == "" || block.Type == BlockThinking && block.Text == "" && block.Encrypted == "" || block.Type == BlockToolUse && block.Name == "" {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d].content[%d]", messageIndex, bi), Code: "invalid_block", Message: "block omitted"})
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks, warnings
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func renderAnthropicContent(blocks []Block) []any {
	result := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			result = append(result, map[string]any{"type": "text", "text": block.Text})
		case BlockThinking:
			item := map[string]any{"type": "thinking", "thinking": block.Text}
			if block.Signature != "" {
				item["signature"] = block.Signature
			}
			if block.Encrypted != "" {
				item["encrypted"] = block.Encrypted
			}
			result = append(result, item)
		case BlockToolUse:
			var input any
			if len(block.Input) > 0 {
				_ = json.Unmarshal(block.Input, &input)
			}
			result = append(result, map[string]any{"type": "tool_use", "id": block.ID, "name": block.Name, "input": input})
		case BlockToolResult:
			var content any
			if len(block.Content) > 0 {
				_ = json.Unmarshal(block.Content, &content)
			}
			result = append(result, map[string]any{"type": "tool_result", "tool_use_id": block.ToolUseID, "content": content, "is_error": block.IsError})
		case BlockImage:
			if block.Source != nil {
				result = append(result, map[string]any{"type": "image", "source": block.Source})
			}
		}
	}
	return result
}

func usageFromMap(v any, inputKeys, outputKeys []string) *Usage {
	m := object(v)
	if len(m) == 0 {
		return nil
	}
	u := &Usage{}
	for _, key := range inputKeys {
		if u.InputTokens == 0 {
			u.InputTokens = integerValue(m[key])
		}
	}
	for _, key := range outputKeys {
		if u.OutputTokens == 0 {
			u.OutputTokens = integerValue(m[key])
		}
	}
	u.CacheReadInputTokens = integerValue(firstNonNil(m["cache_read_input_tokens"], m["cacheReadInputTokens"], m["cacheRead"], object(m["cache"])["read"]))
	u.CacheCreationInputTokens = integerValue(firstNonNil(m["cache_creation_input_tokens"], m["cacheCreationInputTokens"], m["cacheWrite"], object(m["cache"])["write"]))
	return u
}

func decodeFileURI(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" {
		return value
	}
	path, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return parsed.Path
	}
	return path
}
