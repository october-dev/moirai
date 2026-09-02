package moirai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestClaudeNativeLayoutAndDiscovery(t *testing.T) {
	for _, test := range []struct {
		cwd  string
		want string
	}{
		{cwd: "/Users/alice/src/app", want: "-Users-alice-src-app"},
		{cwd: "/tmp/e2e.dot_proj", want: "-tmp-e2e-dot-proj"},
		{cwd: "/Users/alice/my app@v2", want: "-Users-alice-my-app-v2"},
		{cwd: `C:\Users\x`, want: "C--Users-x"},
		{cwd: "", want: "-"},
	} {
		if got := encodeClaudeProject(test.cwd); got != test.want {
			t.Errorf("encodeClaudeProject(%q) = %q, want %q", test.cwd, got, test.want)
		}
	}

	transcript := nativeFixture(t)
	transcript.Meta.CWD = "/Users/alice/src/app"
	location, err := claudeLayout(transcript, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("-Users-alice-src-app", transcript.Meta.ID+".jsonl")
	if location != want {
		t.Fatalf("location = %q, want %q", location, want)
	}

	root := t.TempDir()
	id := "aaaaaaaa-1111-4222-8333-444444444444"
	project := filepath.Join(root, "-tmp-proj")
	if err := os.MkdirAll(filepath.Join(project, id, "subagents"), 0o700); err != nil {
		t.Fatal(err)
	}
	parent := `{"type":"user","sessionId":"` + id + `","cwd":"/tmp/proj","uuid":"u1","timestamp":"2026-08-10T20:05:50Z","message":{"role":"user","content":"hi"}}` + "\n"
	sidechain := `{"type":"user","isSidechain":true,"sessionId":"` + id + `","cwd":"/tmp/proj","uuid":"s1","timestamp":"2026-08-10T20:05:51Z","message":{"role":"user","content":"sub"}}` + "\n"
	if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, id, "subagents", "agent-1.jsonl"), []byte(sidechain), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocalFileStore(FormatClaudeCode, root, ".jsonl", claudeLayout)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.Discover(context.Background())
	if err != nil || len(refs) != 1 {
		t.Fatalf("discover = %#v, %v", refs, err)
	}
	if _, err := FindSession(refs, id, FormatClaudeCode); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeDetectionAndSidechainContract(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "native", "claude_code.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	format, err := DetectFormat(fixture)
	if err != nil || format != FormatClaudeCode {
		t.Fatalf("detect = %s, %v", format, err)
	}
	parsed, err := (ClaudeCodeCodec{}).Parse(fixture, ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ToText(parsed.Transcript, TextOptions{MaxBytes: 1 << 20}), "SIDECHAIN") || len(parsed.Transcript.Messages) != 4 {
		t.Fatalf("sidechain leaked: %#v", parsed.Transcript.Messages)
	}
	rendered, err := (ClaudeCodeCodec{}).Render(parsed.Transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered.Data), `"encrypted"`) || !strings.Contains(string(rendered.Data), `"redacted_thinking"`) {
		t.Fatalf("invalid thinking contract: %s", rendered.Data)
	}
	codex, err := (CodexCodec{}).Render(parsed.Transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := decodeJSONLines(codex.Data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	foundOutput := false
	for _, record := range records {
		payload := object(record["payload"])
		if stringValue(payload["type"]) == "function_call_output" {
			if _, ok := payload["output"].(string); !ok {
				t.Fatalf("Codex tool output is not a string: %#v", payload["output"])
			}
			foundOutput = true
		}
	}
	if !foundOutput {
		t.Fatal("Codex tool output missing")
	}
}

func TestClaudeToolResultContract(t *testing.T) {
	transcript := nativeFixture(t)
	transcript.Messages[2].Content[0].Content = rawJSON(map[string]any{"ok": true, "value": 1})
	rendered, err := (ClaudeCodeCodec{}).Render(transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	records, _, err := decodeJSONLines(rendered.Data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		for _, rawBlock := range array(object(record["message"])["content"]) {
			block := object(rawBlock)
			if stringValue(block["type"]) == "tool_result" {
				if _, ok := block["content"].(string); !ok {
					t.Fatalf("tool result content = %#v", block["content"])
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("tool result missing")
	}
}

func TestCodexNativeContractsAndSetupFiltering(t *testing.T) {
	fixture := strings.Join([]string{
		`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"codex-1","cwd":"/repo"}}`,
		`{"timestamp":"2026-01-01T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>SECRET_MARKER</environment_context>"}]}}`,
		`{"timestamp":"2026-01-01T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"real request"}]}}`,
	}, "\n") + "\n"
	parsed, err := (CodexCodec{}).Parse([]byte(fixture), ParseOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	text := ToText(parsed.Transcript, TextOptions{MaxBytes: 1 << 20})
	if strings.Contains(text, "SECRET_MARKER") || !strings.Contains(text, "real request") {
		t.Fatalf("setup filtering failed: %s", text)
	}

	transcript := nativeFixture(t)
	transcript.Messages[0].Content = append(transcript.Messages[0].Content, Block{Type: BlockImage, Source: &MediaSource{Type: "base64", MediaType: "image/png", Data: "aGVsbG8="}})
	transcript.Messages[2].Content[0].Content = rawJSON([]any{map[string]any{"type": "text", "text": "tool output"}})
	rendered, err := (CodexCodec{}).Render(transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered.Data), `"input_image"`) || strings.Contains(string(rendered.Data), `"turn_context"`) {
		t.Fatalf("invalid Codex record: %s", rendered.Data)
	}
	records, _, err := decodeJSONLines(rendered.Data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		payload := object(record["payload"])
		if stringValue(payload["type"]) == "function_call_output" {
			if output, ok := payload["output"].(string); !ok || output != "tool output" {
				t.Fatalf("output = %#v", payload["output"])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("function_call_output missing")
	}
}

func TestStoreLimitsWarningsAndAtomicCollision(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalFileStore(FormatSimple, root, ".json", flatJSONLayout)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.json"), []byte(`{"id":"large","messages":[{"role":"user","content":"`+strings.Repeat("x", 256)+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxInputBytes = 64
	refs, err := store.DiscoverWithLimits(context.Background(), limits)
	var discoveryErr *DiscoveryError
	if len(refs) != 0 || !errors.As(err, &discoveryErr) || discoveryErr.Code != "skipped_oversize" {
		t.Fatalf("discover = %#v, %#v", refs, err)
	}

	collisionRoot := t.TempDir()
	collisionStore, err := NewLocalFileStore(FormatSimple, collisionRoot, ".json", flatJSONLayout)
	if err != nil {
		t.Fatal(err)
	}
	transcript := nativeFixture(t)
	transcript.Meta.ID = "collision"
	var successes atomic.Int32
	var unexpected atomic.Int32
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, saveErr := collisionStore.Save(context.Background(), transcript, RenderOptions{Limits: DefaultLimits()})
			if saveErr == nil {
				successes.Add(1)
			} else if !errors.Is(saveErr, ErrSessionExists) {
				unexpected.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("successes=%d unexpected=%d", successes.Load(), unexpected.Load())
	}
}

func TestLargeClaudeSessionRemainsDiscoverable(t *testing.T) {
	root := t.TempDir()
	id := "bbbbbbbb-1111-4222-8333-444444444444"
	project := filepath.Join(root, "-tmp-large")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, id+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","sessionId":"` + id + `","cwd":"/tmp/large","uuid":"u1","timestamp":"2026-08-10T20:05:50Z","message":{"role":"user","content":"hi"}}` + "\n"
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Truncate(33 << 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocalFileStore(FormatClaudeCode, root, ".jsonl", claudeLayout)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.Discover(context.Background())
	if err != nil || len(refs) != 1 || refs[0].ID != id {
		t.Fatalf("discover = %#v, %v", refs, err)
	}
}

func TestCursorDesktopWildcardsCannotTouchOtherSessions(t *testing.T) {
	store := &CursorDesktopStore{UserDir: t.TempDir()}
	victim := nativeFixture(t)
	victim.Meta.ID = "victim-1111"
	savedVictim, err := store.Save(context.Background(), victim, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	wildcard := nativeFixture(t)
	wildcard.Meta.ID = "v%"
	savedWildcard, err := store.Save(context.Background(), wildcard, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), savedVictim.Ref, ParseOptions{Limits: DefaultLimits()}); err != nil {
		t.Fatalf("wildcard save damaged victim: %v", err)
	}
	if err := store.Delete(context.Background(), savedWildcard.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), savedVictim.Ref, ParseOptions{Limits: DefaultLimits()}); err != nil {
		t.Fatalf("wildcard delete damaged victim: %v", err)
	}
}

func TestExistingSQLitePermissionsAndSchemaAreNotMutated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are not portable to Windows")
	}
	userDir := t.TempDir()
	path := filepath.Join(userDir, "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLite(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err = openSQLite(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed to %o", info.Mode().Perm())
	}

	db, err = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	store := &CursorDesktopStore{UserDir: userDir}
	if store.dbPath() != path {
		t.Fatalf("db path = %s, want %s", store.dbPath(), path)
	}
	if _, err := store.Save(context.Background(), nativeFixture(t), RenderOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("save error = %v", err)
	}
}

func TestRangesMediaTerminalAndSimpleRobustness(t *testing.T) {
	if selector, err := ParseSelector("session#2-"); err != nil || selector.Span.End != 0 {
		t.Fatalf("open selector = %#v, %v", selector, err)
	}
	selector, err := ParseSelector("session#-3")
	if err != nil || selector.Span.Start != 1 || selector.Span.End != 3 {
		t.Fatalf("prefix selector = %#v, %v", selector, err)
	}
	if _, err := Select(fixtureTranscript(), Span{Start: 2, End: 4}); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("assistant-first range error = %v", err)
	}
	unanswered := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: "pending"}, Messages: []Message{
		{Role: RoleUser, Content: []Block{{Type: BlockText, Text: "run"}}},
		{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: "call", Name: "Bash", Input: json.RawMessage(`{}`)}}},
	}}
	if _, err := Select(unanswered, Span{Start: 1, End: 2}); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("unanswered range error = %v", err)
	}
	badMedia := &Transcript{SchemaVersion: SchemaVersion, Meta: Metadata{ID: "media"}, Messages: []Message{{Role: RoleUser, Content: []Block{{Type: BlockImage, Source: &MediaSource{Type: "base64"}}}}}}
	if err := Validate(badMedia, DefaultLimits()); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("media error = %v", err)
	}
	if got := ScrubTerminal("safe\x1b]52;c;payload\a\rtext"); strings.ContainsAny(got, "\x1b\a\r") {
		t.Fatalf("unsafe terminal output: %q", got)
	}
	simple := []byte(`{"id":"simple","messages":[{"role":"assistant","content":"done","stop_reason":{"other":"custom"}}]}`)
	parsed, err := (SimpleCodec{}).Parse(simple, ParseOptions{Limits: DefaultLimits()})
	if err != nil || parsed.Transcript.Messages[0].StopReason != "custom" {
		t.Fatalf("simple stop reason = %#v, %v", parsed, err)
	}
}

func TestArchiveRejectsUnknownTranscriptFields(t *testing.T) {
	encoded, err := EncodeArchive(nativeFixture(t), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	injected := strings.Replace(string(encoded), `"transcript": {`, `"transcript": {"injected_field":true,`, 1)
	if _, err := DecodeArchive([]byte(injected), DefaultLimits()); !errors.Is(err, ErrInvalidTranscript) {
		t.Fatalf("injected archive error = %v", err)
	}
}
