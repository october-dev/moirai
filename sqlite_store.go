package moirai

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	existed := fileExists(path)
	mode := "rwc"
	pragmas := "&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if readOnly {
		mode = "ro"
		pragmas = "&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	}
	dsn := "file:" + filepath.ToSlash(path) + "?mode=" + mode + pragmas
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if !readOnly && !existed {
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func queryObjects(ctx context.Context, db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		object := make(map[string]any, len(columns))
		for i, value := range values {
			if bytes, ok := value.([]byte); ok {
				object[columns[i]] = append([]byte(nil), bytes...)
			} else {
				object[columns[i]] = value
			}
		}
		result = append(result, object)
	}
	return result, rows.Err()
}

func jsonValue(value any) any {
	switch value := value.(type) {
	case []byte:
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			return decoded
		}
		return string(value)
	case string:
		var decoded any
		if json.Unmarshal([]byte(value), &decoded) == nil {
			return decoded
		}
		return value
	default:
		return value
	}
}

type OpenCodeStore struct{ DBPath string }

func (s *OpenCodeStore) Format() Format { return FormatOpenCode }
func (s *OpenCodeStore) Root() string   { return s.DBPath }

func (s *OpenCodeStore) Discover(ctx context.Context) ([]SessionRef, error) {
	if _, err := os.Stat(s.DBPath); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	db, err := openSQLite(s.DBPath, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := queryObjects(ctx, db, `SELECT id, directory, title, version, time_created, model FROM session WHERE time_archived IS NULL ORDER BY time_created DESC`)
	if err != nil {
		return nil, err
	}
	refs := make([]SessionRef, 0, len(rows))
	for _, row := range rows {
		id := fmt.Sprint(row["id"])
		model := ""
		if value := object(jsonValue(row["model"])); len(value) > 0 {
			model = firstNonEmpty(stringValue(value["id"]), stringValue(value["modelID"]))
		}
		title := fmt.Sprint(row["title"])
		if strings.HasPrefix(title, "New session - ") {
			title = ""
		}
		refs = append(refs, SessionRef{Format: FormatOpenCode, ID: id, Location: id, CWD: fmt.Sprint(row["directory"]), Title: title, Model: model, Timestamp: timestampValue(row["time_created"])})
	}
	return refs, nil
}

func (s *OpenCodeStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	if err := safeID(ref.ID); err != nil {
		return nil, err
	}
	db, err := openSQLite(s.DBPath, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sessions, err := queryObjects(ctx, db, `SELECT id, directory, title, version, time_created, time_updated, model FROM session WHERE id = ?`, ref.ID)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, ErrSessionNotFound
	}
	session := sessions[0]
	info := map[string]any{"id": session["id"], "directory": session["directory"], "title": session["title"], "version": session["version"], "time": map[string]any{"created": session["time_created"], "updated": session["time_updated"]}, "model": jsonValue(session["model"])}
	messages, err := queryObjects(ctx, db, `SELECT id, data, time_created FROM message WHERE session_id = ? ORDER BY time_created, id`, ref.ID)
	if err != nil {
		return nil, err
	}
	parts, err := queryObjects(ctx, db, `SELECT message_id, data FROM part WHERE session_id = ? ORDER BY message_id, id`, ref.ID)
	if err != nil {
		return nil, err
	}
	partsByMessage := map[string][]any{}
	for _, part := range parts {
		id := fmt.Sprint(part["message_id"])
		value := jsonValue(part["data"])
		if _, ok := value.(map[string]any); ok {
			partsByMessage[id] = append(partsByMessage[id], value)
		}
	}
	var records []any
	for _, message := range messages {
		id := fmt.Sprint(message["id"])
		messageInfo := object(jsonValue(message["data"]))
		if messageInfo == nil {
			continue
		}
		if messageInfo["time"] == nil {
			messageInfo["time"] = map[string]any{"created": message["time_created"], "completed": message["time_created"]}
		}
		records = append(records, map[string]any{"info": messageInfo, "parts": partsByMessage[id]})
	}
	data, _ := json.Marshal(map[string]any{"info": info, "messages": records})
	return (OpenCodeCodec{}).Parse(data, opts)
}

func (s *OpenCodeStore) Save(ctx context.Context, transcript *Transcript, opts RenderOptions) (*SavedSession, error) {
	rendered, err := (OpenCodeCodec{}).Render(transcript, opts)
	if err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp("", "moirai-opencode-import-*.json")
	if err != nil {
		return nil, err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return nil, err
	}
	if _, err := temp.Write(rendered.Data); err != nil {
		temp.Close()
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "opencode", "import", name)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("opencode import: %w: %s", err, strings.TrimSpace(string(output)))
	}
	id := firstNonEmpty(opts.ID, transcript.Meta.ID)
	if !strings.HasPrefix(id, "ses") {
		id = "ses_" + strings.ReplaceAll(id, "-", "")
	}
	for _, line := range strings.Split(string(output), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Imported session: "); ok {
			id = strings.TrimSpace(value)
		}
	}
	return &SavedSession{Ref: SessionRef{Format: FormatOpenCode, ID: id, Location: id, Title: transcript.Meta.Title, CWD: transcript.Meta.CWD, Model: transcript.Meta.Model, Timestamp: transcript.Meta.Timestamp}, Created: true, Warnings: rendered.Warnings}, nil
}

