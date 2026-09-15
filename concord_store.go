package moirai

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The source database belongs to Concord. Only the app may merge imports into
// its in-memory state; writing it externally would race its atomic persistence.
type ConcordStore struct {
	Path      string
	ImportDir string
}

func (s *ConcordStore) Format() Format { return FormatConcord }
func (s *ConcordStore) Root() string   { return s.Path }
func (s *ConcordStore) Discover(ctx context.Context) ([]SessionRef, error) {
	return s.DiscoverWithLimits(ctx, DefaultStoreLimits())
}
func (s *ConcordStore) DiscoverWithLimits(ctx context.Context, limits Limits) ([]SessionRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := readFileLimited(s.Path, limits.normalized().MaxInputBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items, _, err := decodeConcord(data, limits)
	if err != nil {
		return nil, err
	}
	refs := []SessionRef{}
	for _, c := range items {
		if err := ctx.Err(); err != nil {
			return refs, err
		}
		if _, err := concordTranscript(c, true, limits); err != nil {
			return refs, err
		}
		refs = append(refs, SessionRef{Format: FormatConcord, ID: c.ID, Location: c.ID, Title: c.Title, Model: c.Model, Timestamp: c.CreatedAt, ModifiedAt: c.UpdatedAt})
	}
	return refs, nil
}
func (s *ConcordStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !concordUUID(ref.ID) || ref.Location != ref.ID {
		return nil, ErrUnsafePath
	}
	data, err := readFileLimited(s.Path, opts.Limits.normalized().MaxInputBytes)
	if err != nil {
		return nil, err
	}
	items, _, err := decodeConcord(data, opts.Limits)
	if err != nil {
		return nil, err
	}
	for _, c := range items {
		if c.ID == ref.ID {
			t, err := concordTranscript(c, true, opts.Limits)
			if err != nil {
				return nil, err
			}
			return &ParseResult{Transcript: t}, nil
		}
	}
	return nil, ErrSessionNotFound
}
func (s *ConcordStore) Save(ctx context.Context, t *Transcript, opts RenderOptions) (*SavedSession, error) {
	store, err := NewLocalFileStore(FormatConcord, s.ImportDir, ".json", flatJSONLayout)
	if err != nil {
		return nil, err
	}
	saved, err := store.Save(ctx, t, opts)
	if err != nil {
		return nil, err
	}
	saved.Warnings = append(saved.Warnings, Warning{Code: "concord_import_pending", Message: "Import file saved separately; confirm the import in Concord. Its conversation database was not modified"})
	return saved, nil
}
func (s *ConcordStore) Delete(context.Context, SessionRef) error { return ErrUnsupported }
func concordPaths() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return envOr("CONCORD_CONVERSATIONS_FILE", filepath.Join(home, "Library", "Application Support", "dev.october.concord", "conversations.json")), envOr("MOIRAI_CONCORD_IMPORT_DIR", filepath.Join(home, ".moirai", "concord-imports")), nil
}
