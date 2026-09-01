package moirai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

func Validate(t *Transcript, limits Limits) error {
	if t == nil {
		return fmt.Errorf("%w: nil document", ErrInvalidTranscript)
	}
	limits = limits.normalized()
	if t.SchemaVersion == "" {
		return fmt.Errorf("%w: schema_version is required", ErrInvalidTranscript)
	}
	if t.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: %s", ErrUnsupportedVersion, t.SchemaVersion)
	}
	if strings.TrimSpace(t.Meta.ID) == "" {
		return fmt.Errorf("%w: meta.id is required", ErrInvalidTranscript)
	}
	if strings.IndexFunc(t.Meta.CWD, unsafeControlRune) >= 0 {
		return fmt.Errorf("%w: meta.cwd contains control characters", ErrInvalidTranscript)
	}
	metadataStrings := []string{t.Meta.ID, t.Meta.Timestamp, t.Meta.UpdatedAt, t.Meta.CWD, t.Meta.GitBranch, t.Meta.Title, t.Meta.Model, t.Meta.ModelProvider, t.Meta.CLIVersion}
	if t.Meta.Provenance != nil {
		metadataStrings = append(metadataStrings, string(t.Meta.Provenance.SourceFormat), t.Meta.Provenance.SourceSessionID, t.Meta.Provenance.ImportedAt, t.Meta.Provenance.ParentSessionID, t.Meta.Provenance.ParentCheckpoint, t.Meta.Provenance.SourceCWD)
	}
	var totalBytes int64
	for _, value := range metadataStrings {
		if len(value) > limits.MaxMetadataBytes {
			return fmt.Errorf("%w: metadata string", ErrLimitExceeded)
		}
		totalBytes += int64(len(value))
	}
	if len(t.Messages) > limits.MaxMessages {
		return fmt.Errorf("%w: message count", ErrLimitExceeded)
	}
	if err := validTime("meta.timestamp", t.Meta.Timestamp); err != nil {
		return err
	}
	if err := validTime("meta.updated_at", t.Meta.UpdatedAt); err != nil {
		return err
	}
	if len(t.Meta.Extra) > limits.MaxMetadataBytes || len(t.Extra) > limits.MaxMetadataBytes {
		return fmt.Errorf("%w: metadata", ErrLimitExceeded)
	}
	totalBytes += int64(len(t.Meta.Extra) + len(t.Extra))
	if err := checkJSONDepthAt(t.Meta.Extra, limits.MaxNestingDepth, 2); err != nil {
		return err
	}
	if err := checkJSONDepthAt(t.Extra, limits.MaxNestingDepth, 1); err != nil {
		return err
	}
	blocks := 0
	uses := map[string]string{}
	for mi := range t.Messages {
		m := &t.Messages[mi]
		if m.Role != RoleUser && m.Role != RoleAssistant {
			return fmt.Errorf("%w: messages[%d].role", ErrInvalidTranscript, mi)
		}
		if err := validTime(fmt.Sprintf("messages[%d].timestamp", mi), m.Timestamp); err != nil {
			return err
		}
		for _, value := range []string{m.ID, m.Timestamp, m.Model, m.StopReason} {
			if len(value) > limits.MaxMetadataBytes {
				return fmt.Errorf("%w: message metadata", ErrLimitExceeded)
			}
			totalBytes += int64(len(value))
		}
		if m.Usage != nil && (m.Usage.InputTokens < 0 || m.Usage.OutputTokens < 0 || m.Usage.CacheReadInputTokens < 0 || m.Usage.CacheCreationInputTokens < 0) {
			return fmt.Errorf("%w: messages[%d].usage", ErrInvalidTranscript, mi)
		}
		if len(m.Extra) > limits.MaxMetadataBytes {
			return fmt.Errorf("%w: message metadata", ErrLimitExceeded)
		}
		totalBytes += int64(len(m.Extra))
		if err := checkJSONDepthAt(m.Extra, limits.MaxNestingDepth, 3); err != nil {
			return err
		}
		blocks += len(m.Content)
		if blocks > limits.MaxBlocks {
			return fmt.Errorf("%w: block count", ErrLimitExceeded)
		}
		for bi := range m.Content {
			b := &m.Content[bi]
			path := fmt.Sprintf("messages[%d].content[%d]", mi, bi)
			if err := validateBlock(b, path, limits); err != nil {
				return err
			}
			totalBytes += blockPayloadBytes(b)
			if totalBytes > limits.MaxInputBytes {
				return fmt.Errorf("%w: aggregate transcript payload", ErrLimitExceeded)
			}
			if b.Type == BlockToolUse {
				if b.ID == "" {
					return fmt.Errorf("%w: %s.id is required", ErrInvalidTranscript, path)
				}
				if _, exists := uses[b.ID]; exists {
					return fmt.Errorf("%w: duplicate tool id %q", ErrInvalidTranscript, b.ID)
				}
				uses[b.ID] = path
			}
			if b.Type == BlockToolResult && b.ToolUseID != "" {
				if _, exists := uses[b.ToolUseID]; !exists {
					return fmt.Errorf("%w: %s references unknown tool %q", ErrInvalidTranscript, path, b.ToolUseID)
				}
			}
		}
	}
	return nil
}

func validTime(path, value string) error {
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("%w: %s must be RFC3339", ErrInvalidTranscript, path)
	}
	return nil
}

