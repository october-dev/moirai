package moirai

import (
	"strings"
	"testing"
)

func nativeFixture(t *testing.T) *Transcript {
	t.Helper()
	return &Transcript{
		SchemaVersion: SchemaVersion,
		Meta:          Metadata{ID: "11111111-2222-4333-a444-555555555555", Timestamp: "2026-08-10T20:05:50Z", CWD: "/repo", GitBranch: "main", Title: "Fix tests", Model: "model-a", CLIVersion: "1.2.3"},
		Messages: []Message{
			{Role: RoleUser, Timestamp: "2026-08-10T20:05:50Z", Content: []Block{{Type: BlockText, Text: "run tests"}}},
			{Role: RoleAssistant, Timestamp: "2026-08-10T20:05:51Z", Model: "model-a", Content: []Block{{Type: BlockThinking, Text: "I should run them."}, {Type: BlockToolUse, ID: "call-1", Name: "Bash", Input: rawJSON(map[string]any{"command": "go test ./..."})}}, StopReason: "tool_use", Usage: &Usage{InputTokens: 10, OutputTokens: 20}},
			{Role: RoleUser, Timestamp: "2026-08-10T20:05:52Z", Content: []Block{{Type: BlockToolResult, ToolUseID: "call-1", Content: rawJSON("ok")}}},
			{Role: RoleAssistant, Timestamp: "2026-08-10T20:05:53Z", Model: "model-a", Content: []Block{{Type: BlockText, Text: "All green."}}, StopReason: "end_turn"},
		},
	}
}

func TestNativeCodecRoundTrips(t *testing.T) {
	for _, codec := range writableCodecs() {
		t.Run(string(codec.Format()), func(t *testing.T) {
			original := nativeFixture(t)
			rendered, err := codec.Render(original, RenderOptions{Limits: DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := codec.Parse(rendered.Data, ParseOptions{Limits: DefaultLimits()})
			if err != nil {
				t.Fatalf("parse rendered output: %v\n%s", err, rendered.Data)
			}
			plain := ToText(parsed.Transcript, TextOptions{IncludeThinking: true, IncludeTools: true, MaxBytes: 1 << 20})
			for _, want := range []string{"run tests", "go test ./...", "All green."} {
				if !strings.Contains(plain, want) {
					t.Fatalf("projection missing %q:\n%s", want, plain)
				}
			}
			if err := Validate(parsed.Transcript, DefaultLimits()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func writableCodecs() []Codec {
	return []Codec{SimpleCodec{}, ClaudeCodeCodec{}, CodexCodec{}, NewPiCodec(FormatPi, "pi"), NewPiCodec(FormatCampfire, "Campfire"), AmpCodec{}, OpenCodeCodec{}, HermesCodec{}, CoworkCodec{}, GrokCodec{}, FXCodec{}, CursorCodec{}, CursorDesktopCodec{}, AntigravityCodec{}}
}

func TestAllWritableCodecPairs(t *testing.T) {
	for _, source := range writableCodecs() {
		t.Run(string(source.Format()), func(t *testing.T) {
			native, err := source.Render(nativeFixture(t), RenderOptions{Limits: DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range writableCodecs() {
				t.Run(string(target.Format()), func(t *testing.T) {
					converted, err := DefaultRegistry.Convert(native.Data, source.Format(), target.Format(), ParseOptions{Limits: DefaultLimits()})
					if err != nil {
						t.Fatal(err)
					}
					parsed, err := target.Parse(converted.Data, ParseOptions{Limits: DefaultLimits()})
					if err != nil {
						t.Fatal(err)
					}
					if len(parsed.Transcript.Messages) == 0 {
						t.Fatal("conversion produced no messages")
					}
				})
			}
		})
	}
}

func TestCodexProtocolRequirements(t *testing.T) {
	rendered, err := (CodexCodec{}).Render(nativeFixture(t), RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered.Data)
	for _, want := range []string{`"type":"session_meta"`, `"model_provider":"openai"`, `"type":"function_call"`, `"type":"function_call_output"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("rollout missing %s:\n%s", want, text)
		}
	}
}

func TestRegistryHasImplementedCodecs(t *testing.T) {
	for _, format := range []Format{FormatSimple, FormatClaudeCode, FormatCodex, FormatPi, FormatCampfire, FormatAmp, FormatOpenCode, FormatHermes, FormatCowork, FormatClaudeChat, FormatChatGPT, FormatGrok, FormatFX, FormatCursor, FormatCursorDesktop, FormatAntigravity} {
		if _, err := DefaultRegistry.Codec(format); err != nil {
			t.Fatalf("%s: %v", format, err)
		}
	}
}

func TestRenderReportsUnsupportedBlocks(t *testing.T) {
	transcript := nativeFixture(t)
	transcript.Messages[0].Content = append(transcript.Messages[0].Content, Block{Type: BlockArtifact, Artifact: &Artifact{Name: "report.txt"}})
	result, err := (CodexCodec{}).Render(transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "unsupported_block" {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	converted, err := DefaultRegistry.Convert(mustRenderSimple(t, transcript), FormatSimple, FormatFX, ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if len(converted.Warnings) < 2 {
		t.Fatalf("conversion warnings = %#v", converted.Warnings)
	}
}

func mustRenderSimple(t *testing.T, transcript *Transcript) []byte {
	t.Helper()
	result, err := (SimpleCodec{}).Render(transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	return result.Data
}
