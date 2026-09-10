package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	moirai "github.com/october-dev/moirai"
)

func TestFormatsAndInterspersedFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"formats", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"claude_code"`) || !strings.Contains(stdout.String(), `"chatgpt"`) {
		t.Fatalf("unexpected formats: %s", stdout.String())
	}

	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte(`{"id":"test","messages":[{"role":"user","content":"hello"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := a.run(context.Background(), []string{"inspect", path, "--from", "simple", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"messages": 1`) {
		t.Fatalf("unexpected inspect result: %s", stdout.String())
	}
}

func TestImportRehomesMissingWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	input := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(input, []byte(`{"id":"source","cwd":"/definitely/missing/moirai-project","messages":[{"role":"user","content":"hello"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"import", input, "--to", "claude_code"}); err != nil {
		t.Fatal(err)
	}
	var saved moirai.SavedSession
	if err := json.Unmarshal(stdout.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if saved.Ref.CWD != cwd || !strings.Contains(stderr.String(), "cwd_rehomed") {
		t.Fatalf("saved=%#v stderr=%q", saved, stderr.String())
	}
}

func TestContinueClaudeUsesNativeProjectLayout(t *testing.T) {
	home := t.TempDir()
	claudeConfig := filepath.Join(home, "claude-config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfig)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "share"))
	project := filepath.Join(t.TempDir(), "e2e.dot_proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "session.json")
	source := map[string]any{
		"id":        "source",
		"timestamp": "2026-09-02T00:00:00Z",
		"cwd":       project,
		"messages":  []any{map[string]any{"role": "user", "content": "continue this session"}},
	}
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"continue", input, "--from", "simple", "--with", "claude_code", "--no-launch"}); err != nil {
		t.Fatal(err)
	}
	var saved moirai.SavedSession
	if err := json.Unmarshal(stdout.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	encodedProject := strings.Map(func(char rune) rune {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			return char
		}
		return '-'
	}, project)
	wantedLocation := filepath.Join(encodedProject, saved.Ref.ID+".jsonl")
	if saved.Ref.Location != wantedLocation {
		t.Fatalf("location = %q, want %q", saved.Ref.Location, wantedLocation)
	}
	if _, err := os.Stat(filepath.Join(claudeConfig, "projects", wantedLocation)); err != nil {
		t.Fatalf("saved Claude session: %v", err)
	}
	command, err := moirai.CommandFor(moirai.FormatClaudeCode, saved.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if command.Program != "claude" || len(command.Args) != 2 || command.Args[0] != "--resume" || command.Args[1] != saved.Ref.ID || command.Dir != project {
		t.Fatalf("launch command = %#v", command)
	}
}

func TestHumanOutputScrubsTerminalControls(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(home, ".pi", "agent", "sessions", "--tmp-project--")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	data := strings.Join([]string{
		`{"type":"session","version":3,"id":"terminal-safe","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp/project"}`,
		`{"type":"message","id":"m1","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"needle\u001b]52;c;cGF5bG9hZA==\u0007"}]}}`,
		`{"type":"session_info","name":"title\u001b[2J"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list", "--format", "pi"}, {"show", "terminal-safe", "--format", "pi"}, {"search", "needle", "--format", "pi"}} {
		var stdout, stderr bytes.Buffer
		a := app{out: &stdout, err: &stderr}
		if err := a.run(context.Background(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.ContainsRune(stdout.String(), '\x1b') || strings.ContainsRune(stdout.String(), '\a') {
			t.Fatalf("%v emitted terminal control bytes: %q", args, stdout.String())
		}
	}
}

func TestArchiveCreateAndVerify(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "session.json")
	archive := filepath.Join(dir, "session.moirai")
	if err := os.WriteFile(input, []byte(`{"id":"test","messages":[{"role":"user","content":"hello"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"archive", "create", input, "--out", archive}); err != nil {
		t.Fatal(err)
	}
	if err := a.run(context.Background(), []string{"archive", "verify", archive}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"valid": true`) {
		t.Fatalf("unexpected verify result: %s", stdout.String())
	}
}

func TestArchiveInspect(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "session.json")
	archive := filepath.Join(dir, "session.moirai")
	source := `{"id":"inspect-me","timestamp":"2026-09-01T00:00:00Z","cwd":"/tmp/project","title":"Refactor","model":"test-model","messages":[{"role":"user","content":"secret needle text"},{"role":"assistant","content":[{"type":"thinking","text":"hidden reasoning needle"},{"type":"tool_use","name":"Bash","input":{"command":"secret tool needle"}}]}]}`
	if err := os.WriteFile(input, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"archive", "create", input, "--out", archive}); err != nil {
		t.Fatal(err)
	}

	// Human-readable output carries structural metadata and no transcript content.
	stdout.Reset()
	if err := a.run(context.Background(), []string{"archive", "inspect", archive}); err != nil {
		t.Fatal(err)
	}
	human := stdout.String()
	for _, want := range []string{"Format: moirai.session", "Schema version:", "Digest: valid", "ID: inspect-me", "Title: Refactor", "Working directory: /tmp/project", "Model: test-model", "Messages: 2", "Blocks:"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human output missing %q:\n%s", want, human)
		}
	}
	for _, leak := range []string{"secret needle text", "hidden reasoning needle", "secret tool needle"} {
		if strings.Contains(human, leak) {
			t.Fatalf("human output leaked transcript content %q:\n%s", leak, human)
		}
	}

	// JSON output is machine-readable and likewise carries no block payloads.
	stdout.Reset()
	if err := a.run(context.Background(), []string{"archive", "inspect", archive, "--json"}); err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Format        string         `json:"format"`
		SchemaVersion string         `json:"schema_version"`
		Valid         bool           `json:"valid"`
		ID            string         `json:"id"`
		Messages      int            `json:"messages"`
		Blocks        map[string]int `json:"blocks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("inspect --json is not valid JSON: %v\n%s", err, stdout.String())
	}
	if summary.Format != "moirai.session" || !summary.Valid || summary.ID != "inspect-me" || summary.Messages != 2 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if summary.Blocks["text"] != 1 || summary.Blocks["thinking"] != 1 || summary.Blocks["tool_use"] != 1 {
		t.Fatalf("unexpected block counts: %#v", summary.Blocks)
	}
	for _, leak := range []string{"secret needle text", "hidden reasoning needle", "secret tool needle"} {
		if strings.Contains(stdout.String(), leak) {
			t.Fatalf("JSON output leaked transcript content %q:\n%s", leak, stdout.String())
		}
	}
}

func TestArchiveInspectRejectsTamperedAndOversized(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "session.json")
	archive := filepath.Join(dir, "session.moirai")
	if err := os.WriteFile(input, []byte(`{"id":"tamper","messages":[{"role":"user","content":"hello"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"archive", "create", input, "--out", archive}); err != nil {
		t.Fatal(err)
	}

	// Tampering with the stored transcript invalidates the digest.
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(data, []byte("hello"), []byte("HELLO"), 1)
	if err := os.WriteFile(archive, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.run(context.Background(), []string{"archive", "inspect", archive}); !errors.Is(err, moirai.ErrIntegrity) {
		t.Fatalf("tampered archive: err = %v, want ErrIntegrity", err)
	}

	// An oversized input is rejected by the existing limit before decoding.
	if err := a.run(context.Background(), []string{"archive", "inspect", archive, "--max-input-bytes", "16"}); !errors.Is(err, moirai.ErrLimitExceeded) {
		t.Fatalf("oversized archive: err = %v, want ErrLimitExceeded", err)
	}

	// An unsupported version returns the existing typed error.
	unsupported := filepath.Join(dir, "unsupported.moirai")
	if err := os.WriteFile(unsupported, []byte(`{"format":"moirai.session","version":"999","created_at":"2026-09-01T00:00:00Z","transcript":{},"sha256":"00"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.run(context.Background(), []string{"archive", "inspect", unsupported}); !errors.Is(err, moirai.ErrUnsupportedVersion) {
		t.Fatalf("unsupported version: err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestListFilterApply(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "app")
	elsewhere := filepath.Join(root, "other")
	refs := []moirai.SessionRef{
		{ID: "a", CWD: project, ModifiedAt: "2026-09-08T12:00:00Z"},
		{ID: "b", CWD: filepath.Join(project, "pkg", "sub"), ModifiedAt: "2026-09-07T12:00:00Z"},
		{ID: "c", CWD: project + "2", ModifiedAt: "2026-09-06T12:00:00Z"},
		{ID: "d", ModifiedAt: "2026-09-05T12:00:00Z"},
		{ID: "e", CWD: elsewhere, Timestamp: "2026-09-04T12:00:00Z"},
		{ID: "f", CWD: elsewhere, ModifiedAt: "not-a-time", Timestamp: "2026-09-03T12:00:00Z"},
		{ID: "g", CWD: elsewhere, ModifiedAt: "2026-09-02T12:00:00Z", Timestamp: "2026-01-01T00:00:00Z"},
		{ID: "h", CWD: elsewhere, ModifiedAt: "2026-09-01T12:00:00.123456789Z"},
		{ID: "i", CWD: elsewhere, ModifiedAt: "2026-08-31T17:30:00+05:30"},
		{ID: "j", CWD: elsewhere},
	}
	all := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	cases := []struct {
		name                     string
		cwd, since, until, limit string
		want                     []string
	}{
		{name: "no filter preserves order", want: all},
		{name: "cwd matches the exact directory", cwd: filepath.Join(project, "pkg", "sub"), want: []string{"b"}},
		{name: "cwd includes descendants", cwd: project, want: []string{"a", "b"}},
		{name: "cwd accepts a trailing separator", cwd: project + string(filepath.Separator), want: []string{"a", "b"}},
		{name: "cwd excludes a sibling sharing the prefix", cwd: project + "2", want: []string{"c"}},
		{name: "cwd excludes sessions without a working directory", cwd: root, want: []string{"a", "b", "c", "e", "f", "g", "h", "i", "j"}},
		{name: "since is inclusive", since: "2026-09-07T12:00:00Z", want: []string{"a", "b"}},
		{name: "until is inclusive", until: "2026-09-02T12:00:00Z", want: []string{"g", "h", "i"}},
		{name: "since and until form a window", since: "2026-09-03T00:00:00Z", until: "2026-09-06T00:00:00Z", want: []string{"d", "e"}},
		{name: "until at the zero instant is an active bound", until: "0001-01-01T00:00:00Z"},
		{name: "bounds fall back to timestamp and drop malformed or missing times", since: "0001-01-01T00:00:00Z", want: []string{"a", "b", "c", "d", "e", "g", "h", "i"}},
		{name: "modified_at wins over a conflicting timestamp", since: "2026-09-02T12:00:00Z", until: "2026-09-02T12:00:00Z", want: []string{"g"}},
		{name: "fractional seconds parse", since: "2026-09-01T12:00:00Z", until: "2026-09-01T13:00:00Z", want: []string{"h"}},
		{name: "offsets compare as instants", since: "2026-08-31T12:00:00Z", until: "2026-08-31T12:00:00Z", want: []string{"i"}},
		{name: "limit keeps the first entries in order", limit: "3", want: []string{"a", "b", "c"}},
		{name: "limit above the result count keeps everything", limit: "99", want: all},
		{name: "limit applies after the other filters", cwd: elsewhere, since: "2026-09-01T00:00:00Z", limit: "2", want: []string{"e", "g"}},
		{name: "nothing matching returns nil", cwd: filepath.Join(root, "missing")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter, err := parseListFilter(tc.cwd, tc.since, tc.until, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			got := filter.apply(refs)
			if tc.want == nil && got != nil {
				t.Fatalf("got %#v, want nil", got)
			}
			ids := make([]string, len(got))
			for i, ref := range got {
				ids[i] = ref.ID
			}
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("got %v, want %v", ids, tc.want)
			}
		})
	}
}

func TestListFilterParse(t *testing.T) {
	failures := []struct {
		name                     string
		cwd, since, until, limit string
		want                     string
	}{
		{name: "malformed since", since: "yesterday", want: "--since"},
		{name: "date-only until", until: "2026-09-08", want: "--until"},
		{name: "since after until", since: "2026-09-08T00:00:00Z", until: "2026-09-01T00:00:00Z", want: "--since must not be after --until"},
		{name: "since after the zero instant", since: "0001-01-01T00:00:01Z", until: "0001-01-01T00:00:00Z", want: "--since must not be after --until"},
		{name: "zero limit", limit: "0", want: "--limit"},
		{name: "negative limit", limit: "-1", want: "--limit"},
		{name: "non-numeric limit", limit: "abc", want: "--limit"},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseListFilter(tc.cwd, tc.since, tc.until, tc.limit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	filter, err := parseListFilter("", "", "", "")
	if err != nil || filter.cwd != "" || filter.since != nil || filter.until != nil || filter.limit != 0 {
		t.Fatalf("empty flags: filter = %#v, err = %v", filter, err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative := strings.Join([]string{"sub", "..", "x"}, string(filepath.Separator))
	filter, err = parseListFilter(relative, "2026-09-01T00:00:00Z", "2026-09-01T00:00:00Z", "5")
	if err != nil {
		t.Fatal(err)
	}
	if filter.cwd != filepath.Join(wd, "x") || filter.since == nil || filter.until == nil || !filter.since.Equal(*filter.until) || filter.limit != 5 {
		t.Fatalf("filter = %#v", filter)
	}
}

func TestListFiltersOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sessions := filepath.Join(home, "pi-sessions")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", sessions)
	work := t.TempDir()
	project := filepath.Join(work, "app")
	elsewhere := filepath.Join(work, "other")
	// Write order, lexical order, and modification order all differ so the
	// assertions below can only pass through the registry's newest-first sort.
	for _, s := range []struct {
		id, cwd  string
		modified time.Time
	}{
		{"gamma", project, time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)},
		{"beta", elsewhere, time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)},
		{"alpha", filepath.Join(project, "sub"), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)},
	} {
		header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": s.id, "timestamp": "2026-09-01T00:00:00Z", "cwd": s.cwd})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(sessions, "--"+s.id+"--")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, s.id+".jsonl")
		data := string(header) + "\n" + `{"type":"message","id":"m1","timestamp":"2026-09-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n"
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, s.modified, s.modified); err != nil {
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		a := app{out: &stdout, err: &stderr}
		if err := a.run(context.Background(), append([]string{"list", "--format", "pi"}, args...)); err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr.String())
		}
		return stdout.String()
	}
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"cwd", []string{"--cwd", project}, []string{"gamma", "alpha"}},
		{"window", []string{"--since", "2026-09-04T00:00:00Z", "--until", "2026-09-06T00:00:00Z"}, []string{"gamma"}},
		{"limit follows discovery order", []string{"--limit", "1"}, []string{"beta"}},
		{"combined", []string{"--cwd", project, "--since", "2026-09-02T00:00:00Z", "--limit", "1"}, []string{"gamma"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result struct {
				Sessions []moirai.SessionRef `json:"sessions"`
			}
			if err := json.Unmarshal([]byte(run(t, append(tc.args, "--json")...)), &result); err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, ref := range result.Sessions {
				ids = append(ids, ref.ID)
			}
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("got %v, want %v", ids, tc.want)
			}
		})
	}
	if out := run(t, "--cwd", filepath.Join(elsewhere, "nothing"), "--json"); !strings.Contains(out, `"sessions": null`) {
		t.Fatalf("zero-match JSON = %s", out)
	}
	lines := strings.Split(strings.TrimSpace(run(t, "--cwd", elsewhere)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "beta") {
		t.Fatalf("human output = %q", lines)
	}
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	err := a.run(context.Background(), []string{"list", "--format", "pi", "--limit", "0"})
	if err == nil || !strings.Contains(err.Error(), "--limit") || stdout.Len() != 0 {
		t.Fatalf("err = %v, stdout = %q", err, stdout.String())
	}
}