func validateBlock(b *Block, path string, limits Limits) error {
	if b == nil {
		return fmt.Errorf("%w: %s is null", ErrInvalidTranscript, path)
	}
	if !utf8.ValidString(b.Text) || len(b.Text) > limits.MaxTextBytes {
		return fmt.Errorf("%w: %s.text", ErrLimitExceeded, path)
	}
	switch b.Type {
	case BlockText, BlockThinking:
		if b.Text == "" && b.Encrypted == "" {
			return fmt.Errorf("%w: %s has no text", ErrInvalidTranscript, path)
		}
	case BlockToolUse:
		if len(b.ID) > limits.MaxMetadataBytes || len(b.Name) > limits.MaxMetadataBytes {
			return fmt.Errorf("%w: %s tool metadata", ErrLimitExceeded, path)
		}
		if strings.TrimSpace(b.Name) == "" {
			return fmt.Errorf("%w: %s.name is required", ErrInvalidTranscript, path)
		}
		if len(b.Input) > limits.MaxTextBytes || !validJSONOrEmpty(b.Input) {
			return fmt.Errorf("%w: %s.input", ErrInvalidTranscript, path)
		}
		if err := checkJSONDepthAt(b.Input, limits.MaxNestingDepth, 5); err != nil {
			return err
		}
	case BlockToolResult:
		if len(b.ToolUseID) > limits.MaxMetadataBytes {
			return fmt.Errorf("%w: %s.tool_use_id", ErrLimitExceeded, path)
		}
		if len(b.Content) > limits.MaxTextBytes || !validJSONOrEmpty(b.Content) {
			return fmt.Errorf("%w: %s.content", ErrInvalidTranscript, path)
		}
		if err := checkJSONDepthAt(b.Content, limits.MaxNestingDepth, 5); err != nil {
			return err
		}
	case BlockImage:
		if err := validateMediaSource(b.Source, path+".source", limits); err != nil {
			return err
		}
	case BlockArtifact:
		if b.Artifact == nil || strings.TrimSpace(b.Artifact.Name) == "" {
			return fmt.Errorf("%w: %s.artifact", ErrInvalidTranscript, path)
		}
		if len(b.Artifact.ID)+len(b.Artifact.Name)+len(b.Artifact.Description)+len(b.Artifact.MediaType)+len(b.Artifact.SHA256) > limits.MaxMetadataBytes {
			return fmt.Errorf("%w: %s.artifact", ErrLimitExceeded, path)
		}
		if b.Artifact.Source != nil {
			if err := validateMediaSource(b.Artifact.Source, path+".artifact.source", limits); err != nil {
				return err
			}
		}
		if b.Artifact.SHA256 != "" {
			if len(b.Artifact.SHA256) != 64 || strings.IndexFunc(b.Artifact.SHA256, func(r rune) bool {
				return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F')
			}) >= 0 {
				return fmt.Errorf("%w: %s.artifact.sha256", ErrInvalidTranscript, path)
			}
		}
	case BlockUnknown:
		if len(b.Data) == 0 || len(b.Data) > limits.MaxTextBytes || !json.Valid(b.Data) {
			return fmt.Errorf("%w: %s.data", ErrInvalidTranscript, path)
		}
		if err := checkJSONDepthAt(b.Data, limits.MaxNestingDepth, 5); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: %s.type %q", ErrInvalidTranscript, path, b.Type)
	}
	return nil
}

func validateMediaSource(source *MediaSource, path string, limits Limits) error {
	if source == nil || strings.TrimSpace(source.Type) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidTranscript, path)
	}
	switch source.Type {
	case "base64":
		if source.Data == "" {
			return fmt.Errorf("%w: %s.data is required", ErrInvalidTranscript, path)
		}
		decoded, err := base64.StdEncoding.DecodeString(source.Data)
		if err != nil {
			return fmt.Errorf("%w: %s.data", ErrInvalidTranscript, path)
		}
		if len(decoded) > limits.MaxInlineMediaBytes {
			return fmt.Errorf("%w: %s", ErrLimitExceeded, path)
		}
	case "path":
		if source.Path == "" || strings.IndexFunc(source.Path, unsafeControlRune) >= 0 {
			return fmt.Errorf("%w: %s.path", ErrInvalidTranscript, path)
		}
	case "url":
		parsed, err := url.ParseRequestURI(source.URL)
		if err != nil || parsed.Scheme == "" {
			return fmt.Errorf("%w: %s.url", ErrInvalidTranscript, path)
		}
	case "text":
		if source.Text == "" {
			return fmt.Errorf("%w: %s.text", ErrInvalidTranscript, path)
		}
	default:
		return fmt.Errorf("%w: %s.type", ErrInvalidTranscript, path)
	}
	return nil
}

func unsafeControlRune(r rune) bool { return r < 0x20 || r >= 0x7f && r <= 0x9f }

func blockPayloadBytes(block *Block) int64 {
	if block == nil {
		return 0
	}
	total := len(block.Text) + len(block.ID) + len(block.Name) + len(block.Input) + len(block.ToolUseID) + len(block.Content) + len(block.Data) + len(block.Signature) + len(block.Encrypted)
	if block.Source != nil {
		total += len(block.Source.Type) + len(block.Source.MediaType) + len(block.Source.Data) + len(block.Source.Path) + len(block.Source.URL) + len(block.Source.Text)
	}
	if block.Artifact != nil {
		total += len(block.Artifact.ID) + len(block.Artifact.Name) + len(block.Artifact.Description) + len(block.Artifact.MediaType) + len(block.Artifact.SHA256)
		if block.Artifact.Source != nil {
			total += len(block.Artifact.Source.Type) + len(block.Artifact.Source.MediaType) + len(block.Artifact.Source.Data) + len(block.Artifact.Source.Path) + len(block.Artifact.Source.URL) + len(block.Artifact.Source.Text)
		}
	}
	return int64(total)
}

func validJSONOrEmpty(v json.RawMessage) bool { return len(v) == 0 || json.Valid(v) }
