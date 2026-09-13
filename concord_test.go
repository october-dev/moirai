package moirai

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConcordRoundTripAndStore(t *testing.T) {
	data, err := os.ReadFile("testdata/native/concord.json")
	if err != nil {
		t.Fatal(err)
	}
	var objects []json.RawMessage
	_ = json.Unmarshal(data, &objects)
	for _, input := range [][]byte{data, objects[0]} {
		f, err := DetectFormat(input)
		if err != nil || f != FormatConcord {
			t.Fatalf("detect %s %v", f, err)
		}
		p, err := (ConcordCodec{}).Parse(input, ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		archive, err := EncodeArchive(p.Transcript, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		back, err := DecodeArchive(archive, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		r, err := (ConcordCodec{}).Render(back, RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Warnings) != 0 {
			t.Fatal(r.Warnings)
		}
		equalChatJSON(t, input, r.Data)
		canonical, err := Convert(input, FormatConcord, FormatSimple)
		if err != nil {
			t.Fatal(err)
		}
		r, err = Convert(canonical.Data, FormatSimple, FormatConcord)
		if err != nil {
			t.Fatal(err)
		}
		equalChatJSON(t, input, r.Data)
	}
	var items []concordConversation
	_ = json.Unmarshal(data, &items)
	second := items[0]
	second.ID = "55555555-5555-4555-8555-555555555555"
	second.Title = "Second"
	items = append(items, second)
	multi, _ := json.Marshal(items)
	root := t.TempDir()
	db := filepath.Join(root, "conversations.json")
	if err := os.WriteFile(db, multi, 0600); err != nil {
		t.Fatal(err)
	}
	store := &ConcordStore{Path: db, ImportDir: filepath.Join(root, "imports")}
	refs, err := store.Discover(context.Background())
	if err != nil || len(refs) != 2 {
		t.Fatalf("%v %v", refs, err)
	}
	p, err := store.Load(context.Background(), refs[1], ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Transcript.Meta.Title != "Second" {
		t.Fatal("wrong conversation")
	}
	saved, err := store.Save(context.Background(), p.Transcript, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Warnings) == 0 || saved.Warnings[len(saved.Warnings)-1].Code != "concord_import_pending" {
		t.Fatal("missing pending import notice")
	}
	if _, err := os.Stat(filepath.Join(store.ImportDir, saved.Ref.Location)); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.ReadFile(db)
	if !bytes.Equal(unchanged, multi) {
		t.Fatal("source Concord database changed")
	}
	if _, err := (ConcordCodec{}).Parse(multi, ParseOptions{}); err == nil {
		t.Fatal("ambiguous database silently picked a conversation")
	}
	if _, err := store.Save(context.Background(), p.Transcript, RenderOptions{}); err == nil {
		t.Fatal("overwrote import file")
	}
	if err := store.Delete(context.Background(), refs[0]); err != ErrUnsupported {
		t.Fatal("Concord deletion must remain app-owned")
	}
}
func TestConcordDegradation(t *testing.T) {
	p, err := (ChatCodec{}).Parse([]byte(`{"model":"m","messages":[{"role":"system","content":"Instruction"},{"role":"assistant","content":"Answer"}]}`), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Transcript.Messages[1].Content = append(p.Transcript.Messages[1].Content, Block{Type: BlockThinking, Text: "R"}, Block{Type: BlockToolUse, ID: "call", Name: "Read", Input: json.RawMessage(`{"file":"example"}`)})
	r, err := (ConcordCodec{}).Render(p.Transcript, RenderOptions{Now: func() string { return "2026-09-01T12:00:00Z" }})
	if err != nil {
		t.Fatal(err)
	}
	back, err := (ConcordCodec{}).Parse(r.Data, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if back.Transcript.Messages[0].Role != RoleSystem || len(back.Transcript.Messages) != 2 {
		t.Fatal("Concord roles/order changed")
	}
	if back.Transcript.Meta.ModelProvider != "" {
		t.Fatal("invented gateway")
	}
	for _, code := range []string{"destination_id_created", "destination_time_created", "provider_selection_required", "chat_block_flattened"} {
		found := false
		for _, w := range r.Warnings {
			if w.Code == code {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s", code)
		}
	}
}
