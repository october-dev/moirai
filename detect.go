package moirai

import (
	"bytes"
	"fmt"
)

func DetectFormat(data []byte) (Format, error) {
	limits := DefaultLimits()
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
	if err := decodeJSONDocument(trimmed, &value, limits); err != nil {
		first := trimmed
		if newline := bytes.IndexByte(trimmed, '\n'); newline >= 0 {
			first = bytes.TrimSpace(trimmed[:newline])
		}
		if lineErr := decodeJSONDocument(first, &value, limits); lineErr != nil {
			return "", fmt.Errorf("%w: cannot detect non-JSON input", ErrUnknownFormat)
		}
	}
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
		format, err = DetectFormat(data)
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
