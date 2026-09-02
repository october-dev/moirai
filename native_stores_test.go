package moirai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBundleStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	for _, item := range []struct {
		format Format
		match  BundleMatch
		read   BundleRead
		write  BundleWrite
		layout FileLayout
	}{
		{FormatGrok, func(dir string) bool { return fileExists(filepath.Join(dir, "updates.jsonl")) }, grokBundleRead, grokBundleWrite, grokBundleLayout},
		{FormatFX, func(dir string) bool { return fileExists(filepath.Join(dir, "events.jsonl")) }, fxBundleRead, fxBundleWrite, flatBundleLayout},
	} {
		t.Run(string(item.format), func(t *testing.T) {
			store, err := NewBundleStore(item.format, t.TempDir(), item.match, item.read, item.write, item.layout)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			refs, err := store.Discover(ctx)
			if err != nil || len(refs) != 1 {
				t.Fatalf("discover = %#v, %v", refs, err)
			}
			parsed, err := store.Load(ctx, refs[0], ParseOptions{Limits: DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Transcript.Messages) < 4 {
				t.Fatalf("messages = %d", len(parsed.Transcript.Messages))
			}
			if err := store.Delete(ctx, saved.Ref); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBundleStoreReservationLocks(t *testing.T) {
	newStore := func(t *testing.T, root string) *BundleStore {
		t.Helper()
		store, err := NewBundleStore(FormatFX, root, func(dir string) bool {
			return fileExists(filepath.Join(dir, "events.jsonl"))
		}, fxBundleRead, fxBundleWrite, flatBundleLayout)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}

	t.Run("stale lock is recovered", func(t *testing.T) {
		root := t.TempDir()
		transcript := nativeFixture(t)
		lock := filepath.Join(root, transcript.Meta.ID) + ".moirai-create"
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(lock, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := newStore(t, root).Save(context.Background(), transcript, RenderOptions{Limits: DefaultLimits()}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale lock remains: %v", err)
		}
	})

	t.Run("fresh lock is preserved", func(t *testing.T) {
		root := t.TempDir()
		transcript := nativeFixture(t)
		lock := filepath.Join(root, transcript.Meta.ID) + ".moirai-create"
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newStore(t, root).Save(context.Background(), transcript, RenderOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrSessionExists) {
			t.Fatalf("fresh lock error = %v", err)
		}
		if _, err := os.Lstat(lock); err != nil {
			t.Fatalf("fresh lock removed: %v", err)
		}
	})
}

func TestAntigravityStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	store := &AntigravityStore{DataRoot: t.TempDir()}
	saved, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(store.DataRoot, saved.Ref.Location)
	var version int
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if version != 1 {
		t.Fatalf("user_version = %d", version)
	}
	refs, err := store.Discover(ctx)
	if err != nil || len(refs) != 1 {
		t.Fatalf("discover = %#v, %v", refs, err)
	}
	parsed, err := store.Load(ctx, refs[0], ParseOptions{Limits: DefaultLimits()})
	if err != nil || len(parsed.Transcript.Messages) < 4 {
		t.Fatalf("load messages = %d, %v", len(parsed.Transcript.Messages), err)
	}
	if _, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate save error = %v", err)
	}
	if err := store.Delete(ctx, saved.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted db stat = %v", err)
	}
}

func TestCursorStoresLifecycle(t *testing.T) {
	ctx := context.Background()
	t.Run("agent", func(t *testing.T) {
		store := &CursorStore{ChatsRoot: t.TempDir()}
		saved, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()})
		if err != nil {
			t.Fatal(err)
		}
		refs, err := store.Discover(ctx)
		if err != nil || len(refs) != 1 {
			t.Fatalf("discover = %#v, %v", refs, err)
		}
		parsed, err := store.Load(ctx, refs[0], ParseOptions{Limits: DefaultLimits()})
		if err != nil || len(parsed.Transcript.Messages) < 4 {
			t.Fatalf("load messages = %d, %v", len(parsed.Transcript.Messages), err)
		}
		if _, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrSessionExists) {
			t.Fatalf("duplicate save error = %v", err)
		}
		if err := store.Delete(ctx, saved.Ref); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("desktop", func(t *testing.T) {
		store := &CursorDesktopStore{UserDir: t.TempDir()}
		saved, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()})
		if err != nil {
			t.Fatal(err)
		}
		refs, err := store.Discover(ctx)
		if err != nil || len(refs) != 1 {
			t.Fatalf("discover = %#v, %v", refs, err)
		}
		parsed, err := store.Load(ctx, refs[0], ParseOptions{Limits: DefaultLimits()})
		if err != nil || len(parsed.Transcript.Messages) < 4 {
			t.Fatalf("load messages = %d, %v", len(parsed.Transcript.Messages), err)
		}
		if _, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrSessionExists) {
			t.Fatalf("duplicate save error = %v", err)
		}
		if err := store.Delete(ctx, saved.Ref); err != nil {
			t.Fatal(err)
		}
		db, err := openSQLite(store.dbPath(), true)
		if err != nil {
			t.Fatal(err)
		}
		var encoded string
		if err := db.QueryRow(`SELECT CAST(value AS TEXT) FROM ItemTable WHERE key = ?`, "glass.localAgentProjectMembership.v1").Scan(&encoded); err != nil {
			db.Close()
			t.Fatal(err)
		}
		db.Close()
		membership := map[string]any{}
		if err := json.Unmarshal([]byte(encoded), &membership); err != nil {
			t.Fatal(err)
		}
		if _, exists := membership[saved.Ref.ID]; exists {
			t.Fatal("deleted session remains in Cursor sidebar membership")
		}
	})
}

