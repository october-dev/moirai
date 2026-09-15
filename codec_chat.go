package moirai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ChatCodec handles one OpenAI-style conversation, not a completion response
// or a multi-conversation database. It never executes or fetches attachments.
type ChatCodec struct{}

func (ChatCodec) Format() Format { return FormatChat }
func (ChatCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatChat, DisplayName: "Plain chat", Capability: Capability{Read: true, Write: true, Discover: true, Save: true, Delete: true}}
}

// Only representation details and unmapped fields live in extensions. Message
// text is never duplicated here, so range selection and canonical edits work.
type chatShape struct {
	Fields []string                           `json:"fields"`
	Rest   map[string]json.RawMessage         `json:"rest,omitempty"`
	Array  bool                               `json:"array,omitempty"`
	Parts  map[int]map[string]json.RawMessage `json:"parts,omitempty"`
	Usage  map[string]json.RawMessage         `json:"usage,omitempty"`
}

func chatExtra(s chatShape) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"chat": s})
	return b
}
func readChatShape(raw json.RawMessage) (chatShape, bool) {
	var e struct {
		Chat *chatShape `json:"chat"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Chat == nil {
		return chatShape{}, false
	}
	return *e.Chat, true
}
func chatObject(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, fmt.Errorf("%w: chat object required", ErrInvalidTranscript)
	}
	return m, nil
}
func takeChatString(m map[string]json.RawMessage, key string, dst *string, s *chatShape) error {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, dst) != nil {
		return fmt.Errorf("%w: %s must be a string", ErrInvalidTranscript, key)
	}
	s.Fields = append(s.Fields, key)
	delete(m, key)
	return nil
}
func chatSet(m map[string]json.RawMessage, key string, value any) { m[key], _ = json.Marshal(value) }
func chatString(m map[string]json.RawMessage, key, value string, s chatShape) {
	if value != "" {
		chatSet(m, key, value)
		return
	}
	for _, k := range s.Fields {
		if k == key {
			chatSet(m, key, value)
			return
		}
	}
}
func chatRest(s chatShape) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for k, v := range s.Rest {
		m[k] = v
	}
	return m
}

func (ChatCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	var doc map[string]json.RawMessage
	if err := decodeJSONDocument(data, &doc, opts.Limits); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, ErrInvalidTranscript
	}
	t := &Transcript{SchemaVersion: ChatSchemaVersion, Messages: []Message{}}
	s := chatShape{Fields: []string{}}
	for _, f := range []struct {
		k string
		p *string
	}{{"id", &t.Meta.ID}, {"title", &t.Meta.Title}, {"model", &t.Meta.Model}, {"provider", &t.Meta.ModelProvider}, {"created_at", &t.Meta.Timestamp}, {"updated_at", &t.Meta.UpdatedAt}} {
		if err := takeChatString(doc, f.k, f.p, &s); err != nil {
			return nil, err
		}
	}
	if raw, ok := doc["schema_version"]; ok && len(raw) > 0 {
		return nil, fmt.Errorf("%w: canonical documents use the simple codec", ErrInvalidTranscript)
	}
	if t.Meta.ID == "" {
		for _, field := range s.Fields {
			if field == "id" {
				return nil, fmt.Errorf("%w: provided chat id must not be empty", ErrInvalidTranscript)
			}
		}
		t.Meta.ID = firstNonEmpty(opts.SourceID, stableID("chat", string(data)))
	}
	var messages []json.RawMessage
	if json.Unmarshal(doc["messages"], &messages) != nil || messages == nil {
		return nil, fmt.Errorf("%w: chat messages array required", ErrInvalidTranscript)
	}
	delete(doc, "messages")
	s.Rest = doc
	t.Meta.Extra = chatExtra(s)
	if len(messages) > opts.Limits.normalized().MaxMessages {
		return nil, ErrLimitExceeded
	}
	for i, raw := range messages {
		m, err := parseChatMessage(raw)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		t.Messages = append(t.Messages, m)
	}
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	return &ParseResult{Transcript: t}, nil
}

func parseChatMessage(raw json.RawMessage) (Message, error) {
	m := Message{Content: []Block{}}
	doc, err := chatObject(raw)
	if err != nil {
		return m, err
	}
	s := chatShape{Fields: []string{}}
	var role string
	for _, f := range []struct {
		k string
		p *string
	}{{"role", &role}, {"id", &m.ID}, {"model", &m.Model}, {"created_at", &m.Timestamp}} {
		if err := takeChatString(doc, f.k, f.p, &s); err != nil {
			return m, err
		}
	}
	m.Role = Role(role)
	if m.Role != RoleSystem && m.Role != RoleUser && m.Role != RoleAssistant {
		return m, fmt.Errorf("%w: unsupported chat role", ErrInvalidTranscript)
	}
	content, ok := doc["content"]
	if !ok || bytes.Equal(bytes.TrimSpace(content), []byte("null")) {
		return m, fmt.Errorf("%w: content is required", ErrInvalidTranscript)
	}
	delete(doc, "content")
	var text string
	if json.Unmarshal(content, &text) == nil {
		if text != "" {
			m.Content = append(m.Content, Block{Type: BlockText, Text: text})
		}
	} else {
		var parts []json.RawMessage
		if json.Unmarshal(content, &parts) != nil || parts == nil {
			return m, fmt.Errorf("%w: content must be text or an array", ErrInvalidTranscript)
		}
		s.Array = true
		s.Parts = map[int]map[string]json.RawMessage{}
		for i, part := range parts {
			p, err := chatObject(part)
			if err != nil {
				return m, err
			}
			var kind string
			_ = json.Unmarshal(p["type"], &kind)
			delete(p, "type")
			switch kind {
			case "text":
				if json.Unmarshal(p["text"], &text) != nil || text == "" {
					return m, fmt.Errorf("%w: content part text must be nonempty", ErrInvalidTranscript)
				}
				m.Content = append(m.Content, Block{Type: BlockText, Text: text})
				delete(p, "text")
			case "image_url":
				im, err := chatObject(p["image_url"])
				if err != nil {
					return m, err
				}
				var url string
				if json.Unmarshal(im["url"], &url) != nil {
					return m, ErrInvalidTranscript
				}
				m.Content = append(m.Content, Block{Type: BlockImage, Source: &MediaSource{Type: "url", URL: url}})
				delete(im, "url")
				chatSet(p, "image_url", im)
			default:
				return m, fmt.Errorf("%w: unsupported chat content part %q", ErrInvalidTranscript, kind)
			}
			s.Parts[i] = p
		}
	}
	if raw, ok := doc["usage"]; ok {
		u, err := chatObject(raw)
		if err != nil {
			return m, err
		}
		s.Usage = u
		m.Usage = &Usage{}
		for _, f := range []struct {
			k string
			p *int64
		}{{"prompt_tokens", &m.Usage.InputTokens}, {"completion_tokens", &m.Usage.OutputTokens}} {
			if v, ok := u[f.k]; ok {
				if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || json.Unmarshal(v, f.p) != nil || *f.p < 0 {
					return m, fmt.Errorf("%w: invalid usage", ErrInvalidTranscript)
				}
			}
		}
		delete(doc, "usage")
	}
	if raw, ok := doc["attachments"]; ok {
		var artifacts []Artifact
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&artifacts) != nil || artifacts == nil {
			return m, fmt.Errorf("%w: invalid attachments", ErrInvalidTranscript)
		}
		for _, a := range artifacts {
			a := a
			m.Content = append(m.Content, Block{Type: BlockArtifact, Artifact: &a})
		}
		s.Fields = append(s.Fields, "attachments")
		delete(doc, "attachments")
	}
	s.Rest = doc
	m.Extra = chatExtra(s)
	return m, nil
}

func (ChatCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	s, original := readChatShape(t.Meta.Extra)
	doc := chatRest(s)
	if !original {
		chatString(doc, "id", t.Meta.ID, s)
	} else {
		for _, k := range s.Fields {
			if k == "id" {
				chatString(doc, "id", t.Meta.ID, s)
			}
		}
	}
	for _, f := range []struct{ k, v string }{{"title", t.Meta.Title}, {"model", t.Meta.Model}, {"provider", t.Meta.ModelProvider}, {"created_at", t.Meta.Timestamp}, {"updated_at", t.Meta.UpdatedAt}} {
		chatString(doc, f.k, f.v, s)
	}
	var warnings []Warning
	if t.Meta.Model == "" {
		warnings = append(warnings, Warning{Path: "meta.model", Code: "missing_model", Message: "Source has no model; select one in the destination before continuing"})
	}
	for _, f := range []struct {
		k       string
		present bool
	}{{"cwd", t.Meta.CWD != ""}, {"git_branch", t.Meta.GitBranch != ""}, {"cli_version", t.Meta.CLIVersion != ""}, {"provenance", t.Meta.Provenance != nil}, {"extra", !original && len(t.Meta.Extra) > 0}} {
		if f.present {
			warnings = append(warnings, Warning{Path: "meta." + f.k, Code: "chat_metadata_omitted", Message: "Plain chat has no equivalent for this agent metadata"})
		}
	}
	if len(t.Extra) > 0 {
		warnings = append(warnings, Warning{Path: "extra", Code: "chat_metadata_omitted", Message: "Transcript extension omitted"})
	}
	messages := make([]map[string]json.RawMessage, 0, len(t.Messages))
	for i, m := range t.Messages {
		s, original := readChatShape(m.Extra)
		out := chatRest(s)
		chatSet(out, "role", m.Role)
		for _, f := range []struct{ k, v string }{{"id", m.ID}, {"model", m.Model}, {"created_at", m.Timestamp}} {
			chatString(out, f.k, f.v, s)
		}
		parts := []map[string]json.RawMessage{}
		artifacts := []Artifact{}
		texts := []string{}
		hasImage := false
		for j, b := range m.Content {
			p := map[string]json.RawMessage{}
			for k, v := range s.Parts[j] {
				p[k] = v
			}
			switch b.Type {
			case BlockText:
				chatSet(p, "type", "text")
				chatSet(p, "text", b.Text)
				texts = append(texts, b.Text)
			case BlockImage:
				if b.Source.Type != "url" {
					warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d].content[%d]", i, j), Code: "chat_block_omitted", Message: "Image is not a URL; no file is read or uploaded"})
					continue
				}
				hasImage = true
				chatSet(p, "type", "image_url")
				im, _ := chatObject(p["image_url"])
				if im == nil {
					im = map[string]json.RawMessage{}
				}
				chatSet(im, "url", b.Source.URL)
				chatSet(p, "image_url", im)
			case BlockArtifact:
				artifacts = append(artifacts, *b.Artifact)
				continue
			default:
				warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d].content[%d]", i, j), Code: "chat_block_omitted", Message: fmt.Sprintf("%s omitted from plain chat", b.Type)})
				continue
			}
			parts = append(parts, p)
		}
		if s.Array || hasImage || len(parts) > 1 {
			chatSet(out, "content", parts)
		} else {
			chatSet(out, "content", strings.Join(texts, ""))
		}
		if len(artifacts) > 0 {
			chatSet(out, "attachments", artifacts)
		} else {
			for _, k := range s.Fields {
				if k == "attachments" {
					chatSet(out, "attachments", artifacts)
				}
			}
		}
		if m.Usage != nil {
			u := map[string]json.RawMessage{}
			for k, v := range s.Usage {
				u[k] = v
			}
			if _, ok := u["prompt_tokens"]; ok || !original || m.Usage.InputTokens != 0 {
				chatSet(u, "prompt_tokens", m.Usage.InputTokens)
			}
			if _, ok := u["completion_tokens"]; ok || !original || m.Usage.OutputTokens != 0 {
				chatSet(u, "completion_tokens", m.Usage.OutputTokens)
			}
			chatSet(out, "usage", u)
			if m.Usage.CacheReadInputTokens != 0 || m.Usage.CacheCreationInputTokens != 0 {
				warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d].usage", i), Code: "chat_usage_omitted", Message: "Canonical cache-token counts have no generic chat equivalent"})
			}
		}
		if m.StopReason != "" || (!original && len(m.Extra) > 0) {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d]", i), Code: "chat_metadata_omitted", Message: "Stop reason or non-chat message extensions omitted"})
		}
		messages = append(messages, out)
	}
	chatSet(doc, "messages", messages)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > opts.Limits.normalized().MaxInputBytes {
		return nil, ErrLimitExceeded
	}
	return &RenderResult{Data: append(data, '\n'), Warnings: warnings}, nil
}

func init() { _ = Register(ChatCodec{}) }
