package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
