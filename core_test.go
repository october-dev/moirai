package moirai

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSimpleRoundTripAndToolPairing(t *testing.T) {
	source := `{"id":"s1","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp/project","messages":[{"role":"user","content":"run tests"},{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]},{"role":"user","content":[{"type":"tool_result","content":"ok"}]},{"role":"assistant","content":"done"}]}`
	parsed, err := (SimpleCodec{}).Parse([]byte(source), ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	call := parsed.Transcript.Messages[1].Content[0]
	result := parsed.Transcript.Messages[2].Content[0]
	if call.ID == "" || result.ToolUseID != call.ID {
		t.Fatalf("tool pair = %q/%q", call.ID, result.ToolUseID)
	}
	rendered, err := (SimpleCodec{}).Render(parsed.Transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(rendered.Data) {
		t.Fatal("rendered invalid JSON")
	}
	reparsed, err := (SimpleCodec{}).Parse(rendered.Data, ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if len(reparsed.Transcript.Messages) != 4 {
		t.Fatalf("messages = %d", len(reparsed.Transcript.Messages))
	}
}

func TestSimpleCanonicalMetadata(t *testing.T) {
	data, err := os.ReadFile("testdata/session-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := (SimpleCodec{}).Parse(data, ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Transcript.Meta.ID != "interop" {
		t.Fatalf("id = %q", parsed.Transcript.Meta.ID)
	}
	rendered, err := (SimpleCodec{}).Render(parsed.Transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered.Data), `"meta"`) {
		t.Fatalf("rendered = %s", rendered.Data)
	}
}

func TestConvertMintsIdentityAndProvenance(t *testing.T) {
	source := []byte(`{"id":"source","messages":[{"role":"user","content":"hello"}]}`)
	r := NewRegistry(SimpleCodec{}, aliasCodec{format: "other", SimpleCodec: SimpleCodec{}})
	result, err := r.Convert(source, FormatSimple, "other", ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := (SimpleCodec{}).Parse(result.Data, ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Transcript.Meta.ID == "source" {
		t.Fatal("identity was not minted")
	}
	if parsed.Transcript.Meta.Provenance == nil || parsed.Transcript.Meta.Provenance.SourceSessionID != "source" {
		t.Fatal("missing provenance")
	}
}

type aliasCodec struct {
	format Format
	SimpleCodec
}

func (a aliasCodec) Format() Format    { return a.format }
func (a aliasCodec) Info() HarnessInfo { i := a.SimpleCodec.Info(); i.Format = a.format; return i }

func TestValidationBoundsAndReferences(t *testing.T) {
	tx := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: "x"}, Messages: []Message{{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: "missing", Content: json.RawMessage(`"x"`)}}}}}
	if err := Validate(tx, DefaultLimits()); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("error = %v", err)
	}
	tx.Messages[0].Content = []Block{{Type: BlockText, Text: strings.Repeat("x", 20)}}
	limits := DefaultLimits()
	limits.MaxTextBytes = 10
	if err := Validate(tx, limits); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestJSONSafetyPreflight(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxNestingDepth = 2
	if _, err := (SimpleCodec{}).Parse([]byte(`{"messages":[{"role":"user","content":"too deep"}]}`), ParseOptions{Limits: limits}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("depth error = %v", err)
	}
	if _, err := (SimpleCodec{}).Parse([]byte(`{"messages":[]} {"messages":[]}`), ParseOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("trailing document error = %v", err)
	}
}

func TestDetectPrettyJSONAndJSONLines(t *testing.T) {
	pretty := []byte("{\n  \"messages\": []\n}\n")
	if format, err := DetectFormat(pretty); err != nil || format != FormatSimple {
		t.Fatalf("pretty format = %q, %v", format, err)
	}
	jsonl := []byte("{\"type\":\"session_meta\",\"payload\":{}}\n{\"type\":\"event_msg\",\"payload\":{}}\n")
	if format, err := DetectFormat(jsonl); err != nil || format != FormatCodex {
		t.Fatalf("jsonl format = %q, %v", format, err)
	}
}

func TestValidationUsageAndAggregateBounds(t *testing.T) {
	transcript := fixtureTranscript()
	transcript.Messages[0].Usage = &Usage{InputTokens: -1}
	if err := Validate(transcript, DefaultLimits()); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("usage error = %v", err)
	}
	transcript = fixtureTranscript()
	limits := DefaultLimits()
	limits.MaxInputBytes = 10
	if err := Validate(transcript, limits); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("aggregate error = %v", err)
	}
}

func TestValidationCountsStructuralNesting(t *testing.T) {
	transcript := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: "nested"}, Messages: []Message{{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: "call", Name: "tool", Input: json.RawMessage(`{"one":{}}`)}}}}}
	limits := DefaultLimits()
	limits.MaxNestingDepth = 6
	if err := Validate(transcript, limits); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("depth error = %v", err)
	}
	limits.MaxNestingDepth = 7
	if err := Validate(transcript, limits); err != nil {
		t.Fatalf("valid depth error = %v", err)
	}
}
