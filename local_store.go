package moirai

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type FileLayout func(*Transcript, RenderOptions) (string, error)

type LocalFileStore struct {
	format    Format
	root      string
	codec     Codec
	extension string
	match     func(path string, entry fs.DirEntry) bool
	layout    FileLayout
	readOnly  bool
}

func NewLocalFileStore(format Format, root, extension string, layout FileLayout) (*LocalFileStore, error) {
	codec, err := DefaultRegistry.Codec(format)
	if err != nil {
		return nil, err
	}
	return &LocalFileStore{format: format, root: root, codec: codec, extension: extension, layout: layout}, nil
}

func (s *LocalFileStore) Format() Format { return s.format }
func (s *LocalFileStore) Root() string   { return s.root }

func (s *LocalFileStore) Discover(ctx context.Context) ([]SessionRef, error) {
	return s.DiscoverWithLimits(ctx, DefaultStoreLimits())
}

func (s *LocalFileStore) DiscoverWithLimits(ctx context.Context, limits Limits) ([]SessionRef, error) {
	if s.root == "" {
		return nil, nil
	}
	if _, err := os.Stat(s.root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	limits = limits.normalized()
	var refs []SessionRef
	byID := map[string]int{}
	var skippedOversize []string
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != s.root && entry.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			if s.format == FormatClaudeCode && path != s.root {
				if entry.Name() == "subagents" || entry.Name() == "tool-results" || fileExists(filepath.Join(filepath.Dir(path), entry.Name()+s.extension)) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || s.extension != "" && filepath.Ext(entry.Name()) != s.extension || s.match != nil && !s.match(path, entry) {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return nil
		}
		info, _ := entry.Info()
		if info != nil && info.Size() > limits.MaxInputBytes {
			skippedOversize = append(skippedOversize, rel)
			return nil
		}
		meta, err := s.discoverMetadata(path, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), limits)
		if err != nil || meta.ID == "" {
			return nil
		}
		ref := SessionRef{Format: s.format, ID: meta.ID, Location: rel, Title: meta.Title, CWD: meta.CWD, Model: meta.Model, Timestamp: meta.Timestamp, ModifiedAt: fileModified(info)}
		if existing, ok := byID[ref.ID]; ok {
			if locationDepth(ref.Location) < locationDepth(refs[existing].Location) {
				refs[existing] = ref
			}
			return nil
		}
		byID[ref.ID] = len(refs)
		refs = append(refs, ref)
		return nil
	})
	if err != nil {
		return refs, err
	}
	if len(skippedOversize) > 0 {
		return refs, &DiscoveryError{Code: "skipped_oversize", Path: skippedOversize[0], Count: len(skippedOversize), Err: ErrLimitExceeded}
	}
	return refs, nil
}

func (s *LocalFileStore) discoverMetadata(path, sourceID string, limits Limits) (Metadata, error) {
	switch s.format {
	case FormatClaudeCode, FormatCodex, FormatPi, FormatCampfire:
		if meta, ok, err := shallowJSONLMetadata(path, s.format, sourceID, limits); err != nil || ok {
			return meta, err
		}
	}
	data, err := readFileLimited(path, limits.MaxInputBytes)
	if err != nil {
		return Metadata{}, err
	}
	parsed, err := s.codec.Parse(data, ParseOptions{Limits: limits, SourceID: sourceID})
	if err != nil {
		return Metadata{}, err
	}
	return parsed.Transcript.Meta, nil
}

