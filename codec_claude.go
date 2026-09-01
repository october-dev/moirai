package moirai

import "fmt"

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
	for _, record := range records {
		kind := stringValue(record["type"])
		if kind == "summary" {
			t.Meta.Title = firstNonEmpty(t.Meta.Title, stringValue(record["summary"]))
			continue
		}
		if kind != "user" && kind != "assistant" {
			continue
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
		payload := map[string]any{"role": message.Role, "content": renderAnthropicContent(message.Content)}
		if message.Role == RoleAssistant {
			payload["id"] = "msg_" + stableID("", sessionID, fmt.Sprint(i))
			payload["type"] = "message"
			payload["model"] = firstNonEmpty(message.Model, t.Meta.Model, "unknown")
			payload["stop_reason"] = firstNonEmpty(message.StopReason, "end_turn")
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

func init() { _ = Register(ClaudeCodeCodec{}) }
