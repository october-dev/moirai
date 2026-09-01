package moirai

import (
	"bytes"
	"fmt"
)

func DetectFormat(data []byte) (Format, error) {
	return DetectFormatWithLimits(data, DefaultLimits())
}

func DetectFormatWithLimits(data []byte, limits Limits) (Format, error) {
	limits = limits.normalized()
	if int64(len(data)) > limits.MaxInputBytes {
		return "", ErrLimitExceeded
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "", fmt.Errorf("%w: empty input", ErrInvalidTranscript)
	}
	if err := checkJSONDepth(trimmed, limits.MaxNestingDepth); err != nil {
		return "", err
	}
	var value map[string]any
	if err := decodeJSONDocument(trimmed, &value, limits); err == nil {
		return detectObject(value)
	}
	remaining := trimmed
	markers := map[Format]bool{}
	for lines := 0; len(remaining) > 0 && lines < 512; lines++ {
		line := remaining
		if newline := bytes.IndexByte(remaining, '\n'); newline >= 0 {
			line, remaining = remaining[:newline], remaining[newline+1:]
		} else {
			remaining = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || decodeJSONDocument(line, &value, limits) != nil {
			continue
		}
		if format, err := detectObject(value); err == nil {
			return format, nil
		}
		kind := stringValue(value["type"])
		if kind == "file-history-snapshot" || kind == "progress" || kind == "queue-operation" || kind == "system" || kind == "attachment" {
			markers[FormatClaudeCode] = true
		}
	}
	if len(markers) == 1 {
		for format := range markers {
			return format, nil
		}
	}
	return "", fmt.Errorf("%w: cannot detect JSON or JSONL input", ErrUnknownFormat)
}

func detectObject(value map[string]any) (Format, error) {
	kind := stringValue(value["type"])
	if kind == "session_meta" || kind == "turn_context" || kind == "response_item" || kind == "event_msg" {
		return FormatCodex, nil
	}
	if kind == "session" && value["version"] != nil || kind == "model_change" || kind == "session_info" || kind == "custom_message" {
		return FormatPi, nil
	}
	if (kind == "user" || kind == "assistant" || kind == "summary") && (value["sessionId"] != nil || value["uuid"] != nil || value["leafUuid"] != nil) {
		return FormatClaudeCode, nil
	}
	if value["v"] != nil && value["env"] != nil && value["messages"] != nil {
		return FormatAmp, nil
	}
	if value["info"] != nil && value["messages"] != nil {
		return FormatOpenCode, nil
	}
	if value["header"] != nil && value["transcript"] != nil {
		return FormatCowork, nil
	}
	if value["uuid"] != nil && value["chat_messages"] != nil {
		return FormatClaudeChat, nil
	}
	if value["mapping"] != nil && (value["conversation_id"] != nil || value["id"] != nil) {
		return FormatChatGPT, nil
	}
	if value["started_at"] != nil && value["messages"] != nil {
		return FormatHermes, nil
	}
	if value["chat_history"] != nil && (value["summary"] != nil || value["updates"] != nil) {
		return FormatGrok, nil
	}
	if value["events"] != nil && value["session"] != nil && value["authority"] != nil {
		return FormatFX, nil
	}
	if value["blobs"] != nil && value["meta"] != nil {
		return FormatCursor, nil
	}
	if value["header"] != nil && value["bubbles"] != nil {
		return FormatCursorDesktop, nil
	}
	if value["trajectory_meta"] != nil && value["steps"] != nil {
		return FormatAntigravity, nil
	}
	if value["messages"] != nil {
		return FormatSimple, nil
	}
	return "", ErrUnknownFormat
}

func Parse(data []byte, format Format, opts ParseOptions) (*ParseResult, Format, error) {
	if format == "" {
		var err error
		format, err = DetectFormatWithLimits(data, opts.Limits)
		if err != nil {
			return nil, "", err
		}
	}
	codec, err := DefaultRegistry.Codec(format)
	if err != nil {
		return nil, "", err
	}
	result, err := codec.Parse(data, opts)
	return result, format, err
}
