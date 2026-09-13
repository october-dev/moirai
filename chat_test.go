package moirai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func equalChatJSON(t *testing.T, a, b []byte) {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("JSON changed:\n%s\n%s", a, b)
	}
}
func TestChatRoundTripArchive(t *testing.T) {
	fixture, err := os.ReadFile("testdata/native/chat.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{fixture, []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":""},{"role":"assistant","content":"x"}]}`), []byte(`{"model":"m","messages":[]}`)} {
		p, err := (ChatCodec{}).Parse(data, ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Transcript.Meta.Timestamp != "" && string(data) != string(fixture) {
			t.Fatal("invented timestamp")
		}
		if p.Transcript.Meta.CWD != "" || p.Transcript.Meta.Provenance != nil {
			t.Fatal("invented agent state")
		}
		archive, err := EncodeArchive(p.Transcript, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeArchive(archive, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		simple, err := (SimpleCodec{}).Render(decoded, RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := (SimpleCodec{}).Parse(simple.Data, ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		r, err := (ChatCodec{}).Render(parsed.Transcript, RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Warnings) != 0 {
			t.Fatal(r.Warnings)
		}
		equalChatJSON(t, data, r.Data)
	}
}
func TestChatCanonicalEditsAreNotOverridden(t *testing.T) {
	p, err := (ChatCodec{}).Parse([]byte(`{"model":"m","messages":[{"id":"u","role":"user","content":"before"}]}`), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Transcript.Messages[0].Content[0].Text = "after"
	r, err := (ChatCodec{}).Render(p.Transcript, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	equalChatJSON(t, []byte(`{"model":"m","messages":[{"id":"u","role":"user","content":"after"}]}`), r.Data)
}
func TestChatDoesNotInventNativeMetadata(t *testing.T) {
	p, err := (ChatCodec{}).Parse([]byte(`{"messages":[{"role":"assistant","content":"Answer"}]}`), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Codec{ClaudeCodeCodec{}, CodexCodec{}, OpenCodeCodec{}, PiCodec{format: FormatPi}, HermesCodec{}} {
		r, err := c.Render(p.Transcript, RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, invented := range []string{`"unknown"`, `"openai"`, `"step-start"`, `"end_turn"`} {
			if strings.Contains(string(r.Data), invented) {
				t.Fatalf("%s invented %s: %s", c.Format(), invented, r.Data)
			}
		}
	}
	if nativeMillis(p.Transcript, "") != nil || nativeSeconds(p.Transcript, "") != nil {
		t.Fatal("invented date")
	}
	if _, err := (ChatCodec{}).Parse([]byte(`{"id":"","messages":[]}`), ParseOptions{}); err == nil {
		t.Fatal("empty source ID accepted")
	}
}

func TestChatDegradationAndAgentRender(t *testing.T) {
	p, err := (ChatCodec{}).Parse([]byte(`{"model":"m","messages":[{"role":"system","content":"Instruction"},{"role":"assistant","content":"Answer"}]}`), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range Formats {
		c, err := DefaultRegistry.Codec(f)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Info().Capability.Write || f == FormatChat || f == FormatSimple || f == FormatConcord {
			continue
		}
		r, err := c.Render(p.Transcript, RenderOptions{})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		found := false
		for _, w := range r.Warnings {
			if w.Code == "system_role_flattened" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s missing role warning", f)
		}
	}
	if p.Transcript.Messages[0].Role != RoleSystem {
		t.Fatal("render mutated source")
	}
	p.Transcript.Meta.CWD = "/source"
	p.Transcript.Messages[1].Content = append(p.Transcript.Messages[1].Content, Block{Type: BlockThinking, Text: "Reasoning"}, Block{Type: BlockToolUse, ID: "t1", Name: "shell", Input: json.RawMessage(`{}`)})
	r, err := (ChatCodec{}).Render(p.Transcript, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 3 {
		t.Fatalf("expected metadata and 2 block warnings: %+v", r.Warnings)
	}
	back, err := (ChatCodec{}).Parse(r.Data, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Transcript.Messages) != 2 || len(back.Transcript.Messages[1].Content) != 1 {
		t.Fatal("message order/visible text changed")
	}
}
func TestChatStoreAndLimits(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOIRAI_CHAT_DIR", root)
	registry, err := DefaultStores()
	if err != nil {
		t.Fatal(err)
	}
	store, err := registry.Store(FormatChat)
	if err != nil {
		t.Fatal(err)
	}
	p, err := (ChatCodec{}).Parse([]byte(`{"id":"chat-test","model":"m","messages":[{"role":"user","content":"hi"}]}`), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(context.Background(), p.Transcript, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.Discover(context.Background())
	if err != nil || len(refs) != 1 {
		t.Fatalf("%v %v", refs, err)
	}
	loaded, err := store.Load(context.Background(), saved.Ref, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Transcript.Messages[0].Content[0].Text != "hi" {
		t.Fatal("bad store data")
	}
	if _, err := os.Stat(filepath.Join(root, saved.Ref.Location)); err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxInputBytes = 5
	if _, err := (ChatCodec{}).Parse([]byte(`{"model":"m","messages":[]}`), ParseOptions{Limits: limits}); err == nil {
		t.Fatal("input limit bypass")
	}
	for _, data := range []string{`{"messages":null}`, `{"messages":[{"role":"tool","content":"hi"}]}`, `{"messages":[{"role":"user","content":null}]}`, `{"messages":[{"role":"user","content":"hi","usage":{"prompt_tokens":-1}}]}`} {
		if _, err := (ChatCodec{}).Parse([]byte(data), ParseOptions{}); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	p.Transcript.Messages[0].Role = RoleSystem
	p.Transcript.SchemaVersion = SchemaVersion
	if Validate(p.Transcript, DefaultLimits()) == nil {
		t.Fatal("1.0 accepted system role")
	}
}
