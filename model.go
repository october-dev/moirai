package moirai

import "encoding/json"

const SchemaVersion = "1.0"

type Format string

const (
	FormatSimple        Format = "simple"
	FormatClaudeCode    Format = "claude_code"
	FormatCodex         Format = "codex"
	FormatPi            Format = "pi"
	FormatAmp           Format = "amp"
	FormatOpenCode      Format = "opencode"
	FormatCursor        Format = "cursor"
	FormatCursorDesktop Format = "cursor_desktop"
	FormatGrok          Format = "grok"
	FormatHermes        Format = "hermes"
	FormatAntigravity   Format = "antigravity"
	FormatCampfire      Format = "campfire"
	FormatCowork        Format = "cowork"
	FormatFX            Format = "fx"
	FormatClaudeChat    Format = "claude_chat"
	FormatChatGPT       Format = "chatgpt"
)

var Formats = []Format{
	FormatSimple, FormatClaudeCode, FormatCodex, FormatPi, FormatAmp,
	FormatOpenCode, FormatCursor, FormatCursorDesktop, FormatGrok,
	FormatHermes, FormatAntigravity, FormatCampfire, FormatCowork,
	FormatFX, FormatClaudeChat, FormatChatGPT,
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	BlockImage      BlockType = "image"
	BlockArtifact   BlockType = "artifact"
	BlockUnknown    BlockType = "unknown"
)

type MediaSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	Path      string `json:"path,omitempty"`
	URL       string `json:"url,omitempty"`
	Text      string `json:"text,omitempty"`
}

type Artifact struct {
	ID          string       `json:"id,omitempty"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	MediaType   string       `json:"media_type,omitempty"`
	Source      *MediaSource `json:"source,omitempty"`
	SHA256      string       `json:"sha256,omitempty"`
}

type Block struct {
	Type      BlockType       `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Source    *MediaSource    `json:"source,omitempty"`
	Artifact  *Artifact       `json:"artifact,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Encrypted string          `json:"encrypted,omitempty"`
}

type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
}

type Message struct {
	ID         string          `json:"id,omitempty"`
	Role       Role            `json:"role"`
	Content    []Block         `json:"content"`
	Timestamp  string          `json:"timestamp,omitempty"`
	Model      string          `json:"model,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Extra      json.RawMessage `json:"extra,omitempty"`
}

type Provenance struct {
	SourceFormat     Format `json:"source_format,omitempty"`
	SourceSessionID  string `json:"source_session_id,omitempty"`
	ImportedAt       string `json:"imported_at,omitempty"`
	ParentSessionID  string `json:"parent_session_id,omitempty"`
	ParentCheckpoint string `json:"parent_checkpoint,omitempty"`
	SourceCWD        string `json:"source_cwd,omitempty"`
}

type Metadata struct {
	ID            string          `json:"id"`
	Timestamp     string          `json:"timestamp,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	CWD           string          `json:"cwd,omitempty"`
	GitBranch     string          `json:"git_branch,omitempty"`
	Title         string          `json:"title,omitempty"`
	Model         string          `json:"model,omitempty"`
	ModelProvider string          `json:"model_provider,omitempty"`
	CLIVersion    string          `json:"cli_version,omitempty"`
	Provenance    *Provenance     `json:"provenance,omitempty"`
	Extra         json.RawMessage `json:"extra,omitempty"`
}

type Transcript struct {
	SchemaVersion string          `json:"schema_version"`
	Meta          Metadata        `json:"meta"`
	Messages      []Message       `json:"messages"`
	Extra         json.RawMessage `json:"extra,omitempty"`
}

type Warning struct {
	Path    string `json:"path,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ParseResult struct {
	Transcript *Transcript `json:"transcript"`
	Warnings   []Warning   `json:"warnings,omitempty"`
}

type RenderResult struct {
	Data     []byte    `json:"-"`
	Warnings []Warning `json:"warnings,omitempty"`
}

type Capability struct {
	Read       bool `json:"read"`
	Write      bool `json:"write"`
	Discover   bool `json:"discover"`
	Save       bool `json:"save"`
	Delete     bool `json:"delete"`
	Continue   bool `json:"continue"`
	Remote     bool `json:"remote"`
	SourceOnly bool `json:"source_only"`
}

type HarnessInfo struct {
	Format      Format     `json:"format"`
	DisplayName string     `json:"display_name"`
	Capability  Capability `json:"capability"`
}
