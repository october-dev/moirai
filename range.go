package moirai

import (
	"fmt"
	"strconv"
	"strings"
)

type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Selector struct {
	SessionID string `json:"session_id"`
	Span      *Span  `json:"span,omitempty"`
}

func ParseSelector(value string) (Selector, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Selector{}, fmt.Errorf("%w: empty selector", ErrInvalidTranscript)
	}
	id, suffix, found := strings.Cut(value, "#")
	if !found {
		return Selector{SessionID: id}, nil
	}
	if id == "" || suffix == "" || strings.Contains(suffix, "#") {
		return Selector{}, fmt.Errorf("%w: invalid selector", ErrInvalidTranscript)
	}
	startText, endText, hasEnd := strings.Cut(suffix, "-")
	start := 1
	var err error
	if startText != "" {
		start, err = strconv.Atoi(startText)
	}
	if err != nil || start < 1 || startText == "" && !hasEnd {
		return Selector{}, fmt.Errorf("%w: invalid range start", ErrInvalidTranscript)
	}
	end := start
	if hasEnd {
		if endText == "" {
			end = 0
		} else {
			end, err = strconv.Atoi(endText)
		}
		if err != nil || end != 0 && end < start {
			return Selector{}, fmt.Errorf("%w: invalid range end", ErrInvalidTranscript)
		}
	}
	return Selector{SessionID: id, Span: &Span{Start: start, End: end}}, nil
}

func Select(t *Transcript, span Span) (*Transcript, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: range outside transcript", ErrInvalidTranscript)
	}
	if span.End == 0 {
		span.End = len(t.Messages)
	}
	if span.Start < 1 || span.End < span.Start || span.End > len(t.Messages) {
		return nil, fmt.Errorf("%w: range outside transcript", ErrInvalidTranscript)
	}
	start, end := span.Start-1, span.End
	first := t.Messages[start]
	if first.Role != RoleUser || !hasNonToolContent(first) {
		return nil, fmt.Errorf("%w: range must start on a user message with portable content", ErrInvalidTranscript)
	}
	selectedUses := map[string]struct{}{}
	selectedResults := map[string]struct{}{}
	allUses := map[string]int{}
	allResults := map[string]int{}
	for mi, message := range t.Messages {
		for _, block := range message.Content {
			if block.Type == BlockToolUse && block.ID != "" {
				allUses[block.ID] = mi
			}
			if block.Type == BlockToolResult && block.ToolUseID != "" {
				allResults[block.ToolUseID] = mi
			}
			if mi >= start && mi < end {
				if block.Type == BlockToolUse && block.ID != "" {
					selectedUses[block.ID] = struct{}{}
				}
				if block.Type == BlockToolResult && block.ToolUseID != "" {
					selectedResults[block.ToolUseID] = struct{}{}
				}
			}
		}
	}
	for id := range selectedUses {
		resultAt, exists := allResults[id]
		if !exists {
			return nil, fmt.Errorf("%w: range ends with unanswered tool call %q", ErrInvalidTranscript, id)
		}
		if resultAt < start || resultAt >= end {
			return nil, fmt.Errorf("%w: range separates tool call %q from its result", ErrInvalidTranscript, id)
		}
	}
	for id := range selectedResults {
		if callAt, exists := allUses[id]; exists && (callAt < start || callAt >= end) {
			return nil, fmt.Errorf("%w: range separates tool result %q from its call", ErrInvalidTranscript, id)
		}
	}
	copyTranscript := *t
	copyTranscript.Messages = append([]Message(nil), t.Messages[start:end]...)
	if copyTranscript.Meta.Provenance == nil {
		copyTranscript.Meta.Provenance = &Provenance{}
	}
	copyTranscript.Meta.Provenance.ParentSessionID = t.Meta.ID
	copyTranscript.Meta.Provenance.ParentCheckpoint = fmt.Sprintf("messages:%d-%d", span.Start, span.End)
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	copyTranscript.Meta.ID = id
	return &copyTranscript, nil
}

func hasNonToolContent(message Message) bool {
	for _, block := range message.Content {
		if block.Type != BlockToolUse && block.Type != BlockToolResult {
			return true
		}
	}
	return false
}
