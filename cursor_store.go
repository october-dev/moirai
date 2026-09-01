package moirai

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type CursorStore struct{ ChatsRoot string }

func (s *CursorStore) Format() Format { return FormatCursor }
func (s *CursorStore) Root() string   { return s.ChatsRoot }

func (s *CursorStore) Discover(ctx context.Context) ([]SessionRef, error) {
	workspaces, err := os.ReadDir(s.ChatsRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []SessionRef
	for _, workspace := range workspaces {
		if !workspace.IsDir() || workspace.Type()&os.ModeSymlink != 0 {
			continue
		}
		sessions, _ := os.ReadDir(filepath.Join(s.ChatsRoot, workspace.Name()))
		for _, session := range sessions {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !session.IsDir() || session.Type()&os.ModeSymlink != 0 {
				continue
			}
			dbPath := filepath.Join(s.ChatsRoot, workspace.Name(), session.Name(), "store.db")
			if !fileExists(dbPath) {
				continue
			}
			ref := SessionRef{Format: FormatCursor, ID: session.Name(), Location: filepath.Join(workspace.Name(), session.Name(), "store.db")}
			parsed, loadErr := s.Load(ctx, ref, ParseOptions{Limits: DefaultLimits()})
			if loadErr != nil {
				continue
			}
			info, _ := os.Stat(dbPath)
			meta := parsed.Transcript.Meta
			ref.ID, ref.Title, ref.CWD, ref.Model, ref.Timestamp, ref.ModifiedAt = meta.ID, meta.Title, meta.CWD, meta.Model, meta.Timestamp, fileModified(info)
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func (s *CursorStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	path, err := checkedPath(s.ChatsRoot, ref.Location, true)
	if err != nil {
		return nil, err
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	blobRows, err := queryObjects(ctx, db, `SELECT id, data FROM blobs ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	metaRows, _ := queryObjects(ctx, db, `SELECT key, value FROM meta ORDER BY key`)
	blobs := make([]any, len(blobRows))
	for i, row := range blobRows {
		data, _ := row["data"].([]byte)
		blobs[i] = map[string]any{"id": row["id"], "data": hex.EncodeToString(data)}
	}
	meta := make([]any, len(metaRows))
	for i, row := range metaRows {
		meta[i] = map[string]any{"key": row["key"], "value": fmt.Sprint(row["value"])}
	}
	body := map[string]any{"blobs": blobs, "meta": meta}
	if sidecar, readErr := readFileLimited(filepath.Join(filepath.Dir(path), "meta.json"), opts.Limits.normalized().MaxInputBytes); readErr == nil {
		var value any
		if decodeJSONDocument(sidecar, &value, opts.Limits) == nil {
			body["session_meta"] = value
		}
	}
	data, _ := json.Marshal(body)
	parsed, err := (CursorCodec{}).Parse(data, opts)
	if err == nil && parsed.Transcript.Meta.ID == "" {
		parsed.Transcript.Meta.ID = filepath.Base(filepath.Dir(path))
	}
	return parsed, err
}

func (s *CursorStore) Save(ctx context.Context, transcript *Transcript, opts RenderOptions) (*SavedSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, transcript.Meta.ID)
	if err := safeID(id); err != nil {
		return nil, err
	}
	rendered, err := (CursorCodec{}).Render(transcript, RenderOptions{Limits: opts.Limits, ID: id})
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(rendered.Data, &body); err != nil {
		return nil, err
	}
	hash := md5.Sum([]byte(transcript.Meta.CWD))
	workspace := hex.EncodeToString(hash[:])
	workspaceDir := filepath.Join(s.ChatsRoot, workspace)
	target := filepath.Join(workspaceDir, id)
	if _, err := os.Stat(target); err == nil {
		return nil, ErrSessionExists
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(workspaceDir, ".moirai-cursor-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	dbPath := filepath.Join(stage, "store.db")
	db, err := openSQLite(dbPath, false)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB); CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		tx.Rollback()
		db.Close()
		return nil, err
	}
	for _, raw := range array(body["blobs"]) {
		row := object(raw)
		data, _ := hex.DecodeString(stringValue(row["data"]))
		if _, err := tx.ExecContext(ctx, `INSERT INTO blobs (id,data) VALUES (?,?)`, row["id"], data); err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
	}
	for _, raw := range array(body["meta"]) {
		row := object(raw)
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key,value) VALUES (?,?)`, row["key"], row["value"]); err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return nil, err
	}
	db.Close()
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return nil, err
	}
	if value := body["session_meta"]; value != nil {
		if err := writeBundleValue(stage, "meta.json", value); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(stage, target); err != nil {
		return nil, err
	}
	return &SavedSession{Ref: SessionRef{Format: FormatCursor, ID: id, Location: filepath.Join(workspace, id, "store.db"), Title: transcript.Meta.Title, CWD: transcript.Meta.CWD, Model: transcript.Meta.Model, Timestamp: transcript.Meta.Timestamp}, Created: true, Warnings: rendered.Warnings}, nil
}

func (s *CursorStore) Delete(ctx context.Context, ref SessionRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := checkedPath(s.ChatsRoot, ref.Location, true)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	root, resolveErr := filepath.EvalSymlinks(s.ChatsRoot)
	if resolveErr != nil {
		return resolveErr
	}
	rel, err := filepath.Rel(root, directory)
	if err != nil || len(strings.Split(rel, string(filepath.Separator))) != 2 || filepath.Base(path) != "store.db" {
		return ErrUnsafePath
	}
	return os.RemoveAll(directory)
}
