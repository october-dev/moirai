package moirai

import "fmt"

// Native numeric date fields may be null when a chat has no source date.
// Legacy agent rendering deliberately retains its existing behavior.
func nativeMillis(t *Transcript, stamp string) any {
	if t.SchemaVersion == ChatSchemaVersion && stamp == "" {
		return nil
	}
	return epochMillis(stamp)
}

func nativeSeconds(t *Transcript, stamp string) any {
	if t.SchemaVersion == ChatSchemaVersion && stamp == "" {
		return nil
	}
	return float64(epochMillis(stamp)) / 1000
}

// Native agent formats do not share a system-message representation. Preserve
// the message position and text as explicitly labelled user context, never as
// invented native instructions. Existing user/assistant transcripts are untouched.
func renderChatSystem(t *Transcript, opts RenderOptions, codec Codec) (*RenderResult, error, bool) {
	if t == nil {
		return nil, nil, false
	}
	hasSystem := false
	for _, m := range t.Messages {
		if m.Role == RoleSystem {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		return nil, nil, false
	}
	if err := Validate(t, opts.Limits); err != nil {
		return nil, err, true
	}
	var warnings []Warning
	copy := *t
	copy.Messages = append([]Message(nil), t.Messages...)
	for i, m := range t.Messages {
		if m.Role != RoleSystem {
			continue
		}
		copy.Messages[i].Role = RoleUser
		copy.Messages[i].Content = append([]Block{{Type: BlockText, Text: "[System message from source chat]"}}, m.Content...)
		warnings = append(warnings, Warning{Path: fmt.Sprintf("messages[%d].role", i), Code: "system_role_flattened", Message: "System message preserved as labelled user context; destination instruction semantics are not equivalent"})
	}
	r, err := codec.Render(&copy, opts)
	if r != nil {
		r.Warnings = append(warnings, r.Warnings...)
	}
	return r, err, true
}
