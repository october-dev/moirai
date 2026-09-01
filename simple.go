package moirai

import (
	"encoding/json"
	"fmt"
	"strings"
)

type SimpleCodec struct{}

func (SimpleCodec) Format() Format { return FormatSimple }
func (SimpleCodec) Info() HarnessInfo {
	return HarnessInfo{Format: FormatSimple, DisplayName: "Simple", Capability: Capability{Read: true, Write: true}}
}

type simpleDocument struct {
	SchemaVersion string          `json:"schema_version,omitempty"`
	Meta          *Metadata       `json:"meta,omitempty"`
	ID            string          `json:"id,omitempty"`
	Timestamp     string          `json:"timestamp,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	CWD           string          `json:"cwd,omitempty"`
	GitBranch     string          `json:"git_branch,omitempty"`
	Title         string          `json:"title,omitempty"`
	Model         string          `json:"model,omitempty"`
	CLIVersion    string          `json:"cli_version,omitempty"`
	Provenance    *Provenance     `json:"provenance,omitempty"`
	Messages      json.RawMessage `json:"messages"`
	Extra         json.RawMessage `json:"extra,omitempty"`
}

type simpleMessage struct {
	ID         string          `json:"id,omitempty"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Timestamp  string          `json:"timestamp,omitempty"`
	Model      string          `json:"model,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Extra      json.RawMessage `json:"extra,omitempty"`
}

func (SimpleCodec) Parse(data []byte, opts ParseOptions) (*ParseResult, error) {
	limits := opts.Limits.normalized()
	var doc simpleDocument
	if err := decodeJSONDocument(data, &doc, limits); err != nil {
		return nil, err
	}
	if doc.SchemaVersion != "" && doc.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedVersion, doc.SchemaVersion)
	}
	if len(doc.Messages) == 0 || string(doc.Messages) == "null" {
		return nil, fmt.Errorf("%w: messages array required", ErrInvalidTranscript)
	}
	var rawMessages []simpleMessage
	if err := json.Unmarshal(doc.Messages, &rawMessages); err != nil {
		return nil, fmt.Errorf("%w: messages: %v", ErrInvalidTranscript, err)
	}
	meta := Metadata{}
	if doc.Meta != nil {
		meta = *doc.Meta
	}
	id := firstNonEmpty(strings.TrimSpace(doc.ID), strings.TrimSpace(meta.ID))
	if id == "" {
		id = strings.TrimSpace(opts.SourceID)
	}
	if id == "" {
		var err error
		id, err = NewID()
		if err != nil {
			return nil, err
		}
	}
	stamp := firstNonEmpty(doc.Timestamp, meta.Timestamp)
	if stamp == "" {
		if opts.Now != nil {
			stamp = opts.Now()
		} else {
			stamp = nowRFC3339()
		}
	}
	meta.ID = id
	meta.Timestamp = stamp
	meta.UpdatedAt = firstNonEmpty(doc.UpdatedAt, meta.UpdatedAt)
	meta.CWD = firstNonEmpty(doc.CWD, meta.CWD)
	meta.GitBranch = firstNonEmpty(doc.GitBranch, meta.GitBranch)
	meta.Title = firstNonEmpty(doc.Title, meta.Title)
	meta.Model = firstNonEmpty(doc.Model, meta.Model)
	meta.CLIVersion = firstNonEmpty(doc.CLIVersion, meta.CLIVersion)
	if doc.Provenance != nil {
		meta.Provenance = doc.Provenance
	}
	t := &Transcript{SchemaVersion: SchemaVersion, Meta: meta, Extra: doc.Extra}
	warnings := make([]Warning, 0)
	pending := make([]string, 0)
	for i, raw := range rawMessages {
		role := Role(strings.ToLower(strings.TrimSpace(raw.Role)))
		if role != RoleUser && role != RoleAssistant {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d]", i), Code: "unknown_role", Message: "message omitted"})
			continue
		}
		blocks, blockWarnings := parseSimpleContent(raw.Content, i, &pending)
		warnings = append(warnings, blockWarnings...)
		if len(blocks) == 0 {
			continue
		}
		t.Messages = append(t.Messages, Message{ID: raw.ID, Role: role, Content: blocks, Timestamp: raw.Timestamp, Model: raw.Model, Usage: raw.Usage, StopReason: raw.StopReason, Extra: raw.Extra})
	}
	if err := Validate(t, limits); err != nil {
		return nil, err
	}
	return &ParseResult{Transcript: t, Warnings: warnings}, nil
}

func parseSimpleContent(raw json.RawMessage, messageIndex int, pending *[]string) ([]Block, []Warning) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []Block{{Type: BlockText, Text: text}}, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, []Warning{{Path: fmt.Sprintf("messages[%d].content", messageIndex), Code: "invalid_content", Message: "content omitted"}}
	}
	blocks := make([]Block, 0, len(entries))
	warnings := make([]Warning, 0)
	for bi, entry := range entries {
		var b Block
		if err := json.Unmarshal(entry, &b); err != nil || b.Type == "" {
			warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d].content[%d]", messageIndex, bi), Code: "invalid_block", Message: "block omitted"})
			continue
		}
		if b.Type == BlockToolUse {
			if b.ID == "" {
				b.ID = syntheticToolID(messageIndex, bi)
			}
			*pending = append(*pending, b.ID)
		}
		if b.Type == BlockToolResult && b.ToolUseID == "" && len(*pending) > 0 {
			b.ToolUseID = (*pending)[0]
			*pending = (*pending)[1:]
		}
		blocks = append(blocks, b)
	}
	return blocks, warnings
}

func syntheticToolID(messageIndex, blockIndex int) string {
	return fmt.Sprintf("tool-%d-%d", messageIndex+1, blockIndex+1)
}

func (SimpleCodec) Render(t *Transcript, opts RenderOptions) (*RenderResult, error) {
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	return &RenderResult{Data: data}, nil
}

func init() { _ = Register(SimpleCodec{}) }
