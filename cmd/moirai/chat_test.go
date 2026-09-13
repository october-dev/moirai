package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	moirai "github.com/october-dev/moirai"
)

func TestChatCLIEndToEnd(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOIRAI_CHAT_DIR", filepath.Join(root, "chats"))
	t.Setenv("MOIRAI_CONCORD_IMPORT_DIR", filepath.Join(root, "concord-imports"))
	t.Setenv("CONCORD_CONVERSATIONS_FILE", filepath.Join(root, "concord-source.json"))
	input := filepath.Join(root, "input.json")
	source := []byte(`{"id":"source-chat","model":"m","messages":[{"role":"system","content":"Be precise."},{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi"}]}`)
	if err := os.WriteFile(input, source, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	a := app{out: &out, err: &errs, launch: func(context.Context, moirai.LaunchCommand) error { t.Fatal("chat launched a process"); return nil }}
	run := func(args ...string) {
		t.Helper()
		out.Reset()
		errs.Reset()
		if err := a.run(context.Background(), args); err != nil {
			t.Fatalf("%v: %v %s", args, err, errs.String())
		}
	}
	canonical := filepath.Join(root, "canonical.json")
	roundtrip := filepath.Join(root, "back.json")
	archive := filepath.Join(root, "chat.moirai")
	run("convert", input, "--to", "simple", "--out", canonical)
	run("convert", canonical, "--to", "chat", "--out", roundtrip)
	data, err := os.ReadFile(roundtrip)
	if err != nil {
		t.Fatal(err)
	}
	var x, y any
	_ = json.Unmarshal(source, &x)
	_ = json.Unmarshal(data, &y)
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("roundtrip changed: %s", data)
	}
	run("archive", "create", input, "--out", archive)
	run("archive", "verify", archive)
	run("continue", archive, "--with", "chat")
	var saved moirai.SavedSession
	if err := json.Unmarshal(out.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Ref.CWD != "" {
		t.Fatal("invented workspace")
	}
	run("list", "--format", "chat", "--json")
	var result struct {
		Sessions []moirai.SessionRef `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("missing saved chat: %s", out.String())
	}
	run("continue", archive, "--with", "concord", "--no-launch")
	files, err := filepath.Glob(filepath.Join(root, "concord-imports", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("Concord import not staged: %v %v", files, err)
	}
	staged, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	concord, err := (moirai.ConcordCodec{}).Parse(staged, moirai.ParseOptions{})
	if err != nil || len(concord.Transcript.Messages) != 3 {
		t.Fatalf("invalid Concord handoff: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "concord-source.json")); !os.IsNotExist(err) {
		t.Fatal("Concord source was touched")
	}
	unchanged, _ := os.ReadFile(input)
	if !bytes.Equal(source, unchanged) {
		t.Fatal("source changed")
	}
}
