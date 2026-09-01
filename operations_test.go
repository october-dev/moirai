package moirai

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func fixtureTranscript() *Transcript {
	return &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: "session", Timestamp: "2026-01-01T00:00:00Z", CWD: "/tmp/work"}, Messages: []Message{
		{Role: RoleUser, Content: []Block{{Type: BlockText, Text: "repair the parser"}}},
		{Role: RoleAssistant, Content: []Block{{Type: BlockThinking, Text: "inspect first"}, {Type: BlockToolUse, ID: "call-1", Name: "Read", Input: []byte(`{"file_path":"parser.go"}`)}}},
		{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: "call-1", Content: []byte(`"package parser"`)}}},
		{Role: RoleAssistant, Content: []Block{{Type: BlockText, Text: "Parser repaired and tests pass."}}},
	}}
}

func TestSelectorAndToolSafeRange(t *testing.T) {
	selector, err := ParseSelector("session#1-3")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(fixtureTranscript(), *selector.Span)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Messages) != 3 || selected.Meta.Provenance.ParentSessionID != "session" {
		t.Fatalf("selection = %#v", selected)
	}
	if _, err := Select(fixtureTranscript(), Span{Start: 2, End: 2}); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("error = %v", err)
	}
	toolOnly := &Transcript{Messages: []Message{{Role: RoleUser, Content: []Block{{Type: BlockToolUse, ID: "call", Name: "Read"}}}}}
	if _, err := Select(toolOnly, Span{Start: 1, End: 1}); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("tool-only start error = %v", err)
	}
}

func TestTextBoundAndSearch(t *testing.T) {
	text := ToText(fixtureTranscript(), TextOptions{MaxBytes: 90, IncludeTools: true, IncludeMetadata: true})
	if len(text) > 90 || !strings.HasPrefix(text, "[earlier context omitted]") {
		t.Fatalf("text (%d) = %q", len(text), text)
	}
	hits := Search(fixtureTranscript(), "parser repaired", 10)
	if len(hits) != 1 || hits[0].MessageIndex != 4 {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestArchiveIntegrity(t *testing.T) {
	encoded, err := EncodeArchive(fixtureTranscript(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeArchive(encoded, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Meta.ID != "session" {
		t.Fatalf("id = %q", decoded.Meta.ID)
	}
	corrupt := strings.Replace(string(encoded), "repair the parser", "replace the parser", 1)
	if _, err := DecodeArchive([]byte(corrupt), DefaultLimits()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("error = %v", err)
	}
}

func TestArchiveV1InteroperabilityFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/archive-v1.moirai")
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := DecodeArchive(data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Meta.ID != "interop" || len(transcript.Messages) != 3 {
		t.Fatalf("unexpected transcript: %#v", transcript)
	}
}

func TestArchiveNestingLimit(t *testing.T) {
	data := []byte(`{"format":"moirai.session","version":"1","created_at":"2026-01-01T00:00:00Z","transcript":{"schema_version":"1.0","meta":{"id":"deep"},"messages":[]},"sha256":"x"}`)
	limits := DefaultLimits()
	limits.MaxNestingDepth = 1
	if _, err := DecodeArchive(data, limits); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("error = %v", err)
	}
}