func (s *OpenCodeStore) Delete(ctx context.Context, ref SessionRef) error {
	if err := safeID(ref.ID); err != nil {
		return err
	}
	db, err := openSQLite(s.DBPath, false)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `UPDATE session SET time_archived = ? WHERE id = ? AND time_archived IS NULL`, time.Now().UTC().UnixMilli(), ref.ID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return ErrSessionNotFound
	}
	return nil
}

type HermesStore struct{ DBPath string }

func (s *HermesStore) Format() Format { return FormatHermes }
func (s *HermesStore) Root() string   { return s.DBPath }
func (s *HermesStore) Discover(ctx context.Context) ([]SessionRef, error) {
	if _, err := os.Stat(s.DBPath); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	db, err := openSQLite(s.DBPath, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := queryObjects(ctx, db, `SELECT * FROM sessions`)
	if err != nil {
		return nil, err
	}
	var refs []SessionRef
	for _, row := range rows {
		if integerValue(row["archived"]) == 1 {
			continue
		}
		id := fmt.Sprint(row["id"])
		if id == "" {
			continue
		}
		refs = append(refs, SessionRef{Format: FormatHermes, ID: id, Location: id, Title: fmt.Sprint(row["title"]), CWD: fmt.Sprint(row["cwd"]), Model: fmt.Sprint(row["model"]), Timestamp: timestampValue(row["started_at"])})
	}
	return refs, nil
}
func (s *HermesStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	db, err := openSQLite(s.DBPath, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sessions, err := queryObjects(ctx, db, `SELECT * FROM sessions WHERE id = ?`, ref.ID)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, ErrSessionNotFound
	}
	document := sessions[0]
	messages, err := queryObjects(ctx, db, `SELECT * FROM messages WHERE session_id = ? AND active = 1 ORDER BY id`, ref.ID)
	if err != nil {
		return nil, err
	}
	for _, message := range messages {
		for _, key := range []string{"content", "tool_calls"} {
			message[key] = jsonValue(message[key])
		}
	}
	values := make([]any, len(messages))
	for i := range messages {
		values[i] = messages[i]
	}
	document["messages"] = values
	data, _ := json.Marshal(document)
	return (HermesCodec{}).Parse(data, opts)
}
func (s *HermesStore) Save(context.Context, *Transcript, RenderOptions) (*SavedSession, error) {
	return nil, ErrSourceOnly
}
func (s *HermesStore) Delete(context.Context, SessionRef) error { return ErrSourceOnly }

type AntigravityStore struct{ DataRoot string }

func (s *AntigravityStore) Format() Format { return FormatAntigravity }
func (s *AntigravityStore) Root() string   { return s.DataRoot }
func (s *AntigravityStore) Discover(ctx context.Context) ([]SessionRef, error) {
	root := filepath.Join(s.DataRoot, "conversations")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []SessionRef
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".db" || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		ref := SessionRef{Format: FormatAntigravity, ID: strings.TrimSuffix(entry.Name(), ".db"), Location: filepath.Join("conversations", entry.Name())}
		parsed, loadErr := s.Load(ctx, ref, ParseOptions{Limits: DefaultLimits()})
		if loadErr != nil {
			continue
		}
		info, _ := entry.Info()
		meta := parsed.Transcript.Meta
		ref.ID, ref.Title, ref.CWD, ref.Model, ref.Timestamp, ref.ModifiedAt = meta.ID, meta.Title, meta.CWD, meta.Model, meta.Timestamp, fileModified(info)
		refs = append(refs, ref)
	}
	return refs, nil
}