func TestNativeFileStoresLifecycle(t *testing.T) {
	ctx := context.Background()
	for _, item := range []struct {
		format Format
		layout FileLayout
	}{
		{FormatClaudeCode, claudeLayout},
		{FormatCodex, codexLayout},
		{FormatPi, piLayout},
		{FormatCampfire, piLayout},
	} {
		t.Run(string(item.format), func(t *testing.T) {
			store, err := NewLocalFileStore(item.format, t.TempDir(), ".jsonl", item.layout)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()})
			if err != nil {
				t.Fatal(err)
			}
			refs, err := store.Discover(ctx)
			if err != nil || len(refs) != 1 {
				t.Fatalf("discover = %#v, %v", refs, err)
			}
			parsed, err := store.Load(ctx, refs[0], ParseOptions{Limits: DefaultLimits()})
			if err != nil || len(parsed.Transcript.Messages) < 4 {
				t.Fatalf("load = %#v, %v", parsed, err)
			}
			if _, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrSessionExists) {
				t.Fatalf("duplicate save error = %v", err)
			}
			if err := store.Delete(ctx, saved.Ref); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCoworkStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	account := filepath.Join(root, "11111111-1111-4111-a111-111111111111", "22222222-2222-4222-a222-222222222222")
	if err := os.MkdirAll(account, 0o700); err != nil {
		t.Fatal(err)
	}
	store := &CoworkStore{SessionsRoot: root}
	saved, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := store.Discover(ctx)
	if err != nil || len(refs) != 1 {
		t.Fatalf("discover = %#v, %v", refs, err)
	}
	parsed, err := store.Load(ctx, refs[0], ParseOptions{Limits: DefaultLimits()})
	if err != nil || len(parsed.Transcript.Messages) < 4 {
		t.Fatalf("load = %#v, %v", parsed, err)
	}
	if _, err := store.Save(ctx, nativeFixture(t), RenderOptions{Limits: DefaultLimits()}); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate save error = %v", err)
	}
	if err := store.Delete(ctx, saved.Ref); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSourceStores(t *testing.T) {
	ctx := context.Background()
	t.Run("opencode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "opencode.db")
		db, err := openSQLite(path, false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`CREATE TABLE session (id TEXT, directory TEXT, title TEXT, version TEXT, time_created INTEGER, time_updated INTEGER, time_archived INTEGER, model TEXT);
CREATE TABLE message (id TEXT, session_id TEXT, time_created INTEGER, data TEXT);
CREATE TABLE part (id TEXT, message_id TEXT, session_id TEXT, data TEXT);
INSERT INTO session VALUES ('ses_1','/repo','Test','1',1000,2000,NULL,'{"id":"model-a"}');
INSERT INTO message VALUES ('msg_1','ses_1',1000,'{"id":"msg_1","role":"user"}');
INSERT INTO part VALUES ('part_1','msg_1','ses_1','{"type":"text","text":"hello"}');`)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		store := &OpenCodeStore{DBPath: path}
		refs, err := store.Discover(ctx)
		if err != nil || len(refs) != 1 {
			t.Fatalf("discover = %#v, %v", refs, err)
		}
		parsed, err := store.Load(ctx, refs[0], ParseOptions{Limits: DefaultLimits()})
		if err != nil || len(parsed.Transcript.Messages) != 1 {
			t.Fatalf("load = %#v, %v", parsed, err)
		}
		if err := store.Delete(ctx, refs[0]); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("hermes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		db, err := openSQLite(path, false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`CREATE TABLE sessions (id TEXT, model TEXT, started_at REAL, cwd TEXT, git_branch TEXT, title TEXT, archived INTEGER);
CREATE TABLE messages (id INTEGER, session_id TEXT, role TEXT, content TEXT, timestamp REAL, active INTEGER);
INSERT INTO sessions VALUES ('h1','model-a',1,'/repo','main','Test',0);
INSERT INTO messages VALUES (1,'h1','user','hello',1,1);`)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		store := &HermesStore{DBPath: path}
		refs, err := store.Discover(ctx)
		if err != nil || len(refs) != 1 {
			t.Fatalf("discover = %#v, %v", refs, err)
		}
		parsed, err := store.Load(ctx, refs[0], ParseOptions{Limits: DefaultLimits()})
		if err != nil || len(parsed.Transcript.Messages) != 1 {
			t.Fatalf("load = %#v, %v", parsed, err)
		}
	})
}