func shallowJSONLMetadata(path string, format Format, sourceID string, limits Limits) (Metadata, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	maxLine := int(min(limits.MaxInputBytes, int64(^uint(0)>>1)))
	scanner.Buffer(make([]byte, 64*1024), maxLine)
	meta := Metadata{ID: sourceID}
	conversational := false
	for line := 0; scanner.Scan() && line < 512; line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var record map[string]any
		if decodeJSONDocument(raw, &record, limits) != nil {
			continue
		}
		kind := stringValue(record["type"])
		switch format {
		case FormatClaudeCode:
			if kind == "summary" {
				meta.Title = firstNonEmpty(meta.Title, stringValue(record["summary"]))
			}
			if kind == "user" || kind == "assistant" {
				meta.ID = firstNonEmpty(stringValue(record["sessionId"]), meta.ID)
				meta.Timestamp = firstNonEmpty(meta.Timestamp, timestampValue(record["timestamp"]))
				meta.CWD = firstNonEmpty(meta.CWD, stringValue(record["cwd"]))
				meta.GitBranch = firstNonEmpty(meta.GitBranch, stringValue(record["gitBranch"]))
				meta.CLIVersion = firstNonEmpty(meta.CLIVersion, stringValue(record["version"]))
				meta.Model = firstNonEmpty(meta.Model, stringValue(object(record["message"])["model"]))
				conversational = true
			}
		case FormatCodex:
			payload := object(record["payload"])
			if kind == "session_meta" {
				meta.ID = firstNonEmpty(stringValue(payload["id"]), meta.ID)
				meta.Timestamp = firstNonEmpty(meta.Timestamp, timestampValue(payload["timestamp"]), timestampValue(record["timestamp"]))
				meta.CWD = firstNonEmpty(meta.CWD, stringValue(payload["cwd"]))
				meta.Model = firstNonEmpty(meta.Model, stringValue(payload["model"]))
			}
			conversational = conversational || kind == "response_item" || kind == "event_msg"
		case FormatPi, FormatCampfire:
			if kind == "session" {
				meta.ID = firstNonEmpty(stringValue(record["id"]), meta.ID)
				meta.Timestamp = firstNonEmpty(meta.Timestamp, timestampValue(record["timestamp"]))
				meta.CWD = firstNonEmpty(meta.CWD, stringValue(record["cwd"]))
			}
			if kind == "session_info" {
				meta.Title = firstNonEmpty(meta.Title, stringValue(record["name"]))
			}
			if kind == "model_change" {
				meta.Model = firstNonEmpty(meta.Model, stringValue(record["modelId"]))
			}
			conversational = conversational || kind == "message" || kind == "custom_message"
		}
		if conversational && meta.ID != "" && meta.Timestamp != "" && (format == FormatClaudeCode || format == FormatCodex) {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return Metadata{}, false, err
	}
	return meta, conversational && meta.ID != "", nil
}

func locationDepth(location string) int {
	return len(strings.FieldsFunc(filepath.Clean(location), func(r rune) bool { return r == '/' || r == '\\' }))
}

