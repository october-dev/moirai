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

func TestHumanOutputScrubsTerminalControls(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
