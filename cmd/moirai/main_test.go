package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
