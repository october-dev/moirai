package moirai

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BundleRead func(directory string, limits Limits) ([]byte, error)
type BundleWrite func(directory string, rendered []byte, limits Limits) error
type BundleMatch func(directory string) bool

type BundleStore struct {
	format Format
	root   string
	codec  Codec
	match  BundleMatch
	read   BundleRead
	write  BundleWrite
	layout FileLayout
}

func NewBundleStore(format Format, root string, match BundleMatch, read BundleRead, write BundleWrite, layout FileLayout) (*BundleStore, error) {
	codec, err := DefaultRegistry.Codec(format)
	if err != nil {
		return nil, err
	}
	return &BundleStore{format: format, root: root, codec: codec, match: match, read: read, write: write, layout: layout}, nil
}

func (s *BundleStore) Format() Format { return s.format }
func (s *BundleStore) Root() string   { return s.root }

func (s *BundleStore) Discover(ctx context.Context) ([]SessionRef, error) {
	return s.DiscoverWithLimits(ctx, DefaultStoreLimits())
}

func (s *BundleStore) DiscoverWithLimits(ctx context.Context, limits Limits) ([]SessionRef, error) {
	if _, err := os.Stat(s.root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	limits = limits.normalized()
	var refs []SessionRef
	var skippedOversize []string
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		if path == s.root || !s.match(path) {
			return nil
		}
		data, err := s.read(path, limits)
		if err != nil {
			if errors.Is(err, ErrLimitExceeded) {
				rel, _ := filepath.Rel(s.root, path)
				skippedOversize = append(skippedOversize, rel)
			}
			return filepath.SkipDir
		}
		parsed, err := s.codec.Parse(data, ParseOptions{Limits: limits, SourceID: filepath.Base(path)})
		if err != nil {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return filepath.SkipDir
		}
		info, _ := entry.Info()
		meta := parsed.Transcript.Meta
		refs = append(refs, SessionRef{Format: s.format, ID: meta.ID, Location: rel, Title: meta.Title, CWD: meta.CWD, Model: meta.Model, Timestamp: meta.Timestamp, ModifiedAt: fileModified(info)})
		return filepath.SkipDir
	})
	if err != nil {
		return refs, err
	}
	if len(skippedOversize) > 0 {
		return refs, &DiscoveryError{Code: "skipped_oversize", Path: skippedOversize[0], Count: len(skippedOversize), Err: ErrLimitExceeded}
	}
	return refs, nil
}

func (s *BundleStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts.Limits = opts.Limits.storeNormalized()
	path, err := checkedPath(s.root, ref.Location, true)
	if err != nil {
		return nil, &PathError{Path: ref.Location, Err: err}
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || !s.match(path) {
		return nil, ErrSessionNotFound
	}
	data, err := s.read(path, opts.Limits)
	if err != nil {
		return nil, err
	}
	return s.codec.Parse(data, opts)
}

func (s *BundleStore) Save(ctx context.Context, transcript *Transcript, opts RenderOptions) (*SavedSession, error) {
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
	target, err := checkedPath(s.root, location, false)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(target); err == nil {
		return nil, ErrSessionExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	rendered, err := s.codec.Render(&copy, RenderOptions{Limits: opts.Limits, ID: id, Now: opts.Now})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	release, err := reservePath(target)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err := os.Stat(target); err == nil {
		return nil, ErrSessionExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".moirai-stage-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return nil, err
	}
	if err := s.write(stage, rendered.Data, opts.Limits); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, target); err != nil {
		return nil, err
	}
	return &SavedSession{Ref: SessionRef{Format: s.format, ID: id, Location: location, Title: copy.Meta.Title, CWD: copy.Meta.CWD, Model: copy.Meta.Model, Timestamp: copy.Meta.Timestamp, ModifiedAt: time.Now().UTC().Format(time.RFC3339Nano)}, Created: true, Warnings: rendered.Warnings}, nil
}

func (s *BundleStore) Delete(ctx context.Context, ref SessionRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := checkedPath(s.root, ref.Location, true)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !s.match(path) {
		return ErrSessionNotFound
	}
	return os.RemoveAll(path)
}

func readBundleJSON(directory, name string, optional bool, limits Limits) (any, error) {
	path := filepath.Join(directory, name)
	info, statErr := os.Lstat(path)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafePath
	}
	data, err := readFileLimited(path, limits.normalized().MaxInputBytes)
	if errors.Is(err, fs.ErrNotExist) && optional {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var value any
	if err := decodeJSONDocument(data, &value, limits); err != nil {
		return nil, err
	}
	return value, nil
}

func readBundleLines(directory, name string, optional bool, limits Limits) ([]any, error) {
	path := filepath.Join(directory, name)
	info, statErr := os.Lstat(path)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafePath
	}
	data, err := readFileLimited(path, limits.normalized().MaxInputBytes)
	if errors.Is(err, fs.ErrNotExist) && optional {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records, _, err := decodeJSONLines(data, limits)
	if err != nil {
		return nil, err
	}
	result := make([]any, len(records))
	for i := range records {
		result[i] = records[i]
	}
	return result, nil
}

func writeBundleValue(directory, name string, value any) error {
	if value == nil {
		return nil
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(directory, name), append(data, '\n'), 0o600)
}

func writeBundleLines(directory, name string, value any) error {
	records := array(value)
	data, err := encodeJSONLines(records)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(directory, name), data, 0o600)
}

