package moirai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

type TextOptions struct {
	MaxBytes        int
	IncludeThinking bool
	IncludeTools    bool
	IncludeMetadata bool
}

func ToText(t *Transcript, opts TextOptions) string {
	if t == nil {
		return ""
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 64 << 10
	}
	var sections []string
	if opts.IncludeMetadata {
		facts := []string{"Session: " + t.Meta.ID}
		if t.Meta.Title != "" {
			facts = append(facts, "Title: "+t.Meta.Title)
		}
		if t.Meta.CWD != "" {
			facts = append(facts, "Working directory: "+t.Meta.CWD)
		}
		if t.Meta.GitBranch != "" {
			facts = append(facts, "Git branch: "+t.Meta.GitBranch)
		}
		sections = append(sections, strings.Join(facts, "\n"))
	}
	for i, message := range t.Messages {
		parts := make([]string, 0, len(message.Content))
		for _, block := range message.Content {
			switch block.Type {
			case BlockText:
				parts = append(parts, block.Text)
			case BlockThinking:
				if opts.IncludeThinking && block.Text != "" {
					parts = append(parts, "Thinking: "+block.Text)
				}
			case BlockToolUse:
				if opts.IncludeTools {
					parts = append(parts, fmt.Sprintf("Tool call %s%s", block.Name, compactJSONSuffix(block.Input)))
				}
			case BlockToolResult:
				if opts.IncludeTools {
					parts = append(parts, fmt.Sprintf("Tool result%s%s", errorSuffix(block.IsError), compactJSONSuffix(block.Content)))
				}
			case BlockImage:
				parts = append(parts, "[image omitted]")
			case BlockArtifact:
				if block.Artifact != nil {
					parts = append(parts, "[artifact: "+block.Artifact.Name+"]")
				}
			}
		}
		if len(parts) == 0 {
			continue
		}
		label := "User"
		if message.Role == RoleAssistant {
			label = "Assistant"
		}
		sections = append(sections, fmt.Sprintf("%s [%d]: %s", label, i+1, strings.Join(parts, "\n")))
	}
	return boundedTail(strings.Join(sections, "\n\n"), opts.MaxBytes)
}

func compactJSONSuffix(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return ""
	}
	return ": " + out.String()
}

func errorSuffix(isError bool) string {
	if isError {
		return " (error)"
	}
	return ""
}

func boundedTail(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	const marker = "[earlier context omitted]\n"
	budget := maxBytes - len(marker)
	if budget <= 0 {
		return validUTF8Tail(value, maxBytes)
	}
	tail := validUTF8Tail(value, budget)
	if newline := strings.IndexByte(tail, '\n'); newline >= 0 {
		tail = tail[newline+1:]
	}
	return marker + tail
}

func validUTF8Tail(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}
