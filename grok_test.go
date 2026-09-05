package moirai

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func readGrokFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/native/grok.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertGrokJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual JSON %q: %v", got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode expected JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}

func TestGrokParserFields(t *testing.T) {
	parsed, err := (GrokCodec{}).Parse(readGrokFixture(t), ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	meta := parsed.Transcript.Meta
	wantMeta := Metadata{
		ID: "native-grok", CWD: "/tmp/invented-grok-project", Title: "Invented Grok fixture",
		Model: "fixture-session-model", Timestamp: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-01-01T00:05:00Z", GitBranch: "fixture/invented",
	}
	if meta.ID != wantMeta.ID || meta.CWD != wantMeta.CWD || meta.Title != wantMeta.Title ||
		meta.Model != wantMeta.Model || meta.Timestamp != wantMeta.Timestamp ||
		meta.UpdatedAt != wantMeta.UpdatedAt || meta.GitBranch != wantMeta.GitBranch {
		t.Errorf("metadata = %+v, want %+v", meta, wantMeta)
	}
	messages := parsed.Transcript.Messages
	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4", len(messages))
	}
	for i, role := range []Role{RoleUser, RoleAssistant, RoleAssistant, RoleUser} {
		if messages[i].Role != role {
			t.Errorf("message %d role = %q, want %q", i, messages[i].Role, role)
		}
		wantBlocks := 1
		if i == 2 {
			wantBlocks = 2
		}
		if len(messages[i].Content) != wantBlocks {
			t.Fatalf("message %d block count = %d, want %d", i, len(messages[i].Content), wantBlocks)
		}
		encoded, err := json.Marshal(messages[i])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "UPDATES_ONLY_INVENTED_GROK_MARKER") {
			t.Errorf("message %d contains ignored updates marker", i)
		}
	}
	if block := messages[0].Content[0]; block.Type != BlockText || block.Text != "Inspect this invented native fixture." {
		t.Errorf("user block = %+v", block)
	}
	if block := messages[1].Content[0]; block.Type != BlockThinking || block.Text != "I will inspect the invented sample." || block.Encrypted != "invented-encrypted-reasoning" {
		t.Errorf("reasoning block = %+v", block)
	}
	if messages[1].Model != wantMeta.Model {
		t.Errorf("reasoning model = %q, want session fallback %q", messages[1].Model, wantMeta.Model)
	}
	if messages[2].Model != "fixture-assistant-model" {
		t.Errorf("assistant model = %q, want fixture-assistant-model override", messages[2].Model)
	}
	if block := messages[2].Content[0]; block.Type != BlockText || block.Text != "Reading the sample now." {
		t.Errorf("assistant text block = %+v", block)
	}
	call := messages[2].Content[1]
	if call.Type != BlockToolUse || call.ID != "fixture-call-1" || call.Name != "read_sample" {
		t.Errorf("tool call = %+v", call)
	}
	assertGrokJSONEqual(t, call.Input, json.RawMessage(`{"lines":2,"path":"sample.txt"}`))
	result := messages[3].Content[0]
	if result.Type != BlockToolResult || result.ToolUseID != "fixture-call-1" || result.ToolUseID != call.ID {
		t.Errorf("tool result = %+v, call ID = %q", result, call.ID)
	}
	assertGrokJSONEqual(t, result.Content, json.RawMessage(`"Invented sample contents."`))
	if len(parsed.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want exactly one", parsed.Warnings)
	}
	if warning := parsed.Warnings[0]; warning.Code != "unpaired_tool_result" || warning.Path != "chat_history[4]" {
		t.Errorf("warning = %+v, want unpaired_tool_result at chat_history[4]", warning)
	}
}

func TestGrokTitleFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		generated bool
		want      string
	}{
		{"generated title wins", true, "Invented Grok fixture"},
		{"session summary fallback", false, "Fallback fixture title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(readGrokFixture(t), &doc); err != nil {
				t.Fatal(err)
			}
			if !tc.generated {
				delete(doc["summary"].(map[string]any), "generated_title")
			}
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := (GrokCodec{}).Parse(data, ParseOptions{Limits: DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			if got := parsed.Transcript.Meta.Title; got != tc.want {
				t.Errorf("title = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGrokRoundTrip(t *testing.T) {
	original := &Transcript{
		SchemaVersion: SchemaVersion,
		Meta: Metadata{ID: "invented-grok-round-trip", Timestamp: "2026-02-01T00:00:00Z",
			Model: "session-model", Title: "Invented round trip", CWD: "/tmp/invented-project", GitBranch: "test/invented"},
		Messages: []Message{
			{Role: RoleUser, Timestamp: "2026-02-01T00:00:01Z", Content: []Block{{Type: BlockText, Text: "Read the invented sample."}}},
			{Role: RoleAssistant, Timestamp: "2026-02-01T00:00:02Z", Model: "session-model", Content: []Block{{Type: BlockThinking, Text: "I should read the sample.", Encrypted: "invented-ciphertext"}}},
			{Role: RoleAssistant, Timestamp: "2026-02-01T00:00:03Z", Model: "tool-model", StopReason: "tool_use", Content: []Block{
				{Type: BlockText, Text: "Reading it now."},
				{Type: BlockToolUse, ID: "round-trip-call", Name: "read_sample", Input: json.RawMessage(`{"path":"missing.txt","options":{"lines":2,"trim":true}}`)},
			}},
			{Role: RoleUser, Timestamp: "2026-02-01T00:00:04Z", Content: []Block{{Type: BlockToolResult, ToolUseID: "round-trip-call", Content: json.RawMessage(`"Invented file is missing."`), IsError: true}}},
			{Role: RoleAssistant, Timestamp: "2026-02-01T00:00:05Z", Model: "final-model", Content: []Block{{Type: BlockText, Text: "The invented sample could not be read."}}},
		},
	}
	rendered, err := (GrokCodec{}).Render(original, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := (GrokCodec{}).Parse(rendered.Data, ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Warnings) != 0 {
		t.Errorf("unexpected parse warnings: %+v", parsed.Warnings)
	}
	got := parsed.Transcript
	if got.Meta.Title != original.Meta.Title || got.Meta.Model != original.Meta.Model ||
		got.Meta.CWD != original.Meta.CWD || got.Meta.GitBranch != original.Meta.GitBranch || got.Meta.Timestamp != original.Meta.Timestamp {
		t.Errorf("metadata = %+v, want preserved fields from %+v", got.Meta, original.Meta)
	}
	if len(got.Messages) != len(original.Messages) {
		t.Fatalf("message count = %d, want %d", len(got.Messages), len(original.Messages))
	}
	for i, want := range original.Messages {
		message := got.Messages[i]
		if message.Role != want.Role || message.Model != want.Model {
			t.Errorf("message %d role/model = %q/%q, want %q/%q", i, message.Role, message.Model, want.Role, want.Model)
		}
		// Timestamps, stop reasons, and error status live only in ignored updates.
		if message.Timestamp != original.Meta.Timestamp {
			t.Errorf("message %d timestamp = %q, want session timestamp %q", i, message.Timestamp, original.Meta.Timestamp)
		}
		if message.StopReason != "" {
			t.Errorf("message %d stop reason = %q, want omission", i, message.StopReason)
		}
		if len(message.Content) != len(want.Content) {
			t.Fatalf("message %d block count = %d, want %d", i, len(message.Content), len(want.Content))
		}
		for j, wantBlock := range want.Content {
			block := message.Content[j]
			if block.Type != wantBlock.Type || block.Text != wantBlock.Text || block.Encrypted != wantBlock.Encrypted ||
				block.ID != wantBlock.ID || block.Name != wantBlock.Name || block.ToolUseID != wantBlock.ToolUseID {
				t.Errorf("message %d block %d = %+v, want preserved fields from %+v", i, j, block, wantBlock)
			}
			if len(block.Input) > 0 || len(wantBlock.Input) > 0 {
				assertGrokJSONEqual(t, block.Input, wantBlock.Input)
			}
			if len(block.Content) > 0 || len(wantBlock.Content) > 0 {
				assertGrokJSONEqual(t, block.Content, wantBlock.Content)
			}
			if block.IsError {
				t.Errorf("message %d block %d IsError = true, want false", i, j)
			}
		}
	}
	if got.Messages[2].Content[1].ID != got.Messages[3].Content[0].ToolUseID {
		t.Error("round trip broke tool call/result pairing")
	}
}

func TestGrokMalformedRecords(t *testing.T) {
	t.Run("junk records and malformed arguments", func(t *testing.T) {
		data := []byte(`{"chat_history":[42,{"content":"missing type"},{"type":"assistant","content":"Valid survivor.","tool_calls":[{"id":"survivor-call","name":"read_sample","arguments":"not JSON: sample.txt"}]}]}`)
		parsed, err := (GrokCodec{}).Parse(data, ParseOptions{Limits: DefaultLimits()})
		if err != nil {
			t.Fatal(err)
		}
		if len(parsed.Transcript.Messages) != 1 {
			t.Fatalf("message count = %d, want one survivor", len(parsed.Transcript.Messages))
		}
		message := parsed.Transcript.Messages[0]
		if message.Role != RoleAssistant || len(message.Content) != 2 {
			t.Fatalf("survivor = %+v, want assistant text and tool call", message)
		}
		if block := message.Content[0]; block.Type != BlockText || block.Text != "Valid survivor." {
			t.Errorf("survivor text = %+v", block)
		}
		call := message.Content[1]
		if call.Type != BlockToolUse || call.ID != "survivor-call" || call.Name != "read_sample" {
			t.Errorf("survivor tool call = %+v", call)
		}
		var arguments string
		if err := json.Unmarshal(call.Input, &arguments); err != nil {
			t.Fatalf("decode malformed arguments as JSON string: %v", err)
		}
		if arguments != "not JSON: sample.txt" {
			t.Errorf("arguments = %q, want original malformed text", arguments)
		}
	})
	t.Run("empty history", func(t *testing.T) {
		_, err := (GrokCodec{}).Parse([]byte(`{"chat_history":[]}`), ParseOptions{Limits: DefaultLimits()})
		if !errors.Is(err, ErrInvalidTranscript) {
			t.Errorf("error = %v, want ErrInvalidTranscript", err)
		}
	})
}

func TestGrokParseEnforcesInputLimits(t *testing.T) {
	const prefix = `{"chat_history":[{"type":"assistant","content":"Valid limit survivor."}],"updates":`
	for _, tc := range []struct {
		name    string
		updates string
		lower   func(*Limits)
	}{
		{"input bytes", `["` + strings.Repeat("x", 2048) + `"]`, func(l *Limits) { l.MaxInputBytes = 1024 }},
		{"nesting depth", strings.Repeat("[", 12) + `"ignored"` + strings.Repeat("]", 12), func(l *Limits) { l.MaxNestingDepth = 8 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := DefaultLimits()
			tc.lower(&limits)
			// The same chat history succeeds with these limits when updates is empty.
			if _, err := (GrokCodec{}).Parse([]byte(prefix+`[]}`), ParseOptions{Limits: limits}); err != nil {
				t.Fatalf("valid history under lowered limits: %v", err)
			}
			data := []byte(prefix + tc.updates + `}`)
			if _, err := (GrokCodec{}).Parse(data, ParseOptions{Limits: limits}); !errors.Is(err, ErrLimitExceeded) {
				t.Errorf("error = %v, want ErrLimitExceeded from ignored updates payload", err)
			}
			parsed, err := (GrokCodec{}).Parse(data, ParseOptions{Limits: DefaultLimits()})
			if err != nil {
				t.Fatalf("same document under default limits: %v", err)
			}
			if len(parsed.Transcript.Messages) != 1 || len(parsed.Transcript.Messages[0].Content) != 1 ||
				parsed.Transcript.Messages[0].Content[0].Text != "Valid limit survivor." {
				t.Fatalf("default-limit survivor = %+v", parsed.Transcript.Messages)
			}
		})
	}
}