func (s *AntigravityStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	path, err := checkedPath(s.DataRoot, ref.Location, true)
	if err != nil {
		return nil, err
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	body := map[string]any{}
	queries := map[string]string{
		"trajectory_meta":          `SELECT trajectory_id, cascade_id, trajectory_type, source FROM trajectory_meta ORDER BY trajectory_id`,
		"steps":                    `SELECT idx, step_type, status, has_subtrajectory, metadata, error_details, permissions, task_details, render_info, step_payload, step_format FROM steps ORDER BY idx`,
		"gen_metadata":             `SELECT idx, data, size FROM gen_metadata ORDER BY idx`,
		"executor_metadata":        `SELECT idx, data FROM executor_metadata ORDER BY idx`,
		"parent_references":        `SELECT idx, data FROM parent_references ORDER BY idx`,
		"battle_mode_infos":        `SELECT idx, data FROM battle_mode_infos ORDER BY idx`,
		"trajectory_metadata_blob": `SELECT id, data FROM trajectory_metadata_blob ORDER BY id`,
	}
	for key, query := range queries {
		rows, queryErr := queryObjects(ctx, db, query)
		if queryErr != nil {
			return nil, queryErr
		}
		values := make([]any, len(rows))
		for i, row := range rows {
			for column, value := range row {
				if bytes, ok := value.([]byte); ok {
					row[column] = hex.EncodeToString(bytes)
				}
			}
			values[i] = row
		}
		body[key] = values
	}
	id := strings.TrimSuffix(filepath.Base(path), ".db")
	logs := filepath.Join(s.DataRoot, "brain", id, ".system_generated", "logs")
	if data, readErr := readFileLimited(filepath.Join(logs, "transcript.jsonl"), opts.Limits.normalized().MaxInputBytes); readErr == nil {
		body["transcript"] = string(data)
	}
	if data, readErr := readFileLimited(filepath.Join(logs, "transcript_full.jsonl"), opts.Limits.normalized().MaxInputBytes); readErr == nil {
		body["transcript_full"] = string(data)
	}
	data, _ := json.Marshal(body)
	return (AntigravityCodec{}).Parse(data, opts)
}

func (s *AntigravityStore) Save(ctx context.Context, transcript *Transcript, opts RenderOptions) (*SavedSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, transcript.Meta.ID)
	if err := safeID(id); err != nil {
		return nil, err
	}
	rendered, err := (AntigravityCodec{}).Render(transcript, RenderOptions{Limits: opts.Limits, ID: id})
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(rendered.Data, &body); err != nil {
		return nil, err
	}
	conversations := filepath.Join(s.DataRoot, "conversations")
	if err := os.MkdirAll(conversations, 0o700); err != nil {
		return nil, err
	}
	target := filepath.Join(conversations, id+".db")
	if _, err := os.Stat(target); err == nil {
		return nil, ErrSessionExists
	}
	temp, err := os.CreateTemp(conversations, ".moirai-antigravity-*.db")
	if err != nil {
		return nil, err
	}
	tempPath := temp.Name()
	temp.Close()
	defer os.Remove(tempPath)
	if err := writeAntigravityDB(ctx, tempPath, body); err != nil {
		return nil, err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return nil, err
	}
	logs := filepath.Join(s.DataRoot, "brain", id, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return nil, err
	}
	for key, name := range map[string]string{"transcript": "transcript.jsonl", "transcript_full": "transcript_full.jsonl"} {
		if value := stringValue(body[key]); value != "" {
			if err := atomicWrite(filepath.Join(logs, name), []byte(value), 0o600); err != nil {
				return nil, err
			}
		}
	}
	return &SavedSession{Ref: SessionRef{Format: FormatAntigravity, ID: id, Location: filepath.Join("conversations", id+".db"), Title: transcript.Meta.Title, CWD: transcript.Meta.CWD, Model: transcript.Meta.Model, Timestamp: transcript.Meta.Timestamp}, Created: true, Warnings: rendered.Warnings}, nil
}