func (s *LocalFileStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.Format != "" && ref.Format != s.format {
		return nil, ErrUnknownFormat
	}
	path, err := checkedPath(s.root, ref.Location, true)
	if err != nil {
		return nil, &PathError{Path: ref.Location, Err: err}
	}
	opts.Limits = opts.Limits.storeNormalized()
	data, err := readFileLimited(path, opts.Limits.MaxInputBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.codec.Parse(data, opts)
}

func (s *LocalFileStore) Save(ctx context.Context, transcript *Transcript, opts RenderOptions) (*SavedSession, error) {
	if s.readOnly {
		return nil, ErrSourceOnly
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := Validate(transcript, opts.Limits); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, transcript.Meta.ID)
	if err := safeID(id); err != nil {
		return nil, err
	}
	copy := *transcript
	copy.Meta = transcript.Meta
	copy.Meta.ID = id
	location, err := s.layout(&copy, opts)
	if err != nil {
		return nil, err
	}
	path, err := checkedPath(s.root, location, false)
	if err != nil {
		return nil, &PathError{Path: location, Err: err}
	}
	rendered, err := s.codec.Render(&copy, RenderOptions{Limits: opts.Limits, ID: id, Now: opts.Now})
	if err != nil {
		return nil, err
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, fs.ErrNotExist)
	if statErr == nil {
		return nil, ErrSessionExists
	}
	if !created {
		return nil, statErr
	}
	if err := atomicWriteExclusive(path, rendered.Data, 0o600); err != nil {
		return nil, err
	}
	return &SavedSession{Ref: SessionRef{Format: s.format, ID: id, Location: location, Title: copy.Meta.Title, CWD: copy.Meta.CWD, Model: copy.Meta.Model, Timestamp: copy.Meta.Timestamp, ModifiedAt: time.Now().UTC().Format(time.RFC3339Nano)}, Created: true, Warnings: rendered.Warnings}, nil
}

func (s *LocalFileStore) Delete(ctx context.Context, ref SessionRef) error {
	if s.readOnly {
		return ErrSourceOnly
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := checkedPath(s.root, ref.Location, true)
	if err != nil {
		return &PathError{Path: ref.Location, Err: err}
	}
	if err := os.Remove(path); errors.Is(err, fs.ErrNotExist) {
		return ErrSessionNotFound
	} else {
		return err
	}
}

func DefaultStores() (*StoreRegistry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	registry := NewStoreRegistry()
	add := func(store *LocalFileStore, err error) error {
		if err != nil {
			return err
		}
		return registry.Register(store)
	}
	claudeRoot := filepath.Join(home, ".claude", "projects")
	if err := add(NewLocalFileStore(FormatClaudeCode, claudeRoot, ".jsonl", claudeLayout)); err != nil {
		return nil, err
	}
	codexHome := envOr("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := add(NewLocalFileStore(FormatCodex, filepath.Join(codexHome, "sessions"), ".jsonl", codexLayout)); err != nil {
		return nil, err
	}
	piRoot := envOr("PI_CODING_AGENT_SESSION_DIR", "")
	if piRoot == "" {
		piRoot = filepath.Join(envOr("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent")), "sessions")
	}
	if err := add(NewLocalFileStore(FormatPi, piRoot, ".jsonl", piLayout)); err != nil {
		return nil, err
	}
	campfireRoot := envOr("CAMPFIRE_CODING_AGENT_SESSION_DIR", "")
	if campfireRoot == "" {
		campfireRoot = filepath.Join(envOr("CAMPFIRE_CODING_AGENT_DIR", filepath.Join(home, ".campfire", "agent")), "sessions")
	}
	if err := add(NewLocalFileStore(FormatCampfire, campfireRoot, ".jsonl", piLayout)); err != nil {
		return nil, err
	}
	ampRoot := envOr("AMP_THREADS_DIR", "")
	if ampRoot == "" {
		xdg := envOr("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
		ampRoot = filepath.Join(xdg, "amp", "threads")
	}
	amp, err := NewLocalFileStore(FormatAmp, ampRoot, ".json", flatJSONLayout)
	if err != nil {
		return nil, err
	}
	amp.readOnly = true
	if err := registry.Register(amp); err != nil {
		return nil, err
	}
	grokHome := envOr("GROK_HOME", filepath.Join(home, ".grok"))
	grok, err := NewBundleStore(FormatGrok, filepath.Join(grokHome, "sessions"), func(dir string) bool {
		return fileExists(filepath.Join(dir, "chat_history.jsonl")) || fileExists(filepath.Join(dir, "updates.jsonl"))
	}, grokBundleRead, grokBundleWrite, grokBundleLayout)
	if err != nil {
		return nil, err
	}
	if err := registry.Register(grok); err != nil {
		return nil, err
	}
	fxHome := envOr("FX_HOME", filepath.Join(home, ".fx"))
	fx, err := NewBundleStore(FormatFX, filepath.Join(fxHome, "sessions"), func(dir string) bool {
		return fileExists(filepath.Join(dir, "events.jsonl"))
	}, fxBundleRead, fxBundleWrite, flatBundleLayout)
	if err != nil {
		return nil, err
	}
	if err := registry.Register(fx); err != nil {
		return nil, err
	}
	openCodeDB := envOr("OPENCODE_DB", filepath.Join(envOr("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "opencode", "opencode.db"))
	if err := registry.Register(&OpenCodeStore{DBPath: openCodeDB}); err != nil {
		return nil, err
	}
	hermesHome := envOr("HERMES_HOME", filepath.Join(home, ".hermes"))
	if err := registry.Register(&HermesStore{DBPath: filepath.Join(hermesHome, "state.db")}); err != nil {
		return nil, err
	}
	if err := registry.Register(&AntigravityStore{DataRoot: filepath.Join(home, ".gemini", "antigravity-cli")}); err != nil {
		return nil, err
	}
	cursorUser := envOr("CURSOR_DESKTOP_USER_DIR", "")
	if cursorUser == "" {
		switch runtime.GOOS {
		case "darwin":
			cursorUser = filepath.Join(home, "Library", "Application Support", "Cursor", "User")
		case "windows":
			cursorUser = filepath.Join(home, "AppData", "Roaming", "Cursor", "User")
		default:
			cursorUser = filepath.Join(home, ".config", "Cursor", "User")
		}
	}
	if err := registry.Register(&CursorDesktopStore{UserDir: cursorUser}); err != nil {
		return nil, err
	}
	coworkRoot := envOr("COWORK_SESSIONS_DIR", "")
	if coworkRoot == "" {
		switch runtime.GOOS {
		case "darwin":
			coworkRoot = filepath.Join(home, "Library", "Application Support", "Claude", "local-agent-mode-sessions")
		case "windows":
			coworkRoot = filepath.Join(envOr("APPDATA", filepath.Join(home, "AppData", "Roaming")), "Claude", "local-agent-mode-sessions")
		default:
			coworkRoot = filepath.Join(home, ".config", "Claude", "local-agent-mode-sessions")
		}
	}
	if err := registry.Register(&CoworkStore{SessionsRoot: coworkRoot}); err != nil {
		return nil, err
	}
	if err := registry.Register(&CursorStore{ChatsRoot: filepath.Join(home, ".cursor", "chats")}); err != nil {
		return nil, err
	}
	return registry, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return expandHome(value)
	}
	return fallback
}

func expandHome(value string) string {
	if value == "~" || strings.HasPrefix(value, "~"+string(filepath.Separator)) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(value, "~"+string(filepath.Separator)))
		}
	}
	return value
}

func claudeLayout(t *Transcript, _ RenderOptions) (string, error) {
	return filepath.Join(encodeClaudeProject(t.Meta.CWD), t.Meta.ID+".jsonl"), nil
}

func codexLayout(t *Transcript, _ RenderOptions) (string, error) {
	stamp, err := time.Parse(time.RFC3339Nano, t.Meta.Timestamp)
	if err != nil {
		return "", err
	}
	filenameStamp := strings.NewReplacer(":", "-", ".", "-").Replace(stamp.UTC().Format("2006-01-02T15:04:05"))
	return filepath.Join(stamp.UTC().Format("2006"), stamp.UTC().Format("01"), stamp.UTC().Format("02"), fmt.Sprintf("rollout-%s-%s.jsonl", filenameStamp, t.Meta.ID)), nil
}

func piLayout(t *Transcript, _ RenderOptions) (string, error) {
	stamp, err := time.Parse(time.RFC3339Nano, t.Meta.Timestamp)
	if err != nil {
		return "", err
	}
	filenameStamp := strings.NewReplacer(":", "-", ".", "-").Replace(stamp.UTC().Format("2006-01-02T15:04:05.000Z"))
	return filepath.Join(encodeWorkspace(t.Meta.CWD), filenameStamp+"_"+t.Meta.ID+".jsonl"), nil
}

func flatJSONLayout(t *Transcript, _ RenderOptions) (string, error) {
	return t.Meta.ID + ".json", nil
}

func encodeWorkspace(cwd string) string {
	if cwd == "" {
		cwd = string(filepath.Separator)
	}
	if runtime.GOOS == "windows" {
		cwd = strings.TrimPrefix(cwd, `\\?\`)
	}
	trimmed := strings.TrimLeft(cwd, `/\`)
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-")
	return "--" + replacer.Replace(trimmed) + "--"
}

func encodeClaudeProject(cwd string) string {
	if cwd == "" {
		return "-"
	}
	cwd = strings.TrimPrefix(cwd, `\\?\`)
	var encoded strings.Builder
	encoded.Grow(len(cwd))
	for _, char := range cwd {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			encoded.WriteRune(char)
		} else {
			encoded.WriteByte('-')
		}
	}
	return encoded.String()
}

func URLWorkspace(cwd string) string { return url.PathEscape(cwd) }