func grokBundleRead(directory string, limits Limits) ([]byte, error) {
	chat, err := readBundleLines(directory, "chat_history.jsonl", false, limits)
	if err != nil {
		return nil, err
	}
	updates, err := readBundleLines(directory, "updates.jsonl", true, limits)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"chat_history": chat, "updates": updates}
	for _, name := range []string{"summary.json", "prompt_context.json", "resources_state.json", "signals.json"} {
		value, readErr := readBundleJSON(directory, name, true, limits)
		if readErr != nil {
			return nil, readErr
		}
		if value != nil {
			body[strings.TrimSuffix(name, ".json")] = value
		}
	}
	data, _ := json.Marshal(body)
	return data, nil
}

func grokBundleWrite(directory string, rendered []byte, _ Limits) error {
	var body map[string]any
	if err := json.Unmarshal(rendered, &body); err != nil {
		return err
	}
	if err := writeBundleLines(directory, "chat_history.jsonl", body["chat_history"]); err != nil {
		return err
	}
	if err := writeBundleLines(directory, "updates.jsonl", body["updates"]); err != nil {
		return err
	}
	return writeBundleValue(directory, "summary.json", body["summary"])
}

func fxBundleRead(directory string, limits Limits) ([]byte, error) {
	events, err := readBundleLines(directory, "events.jsonl", false, limits)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"events": events}
	for key, name := range map[string]string{"session": "session.json", "authority": "authority.json", "display": "display.json", "usage": "usage-v2.json", "checkpoint": "checkpoint.json"} {
		value, readErr := readBundleJSON(directory, name, true, limits)
		if readErr != nil {
			return nil, readErr
		}
		if value != nil {
			body[key] = value
		}
	}
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "commit.") && strings.HasSuffix(entry.Name(), ".json") {
			body["commit"], _ = readBundleJSON(directory, entry.Name(), true, limits)
			break
		}
	}
	data, _ := json.Marshal(body)
	return data, nil
}

func fxBundleWrite(directory string, rendered []byte, _ Limits) error {
	var body map[string]any
	if err := json.Unmarshal(rendered, &body); err != nil {
		return err
	}
	if err := writeBundleLines(directory, "events.jsonl", body["events"]); err != nil {
		return err
	}
	for key, name := range map[string]string{"session": "session.json", "authority": "authority.json", "display": "display.json", "usage": "usage-v2.json", "checkpoint": "checkpoint.json"} {
		if err := writeBundleValue(directory, name, body[key]); err != nil {
			return err
		}
	}
	generation := stringValue(object(body["session"])["log_generation"])
	if generation == "" {
		generation = "1"
	}
	if err := writeBundleValue(directory, "commit."+generation+".json", body["commit"]); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(directory, "commit.lock"), nil, 0o600)
}

func grokBundleLayout(t *Transcript, _ RenderOptions) (string, error) {
	return filepath.Join(URLWorkspace(t.Meta.CWD), t.Meta.ID), nil
}

func flatBundleLayout(t *Transcript, _ RenderOptions) (string, error) { return t.Meta.ID, nil }
