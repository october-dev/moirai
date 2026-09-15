package moirai

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Matches Concord/Models/ChatModels.swift and its ISO8601 JSONEncoder.
type concordMessage struct {
	ID   string `json:"id"`
	Role Role   `json:"role"`
	Text string `json:"text"`
}
type concordConversation struct {
	ID         string           `json:"id"`
	ProviderID string           `json:"providerID"`
	Title      string           `json:"title"`
	Model      string           `json:"model"`
	Messages   []concordMessage `json:"messages"`
	CreatedAt  string           `json:"createdAt"`
	UpdatedAt  string           `json:"updatedAt"`
}
type ConcordCodec struct{}

func (ConcordCodec) Format() Format { return FormatConcord }
func (ConcordCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatConcord, DisplayName: "Concord", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Continue: runtime.GOOS == "darwin"}}
}
func concordUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil
}
func decodeConcord(data []byte, limits Limits) ([]concordConversation, bool, error) {
	if int64(len(data)) > limits.normalized().MaxInputBytes {
		return nil, false, ErrLimitExceeded
	}
	if err := checkJSONDepth(data, limits.normalized().MaxNestingDepth); err != nil {
		return nil, false, err
	}
	array := bytes.HasPrefix(bytes.TrimSpace(data), []byte("["))
	var raws []json.RawMessage
	if array {
		if err := decodeJSONDocument(data, &raws, limits); err != nil || raws == nil {
			return nil, array, ErrInvalidTranscript
		}
	} else {
		raws = []json.RawMessage{data}
	}
	if len(raws) > limits.normalized().MaxMessages {
		return nil, array, ErrLimitExceeded
	}
	items := make([]concordConversation, 0, len(raws))
	seen := map[string]bool{}
	for _, raw := range raws {
		obj, err := chatObject(raw)
		if err != nil {
			return nil, array, err
		}
		for _, k := range []string{"id", "providerID", "title", "model", "messages", "createdAt", "updatedAt"} {
			if v, ok := obj[k]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return nil, array, fmt.Errorf("%w: Concord requires %s", ErrInvalidTranscript, k)
			}
		}
		var c concordConversation
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, array, fmt.Errorf("%w: %v", ErrInvalidTranscript, err)
		}
		if !concordUUID(c.ID) || seen[strings.ToLower(c.ID)] {
			return nil, array, fmt.Errorf("%w: invalid or duplicate Concord ID", ErrInvalidTranscript)
		}
		seen[strings.ToLower(c.ID)] = true
		if c.Messages == nil {
			return nil, array, ErrInvalidTranscript
		}
		messageIDs := map[string]bool{}
		var rawMessages []map[string]json.RawMessage
		_ = json.Unmarshal(obj["messages"], &rawMessages)
		for i, m := range c.Messages {
			if !concordUUID(m.ID) || messageIDs[strings.ToLower(m.ID)] {
				return nil, array, fmt.Errorf("%w: invalid or duplicate message ID", ErrInvalidTranscript)
			}
			messageIDs[strings.ToLower(m.ID)] = true
			if v, ok := rawMessages[i]["text"]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return nil, array, ErrInvalidTranscript
			}
		}
		items = append(items, c)
	}
	return items, array, nil
}
func concordTranscript(c concordConversation, array bool, limits Limits) (*Transcript, error) {
	t := &Transcript{SchemaVersion: ChatSchemaVersion, Meta: Metadata{ID: c.ID, Title: c.Title, Model: c.Model, ModelProvider: c.ProviderID, Timestamp: c.CreatedAt, UpdatedAt: c.UpdatedAt}, Messages: []Message{}}
	t.Meta.Extra, _ = json.Marshal(map[string]any{"concord": map[string]bool{"array": array}})
	for _, m := range c.Messages {
		blocks := []Block{}
		if m.Text != "" {
			blocks = append(blocks, Block{Type: BlockText, Text: m.Text})
		}
		t.Messages = append(t.Messages, Message{ID: m.ID, Role: m.Role, Content: blocks})
	}
	if c.CreatedAt == "" || c.UpdatedAt == "" {
		return nil, ErrInvalidTranscript
	}
	return t, Validate(t, limits)
}
func (ConcordCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	items, array, err := decodeConcord(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	var selected *concordConversation
	if len(items) == 1 {
		selected = &items[0]
	} else if opts.SourceID != "" {
		for i := range items {
			if items[i].ID == opts.SourceID {
				selected = &items[i]
				break
			}
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("%w: select a Concord conversation with local discovery; a file may contain multiple chats", ErrInvalidTranscript)
	}
	t, err := concordTranscript(*selected, array, opts.Limits)
	if err != nil {
		return nil, err
	}
	return &ParseResult{Transcript: t}, nil
}
func (ConcordCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	var warnings []Warning
	warn := func(path, code, message string) {
		warnings = append(warnings, Warning{Path: path, Code: code, Message: message})
	}
	id := func(s, path string) string {
		if concordUUID(s) {
			return s
		}
		warn(path, "destination_id_created", "Created a deterministic UUID required by Concord; source ID is not representable")
		return uuidFromSeed("concord", t.Meta.ID, path, s)
	}
	now := ""
	date := func(s, path string) string {
		if s == "" {
			if now == "" {
				if opts.Now != nil {
					now = opts.Now()
				} else {
					now = nowRFC3339()
				}
			}
			s = now
			warn(path, "destination_time_created", "Concord requires a date; using import time, not a claimed source timestamp")
		}
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return ""
		}
		if parsed.Nanosecond() != 0 {
			warn(path, "timestamp_precision_reduced", "Concord ISO8601 storage uses whole seconds")
		}
		if parsed.Nanosecond() == 0 && !strings.ContainsAny(s, ".,") {
			return s
		}
		return parsed.Format(time.RFC3339)
	}
	c := concordConversation{ID: id(firstNonEmpty(opts.ID, t.Meta.ID), "meta.id"), ProviderID: t.Meta.ModelProvider, Title: t.Meta.Title, Model: t.Meta.Model, CreatedAt: date(t.Meta.Timestamp, "meta.timestamp"), UpdatedAt: date(t.Meta.UpdatedAt, "meta.updated_at"), Messages: []concordMessage{}}
	if c.CreatedAt == "" || c.UpdatedAt == "" {
		return nil, fmt.Errorf("%w: invalid import time", ErrInvalidTranscript)
	}
	if c.ProviderID == "" {
		warn("meta.model_provider", "provider_selection_required", "No source gateway is known; choose a provider in Concord's import dialog")
	}
	if c.Model == "" {
		warn("meta.model", "missing_model", "Select a model in Concord before sending a message")
	}
	for i, m := range t.Messages {
		var text strings.Builder
		for j, b := range m.Content {
			path := fmt.Sprintf("messages[%d].content[%d]", i, j)
			switch b.Type {
			case BlockText:
				text.WriteString(b.Text)
			case BlockImage:
				if b.Source.Type == "url" {
					fmt.Fprintf(&text, "\n![Image](%s)\n", b.Source.URL)
				} else if b.Source.Type == "base64" {
					fmt.Fprintf(&text, "\n![Image](data:%s;base64,%s)\n", b.Source.MediaType, b.Source.Data)
				} else {
					warn(path, "chat_block_omitted", "Concord cannot represent this image without reading or uploading a local file")
					continue
				}
				warn(path, "chat_block_flattened", "Image represented as Markdown text")
			case BlockThinking:
				fmt.Fprintf(&text, "\n[Reasoning]\n%s\n", b.Text)
				warn(path, "chat_block_flattened", "Reasoning flattened; signatures and encrypted payloads omitted")
			case BlockToolUse:
				fmt.Fprintf(&text, "\n[Tool call: %s]\n%s\n", b.Name, b.Input)
				warn(path, "chat_block_flattened", "Tool call is text, not an executable native call")
			case BlockToolResult:
				fmt.Fprintf(&text, "\n[Tool result: %s]\n%s\n", b.ToolUseID, b.Content)
				warn(path, "chat_block_flattened", "Tool result flattened; structured status omitted")
			default:
				warn(path, "chat_block_omitted", fmt.Sprintf("Concord cannot represent %s", b.Type))
			}
		}
		c.Messages = append(c.Messages, concordMessage{ID: id(m.ID, fmt.Sprintf("messages[%d].id", i)), Role: m.Role, Text: text.String()})
		if m.Timestamp != "" || m.Model != "" || m.Usage != nil || m.StopReason != "" || len(m.Extra) > 0 {
			warn(fmt.Sprintf("messages[%d]", i), "chat_metadata_omitted", "Concord has no per-message timestamps, model, usage, stop reason or extension fields")
		}
	}
	var shape struct {
		Concord *struct {
			Array bool `json:"array"`
		} `json:"concord"`
	}
	_ = json.Unmarshal(t.Meta.Extra, &shape)
	if t.Meta.CWD != "" || t.Meta.GitBranch != "" || t.Meta.CLIVersion != "" || t.Meta.Provenance != nil || len(t.Extra) > 0 || (shape.Concord == nil && len(t.Meta.Extra) > 0) {
		warn("meta", "chat_metadata_omitted", "Agent metadata and non-Concord extensions omitted")
	}
	var value any = c
	if shape.Concord != nil && shape.Concord.Array {
		value = []concordConversation{c}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > opts.Limits.normalized().MaxInputBytes {
		return nil, ErrLimitExceeded
	}
	return &RenderResult{Data: append(data, '\n'), Warnings: warnings}, nil
}
func init() { _ = Register(ConcordCodec{}) }
