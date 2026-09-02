package moirai

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocalFileStoreLifecycle(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalFileStore(FormatSimple, root, ".json", flatJSONLayout)
	if err != nil {
		t.Fatal(err)
	}
	transcript := fixtureTranscript()
	transcript.Meta.ID = "safe-session"
	saved, err := store.Save(context.Background(), transcript, RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Created || saved.Ref.Location != "safe-session.json" {
		t.Fatalf("saved = %#v", saved)
	}
	if _, err := store.Save(context.Background(), transcript, RenderOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate save error = %v", err)
	}
	path := filepath.Join(root, saved.Ref.Location)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o", info.Mode().Perm())
		}
	}
	refs, err := store.Discover(context.Background())
	if err != nil || len(refs) != 1 {
		t.Fatalf("discover = %#v, %v", refs, err)
	}
	loaded, err := store.Load(context.Background(), refs[0], ParseOptions{Limits: DefaultLimits()})
	if err != nil || loaded.Transcript.Meta.ID != "safe-session" {
		t.Fatalf("load = %#v, %v", loaded, err)
	}
	if err := store.Delete(context.Background(), refs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file stat = %v", err)
	}
}

func TestDefaultStoresHonorClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	for _, test := range []struct {
		name       string
		configDir  string
		wantedRoot string
	}{
		{name: "default", wantedRoot: filepath.Join(home, ".claude", "projects")},
		{name: "override", configDir: filepath.Join(home, "claude-custom"), wantedRoot: filepath.Join(home, "claude-custom", "projects")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", test.configDir)
			registry, err := DefaultStores()
			if err != nil {
				t.Fatal(err)
			}
			store, err := registry.Store(FormatClaudeCode)
			if err != nil {
				t.Fatal(err)
			}
			if store.Root() != test.wantedRoot {
				t.Fatalf("Claude root = %q, want %q", store.Root(), test.wantedRoot)
			}
		})
	}
}

func TestStoreRejectsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if _, err := checkedPath(root, "../outside", false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("traversal error = %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedPath(root, filepath.Join("link", "session.json"), false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestSafeID(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../x", "a/b", `a\\b`, "a:b", "bad\nname"} {
		if !errors.Is(safeID(id), ErrUnsafePath) {
			t.Errorf("safeID(%q) accepted", id)
		}
	}
	if err := safeID("session-123"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreReadLimit(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalFileStore(FormatSimple, root, ".json", flatJSONLayout)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"id":"bounded","messages":[{"role":"user","content":"hello"}]}`)
	if err := os.WriteFile(filepath.Join(root, "bounded.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxInputBytes = 10
	_, err = store.Load(context.Background(), SessionRef{Format: FormatSimple, ID: "bounded", Location: "bounded.json"}, ParseOptions{Limits: limits})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("load error = %v", err)
	}
}

func TestBundleReadRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "summary.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := readBundleJSON(dir, "summary.json", false, DefaultLimits()); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("read error = %v", err)
	}
}
