package moirai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type SessionRef struct {
	Format     Format `json:"format"`
	ID         string `json:"id"`
	Location   string `json:"location"`
	Title      string `json:"title,omitempty"`
	CWD        string `json:"cwd,omitempty"`
	Model      string `json:"model,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
}

type SavedSession struct {
	Ref      SessionRef `json:"ref"`
	Created  bool       `json:"created"`
	Warnings []Warning  `json:"warnings,omitempty"`
}

type Store interface {
	Format() Format
	Root() string
	Discover(context.Context) ([]SessionRef, error)
	Load(context.Context, SessionRef, ParseOptions) (*ParseResult, error)
	Save(context.Context, *Transcript, RenderOptions) (*SavedSession, error)
	Delete(context.Context, SessionRef) error
}

type StoreRegistry struct {
	mu     sync.RWMutex
	stores map[Format]Store
}

func NewStoreRegistry(stores ...Store) *StoreRegistry {
	r := &StoreRegistry{stores: make(map[Format]Store, len(stores))}
	for _, store := range stores {
		_ = r.Register(store)
	}
	return r
}

func (r *StoreRegistry) Register(store Store) error {
	if store == nil || store.Format() == "" {
		return fmt.Errorf("%w: empty store", ErrUnknownFormat)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.stores[store.Format()]; exists {
		return fmt.Errorf("store already registered: %s", store.Format())
	}
	r.stores[store.Format()] = store
	return nil
}

func (r *StoreRegistry) Store(format Format) (Store, error) {
	r.mu.RLock()
	store := r.stores[format]
	r.mu.RUnlock()
	if store == nil {
		return nil, &FormatError{Format: format, Op: "store", Err: ErrUnsupported}
	}
	return store, nil
}

func (r *StoreRegistry) Discover(ctx context.Context, formats ...Format) ([]SessionRef, []Warning, error) {
	selected := make(map[Format]bool)
	for _, format := range formats {
		selected[format] = true
	}
	var refs []SessionRef
	var warnings []Warning
	for _, format := range Formats {
		if len(selected) > 0 && !selected[format] {
			continue
		}
		store, err := r.Store(format)
		if err != nil {
			continue
		}
		found, err := store.Discover(ctx)
		if err != nil {
			warnings = append(warnings, Warning{Path: store.Root(), Code: "store_unavailable", Message: err.Error()})
			continue
		}
		refs = append(refs, found...)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return firstNonEmpty(refs[i].ModifiedAt, refs[i].Timestamp) > firstNonEmpty(refs[j].ModifiedAt, refs[j].Timestamp)
	})
	return refs, warnings, nil
}

func FindSession(refs []SessionRef, selector string, format Format) (SessionRef, error) {
	var match *SessionRef
	for i := range refs {
		if format != "" && refs[i].Format != format {
			continue
		}
		if refs[i].ID == selector || refs[i].Location == selector {
			if match != nil {
				return SessionRef{}, fmt.Errorf("%w: selector is ambiguous", ErrInvalidTranscript)
			}
			candidate := refs[i]
			match = &candidate
		}
	}
	if match == nil {
		return SessionRef{}, ErrSessionNotFound
	}
	return *match, nil
}

func checkedPath(root, location string, mustExist bool) (string, error) {
	if root == "" || location == "" || filepath.IsAbs(location) {
		return "", ErrUnsafePath
	}
	clean := filepath.Clean(location)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrUnsafePath
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absRoot); resolveErr == nil {
		absRoot = resolved
	} else if !errors.Is(resolveErr, fs.ErrNotExist) {
		return "", resolveErr
	}
	candidate := filepath.Join(absRoot, clean)
	rel, err := filepath.Rel(absRoot, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrUnsafePath
	}
	current := absRoot
	parts := strings.Split(clean, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) {
			if mustExist || i == len(parts)-1 && mustExist {
				return "", ErrSessionNotFound
			}
			break
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", ErrUnsafePath
		}
	}
	return candidate, nil
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".moirai-write-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func readFileLimited(path string, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		maximum = DefaultLimits().MaxInputBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, ErrLimitExceeded
	}
	return data, nil
}

func safeID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\:") || strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ErrUnsafePath
	}
	return nil
}

func fileModified(info fs.FileInfo) string {
	if info == nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339Nano)
}