func writeAntigravityDB(ctx context.Context, path string, body map[string]any) error {
	db, err := openSQLite(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	schema := `PRAGMA user_version = 1;
CREATE TABLE trajectory_meta (trajectory_id text PRIMARY KEY,cascade_id text,trajectory_type integer,source integer);
CREATE TABLE steps (idx integer PRIMARY KEY,step_type integer NOT NULL DEFAULT 0,status integer NOT NULL DEFAULT 0,has_subtrajectory numeric NOT NULL DEFAULT false,metadata blob,error_details blob,permissions blob,task_details blob,render_info blob,step_payload blob,step_format integer NOT NULL DEFAULT 0);
CREATE INDEX idx_steps_status ON steps(status); CREATE INDEX idx_steps_step_type ON steps(step_type);
CREATE TABLE gen_metadata (idx integer PRIMARY KEY,data blob,size integer NOT NULL DEFAULT 0);
CREATE TABLE executor_metadata (idx integer PRIMARY KEY,data blob); CREATE TABLE parent_references (idx integer PRIMARY KEY,data blob);
CREATE TABLE trajectory_metadata_blob (id text PRIMARY KEY DEFAULT "main",data blob); CREATE TABLE battle_mode_infos (idx integer PRIMARY KEY,data blob);`
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	for _, raw := range array(body["trajectory_meta"]) {
		row := object(raw)
		if _, err := tx.ExecContext(ctx, `INSERT INTO trajectory_meta VALUES (?,?,?,?)`, row["trajectory_id"], row["cascade_id"], row["trajectory_type"], row["source"]); err != nil {
			return err
		}
	}
	for _, raw := range array(body["steps"]) {
		row := object(raw)
		blobs := make([]any, 0, 7)
		for _, key := range []string{"metadata", "error_details", "permissions", "task_details", "render_info", "step_payload"} {
			value, _ := hex.DecodeString(stringValue(row[key]))
			blobs = append(blobs, value)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO steps VALUES (?,?,?,?,?,?,?,?,?,?,?)`, row["idx"], row["step_type"], row["status"], row["has_subtrajectory"], blobs[0], blobs[1], blobs[2], blobs[3], blobs[4], blobs[5], row["step_format"]); err != nil {
			return err
		}
	}
	for _, raw := range array(body["trajectory_metadata_blob"]) {
		row := object(raw)
		data, _ := hex.DecodeString(stringValue(row["data"]))
		if _, err := tx.ExecContext(ctx, `INSERT INTO trajectory_metadata_blob VALUES (?,?)`, row["id"], data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *AntigravityStore) Delete(ctx context.Context, ref SessionRef) error {
	path, err := checkedPath(s.DataRoot, ref.Location, true)
	if err != nil {
		return err
	}
	id := strings.TrimSuffix(filepath.Base(path), ".db")
	conversations, resolveErr := filepath.EvalSymlinks(filepath.Join(s.DataRoot, "conversations"))
	if err := safeID(id); err != nil || resolveErr != nil || filepath.Dir(path) != conversations {
		return ErrUnsafePath
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	brain, err := checkedPath(s.DataRoot, filepath.Join("brain", id), false)
	if err == nil {
		_ = os.RemoveAll(brain)
	}
	return ctx.Err()
}

type CursorDesktopStore struct{ UserDir string }

func (s *CursorDesktopStore) Format() Format { return FormatCursorDesktop }
func (s *CursorDesktopStore) Root() string   { return s.UserDir }
func (s *CursorDesktopStore) dbPath() string {
	return filepath.Join(s.UserDir, "globalStorage", "state.vscdb")
}

func (s *CursorDesktopStore) Discover(ctx context.Context) ([]SessionRef, error) {
	if _, err := os.Stat(s.dbPath()); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	db, err := openSQLite(s.dbPath(), true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := queryObjects(ctx, db, `SELECT h.composerId, h.value, h.createdAt, h.lastUpdatedAt, CAST(d.value AS TEXT) AS composerData
FROM composerHeaders h LEFT JOIN cursorDiskKV d ON d.key = 'composerData:' || h.composerId ORDER BY h.recency DESC`)
	if err != nil {
		rows, err = queryObjects(ctx, db, `SELECT key, CAST(value AS TEXT) AS composerData FROM cursorDiskKV WHERE key LIKE 'composerData:%'`)
		if err != nil {
			return nil, err
		}
	}
	var refs []SessionRef
	for _, row := range rows {
		id := fmt.Sprint(firstNonNil(row["composerId"], row["key"]))
		id = strings.TrimPrefix(id, "composerData:")
		ref := SessionRef{Format: FormatCursorDesktop, ID: id, Location: id, Timestamp: timestampValue(row["createdAt"]), ModifiedAt: timestampValue(row["lastUpdatedAt"])}
		var header map[string]any
		_ = json.Unmarshal([]byte(fmt.Sprint(row["value"])), &header)
		var composer map[string]any
		_ = json.Unmarshal([]byte(fmt.Sprint(row["composerData"])), &composer)
		ref.Title = firstNonEmpty(stringValue(header["name"]), stringValue(header["subtitle"]), stringValue(composer["name"]))
		ref.CWD = stringValue(object(object(header["workspaceIdentifier"])["uri"])["fsPath"])
		ref.Model = stringValue(object(composer["modelConfig"])["modelName"])
		refs = append(refs, ref)
	}
	return refs, nil
}

func (s *CursorDesktopStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	if err := safeID(ref.ID); err != nil {
		return nil, err
	}
	db, err := openSQLite(s.dbPath(), true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	headers, _ := queryObjects(ctx, db, `SELECT value, workspaceId, createdAt, lastUpdatedAt, isArchived, isSubagent, recency, checkpointAt FROM composerHeaders WHERE composerId = ?`, ref.ID)
	composerRows, err := queryObjects(ctx, db, `SELECT CAST(value AS TEXT) AS value FROM cursorDiskKV WHERE key = ?`, "composerData:"+ref.ID)
	if err != nil {
		return nil, err
	}
	composer := ""
	if len(composerRows) > 0 {
		composer = fmt.Sprint(composerRows[0]["value"])
	}
	header := map[string]any{"value": fmt.Sprintf(`{"type":"head","composerId":%q}`, ref.ID)}
	if len(headers) > 0 {
		header = headers[0]
	} else if composer == "" {
		return nil, ErrSessionNotFound
	}
	escapedID := escapeSQLiteLike(ref.ID)
	bubbleRows, err := queryObjects(ctx, db, `SELECT key, CAST(value AS TEXT) AS value FROM cursorDiskKV WHERE key LIKE ? ESCAPE '\' ORDER BY rowid`, "bubbleId:"+escapedID+":%")
	if err != nil {
		return nil, err
	}
	auxRows, _ := queryObjects(ctx, db, `SELECT key, CAST(value AS TEXT) AS value FROM cursorDiskKV WHERE key LIKE ? ESCAPE '\' AND key NOT LIKE ? ESCAPE '\' AND key NOT LIKE 'composerData:%' AND key NOT LIKE 'agentKv:%' ORDER BY rowid`, "%"+escapedID+"%", "bubbleId:"+escapedID+":%")
	bubbles := make([]any, len(bubbleRows))
	for i := range bubbleRows {
		bubbles[i] = bubbleRows[i]
	}
	aux := make([]any, len(auxRows))
	for i := range auxRows {
		aux[i] = auxRows[i]
	}
	body := map[string]any{"header": fmt.Sprint(header["value"]), "workspace_id": header["workspaceId"], "created_at": header["createdAt"], "last_updated_at": header["lastUpdatedAt"], "is_archived": integerValue(header["isArchived"]) != 0, "is_subagent": integerValue(header["isSubagent"]) != 0, "recency": header["recency"], "checkpoint_at": header["checkpointAt"], "composer_data": composer, "bubbles": bubbles, "aux": aux}
	data, _ := json.Marshal(body)
	return (CursorDesktopCodec{}).Parse(data, opts)
}

func (s *CursorDesktopStore) Save(ctx context.Context, transcript *Transcript, opts RenderOptions) (*SavedSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, transcript.Meta.ID)
	if err := safeID(id); err != nil {
		return nil, err
	}
	rendered, err := (CursorDesktopCodec{}).Render(transcript, RenderOptions{Limits: opts.Limits, ID: id})
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(rendered.Data, &body); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath()), 0o700); err != nil {
		return nil, err
	}
	newDatabase := !fileExists(s.dbPath())
	db, err := openSQLite(s.dbPath(), false)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if newDatabase {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB);
CREATE TABLE IF NOT EXISTS composerHeaders (composerId TEXT PRIMARY KEY, workspaceId TEXT, createdAt INTEGER, lastUpdatedAt INTEGER, isArchived INTEGER, isSubagent INTEGER, recency INTEGER, checkpointAt INTEGER, value TEXT);
CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
			return nil, err
		}
	} else if err := requireSQLiteTables(ctx, tx, "cursorDiskKV", "composerHeaders", "ItemTable"); err != nil {
		return nil, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM composerHeaders WHERE composerId = ?
UNION ALL SELECT 1 FROM cursorDiskKV WHERE key = ?
)`, id, "composerData:"+id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 0 {
		return nil, ErrSessionExists
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO composerHeaders (composerId, workspaceId, createdAt, lastUpdatedAt, isArchived, isSubagent, recency, checkpointAt, value) VALUES (?,?,?,?,?,?,?,?,?)`, id, body["workspace_id"], body["created_at"], body["last_updated_at"], boolInt(boolValue(body["is_archived"])), boolInt(boolValue(body["is_subagent"])), body["recency"], body["checkpoint_at"], body["header"]); err != nil {
		return nil, err
	}
	if composer := stringValue(body["composer_data"]); composer != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cursorDiskKV (key,value) VALUES (?,?)`, "composerData:"+id, composer); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"bubbles", "aux"} {
		for _, raw := range array(body[key]) {
			row := object(raw)
			if _, err := tx.ExecContext(ctx, `INSERT INTO cursorDiskKV (key,value) VALUES (?,?)`, row["key"], row["value"]); err != nil {
				return nil, err
			}
		}
	}
	if err := registerCursorSidebar(ctx, tx, id, transcript.Meta); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &SavedSession{Ref: SessionRef{Format: FormatCursorDesktop, ID: id, Location: id, Title: transcript.Meta.Title, CWD: transcript.Meta.CWD, Model: transcript.Meta.Model, Timestamp: transcript.Meta.Timestamp}, Created: true, Warnings: rendered.Warnings}, nil
}

func registerCursorSidebar(ctx context.Context, tx *sql.Tx, id string, meta Metadata) error {
	if meta.CWD == "" {
		return nil
	}
	read := func(key string) string {
		var value string
		_ = tx.QueryRowContext(ctx, `SELECT CAST(value AS TEXT) FROM ItemTable WHERE key = ?`, key).Scan(&value)
		return value
	}
	var projects []any
	_ = json.Unmarshal([]byte(read("glass.localAgentProjects.v1")), &projects)
	projectID := ""
	for _, raw := range projects {
		project := object(raw)
		if stringValue(object(object(project["workspace"])["uri"])["fsPath"]) == meta.CWD {
			projectID = stringValue(project["id"])
		}
	}
	if projectID == "" {
		projectID = uuidFromSeed("cursor-project", meta.CWD)
		projects = append(projects, map[string]any{"id": projectID, "name": filepath.Base(meta.CWD), "workspace": map[string]any{"id": "", "uri": map[string]any{"fsPath": meta.CWD, "external": "file://" + meta.CWD, "path": meta.CWD, "scheme": "file"}}, "createdAt": epochMillis(meta.Timestamp), "lastUpdatedAt": epochMillis(meta.Timestamp), "isArchived": false})
		encoded, _ := json.Marshal(projects)
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO ItemTable (key,value) VALUES (?,?)`, "glass.localAgentProjects.v1", string(encoded)); err != nil {
			return err
		}
	}
	membership := map[string]any{}
	_ = json.Unmarshal([]byte(read("glass.localAgentProjectMembership.v1")), &membership)
	membership[id] = projectID
	encoded, _ := json.Marshal(membership)
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO ItemTable (key,value) VALUES (?,?)`, "glass.localAgentProjectMembership.v1", string(encoded))
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *CursorDesktopStore) Delete(ctx context.Context, ref SessionRef) error {
	if err := safeID(ref.ID); err != nil {
		return err
	}
	db, err := openSQLite(s.dbPath(), false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM composerHeaders WHERE composerId = ?`, ref.ID)
	if err != nil {
		return err
	}
	escapedID := escapeSQLiteLike(ref.ID)
	if _, err := tx.ExecContext(ctx, `DELETE FROM cursorDiskKV WHERE key = ? OR key LIKE ? ESCAPE '\'`, "composerData:"+ref.ID, "bubbleId:"+escapedID+":%"); err != nil {
		return err
	}
	if err := unregisterCursorSidebar(ctx, tx, ref.ID); err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return ErrSessionNotFound
	}
	return tx.Commit()
}

func escapeSQLiteLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func requireSQLiteTables(ctx context.Context, tx *sql.Tx, tables ...string) error {
	for _, table := range tables {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("%w: Cursor database is missing table %s", ErrUnsupported, table)
		}
	}
	return nil
}

func unregisterCursorSidebar(ctx context.Context, tx *sql.Tx, id string) error {
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT CAST(value AS TEXT) FROM ItemTable WHERE key = ?`, "glass.localAgentProjectMembership.v1").Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	membership := map[string]any{}
	if json.Unmarshal([]byte(encoded), &membership) != nil {
		return nil
	}
	delete(membership, id)
	updated, err := json.Marshal(membership)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO ItemTable (key,value) VALUES (?,?)`, "glass.localAgentProjectMembership.v1", string(updated))
	return err
}
