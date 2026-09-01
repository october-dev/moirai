package moirai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type CoworkStore struct{ SessionsRoot string }

func (s *CoworkStore) Format() Format { return FormatCowork }
func (s *CoworkStore) Root() string   { return s.SessionsRoot }

func (s *CoworkStore) accountDirs() []string {
	var result []string
	organizations, _ := os.ReadDir(s.SessionsRoot)
	for _, organization := range organizations {
		if !organization.IsDir() || !uuidDirectory(organization.Name()) {
			continue
		}
		orgPath := filepath.Join(s.SessionsRoot, organization.Name())
		accounts, _ := os.ReadDir(orgPath)
		for _, account := range accounts {
			if account.IsDir() && uuidDirectory(account.Name()) {
				result = append(result, filepath.Join(orgPath, account.Name()))
			}
		}
	}
	sort.Strings(result)
	return result
}

func uuidDirectory(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

func coworkRecords(account string) []string {
	var records []string
	for _, directory := range []string{account, filepath.Join(account, "agent")} {
		entries, _ := os.ReadDir(directory)
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "local_") && filepath.Ext(entry.Name()) == ".json" {
				records = append(records, filepath.Join(directory, entry.Name()))
			}
		}
	}
	sort.Strings(records)
	return records
}

func (s *CoworkStore) Discover(ctx context.Context) ([]SessionRef, error) {
	if _, err := os.Stat(s.SessionsRoot); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var refs []SessionRef
	for _, account := range s.accountDirs() {
		for _, record := range coworkRecords(account) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rel, err := filepath.Rel(s.SessionsRoot, record)
			if err != nil {
				continue
			}
			ref := SessionRef{Format: FormatCowork, ID: strings.TrimSuffix(filepath.Base(record), ".json"), Location: rel}
			data, readErr := readFileLimited(record, int64(DefaultLimits().MaxMetadataBytes))
			if readErr != nil {
				continue
			}
			var header map[string]any
			if decodeJSONDocument(data, &header, DefaultLimits()) != nil {
				continue
			}
			info, _ := os.Stat(record)
			ref.ID = firstNonEmpty(stringValue(header["sessionId"]), ref.ID)
			ref.Title = stringValue(header["title"])
			ref.CWD = stringValue(header["cwd"])
			ref.Model = stringValue(header["model"])
			ref.Timestamp = timestampValue(header["createdAt"])
			ref.ModifiedAt = fileModified(info)
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func (s *CoworkStore) Load(ctx context.Context, ref SessionRef, opts ParseOptions) (*ParseResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, err := checkedPath(s.SessionsRoot, ref.Location, true)
	if err != nil {
		return nil, err
	}
	headerData, err := readFileLimited(record, opts.Limits.normalized().MaxInputBytes)
	if err != nil {
		return nil, err
	}
	var header map[string]any
	if err := decodeJSONDocument(headerData, &header, opts.Limits); err != nil || stringValue(header["sessionId"]) == "" {
		return nil, fmt.Errorf("%w: invalid Cowork session record", ErrInvalidTranscript)
	}
	directory := strings.TrimSuffix(record, ".json")
	cliID := stringValue(header["cliSessionId"])
	var transcript []any
	if cliID != "" && safeID(cliID) == nil {
		projects := filepath.Join(directory, ".claude", "projects")
		_ = filepath.WalkDir(projects, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			if entry.Name() == cliID+".jsonl" {
				data, readErr := readFileLimited(path, opts.Limits.normalized().MaxInputBytes)
				if readErr == nil {
					records, _, _ := decodeJSONLines(data, opts.Limits)
					for _, item := range records {
						transcript = append(transcript, item)
					}
				}
				return fs.SkipAll
			}
			return nil
		})
	}
	var audit []any
	if data, readErr := readFileLimited(filepath.Join(directory, "audit.jsonl"), opts.Limits.normalized().MaxInputBytes); readErr == nil {
		records, _, _ := decodeJSONLines(data, opts.Limits)
		for _, item := range records {
			audit = append(audit, item)
		}
	}
	body, _ := json.Marshal(map[string]any{"header": header, "transcript": transcript, "audit": audit})
	return (CoworkCodec{}).Parse(body, opts)
}

func (s *CoworkStore) activeAccount() (string, error) {
	accounts := s.accountDirs()
	if len(accounts) == 0 {
		return "", fmt.Errorf("%w: open Cowork once so it creates an account session directory", ErrUnsupported)
	}
	best := accounts[0]
	var bestTime time.Time
	for _, account := range accounts {
		for _, record := range coworkRecords(account) {
			if info, err := os.Stat(record); err == nil && info.ModTime().After(bestTime) {
				best, bestTime = account, info.ModTime()
			}
		}
	}
	return best, nil
}

func (s *CoworkStore) Save(ctx context.Context, transcript *Transcript, opts RenderOptions) (*SavedSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := firstNonEmpty(opts.ID, transcript.Meta.ID)
	if !strings.HasPrefix(id, "local_") {
		id = "local_" + id
	}
	if err := safeID(id); err != nil {
		return nil, err
	}
	rendered, err := (CoworkCodec{}).Render(transcript, RenderOptions{Limits: opts.Limits, ID: id})
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(rendered.Data, &body); err != nil {
		return nil, err
	}
	header := object(body["header"])
	cliID := stringValue(header["cliSessionId"])
	if err := safeID(cliID); err != nil {
		return nil, err
	}
	account, err := s.activeAccount()
	if err != nil {
		return nil, err
	}
	record := filepath.Join(account, id+".json")
	directory := filepath.Join(account, id)
	if _, err := os.Stat(record); err == nil {
		return nil, ErrSessionExists
	}
	release, err := reservePath(record)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err := os.Stat(record); err == nil {
		return nil, ErrSessionExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	stage, err := os.MkdirTemp(account, ".moirai-cowork-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	project := filepath.Join(stage, ".claude", "projects", encodeClaudeProject(stringValue(header["cwd"])))
	if err := os.MkdirAll(project, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(stage, "outputs"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(stage, "uploads"), 0o700); err != nil {
		return nil, err
	}
	transcriptData, err := encodeJSONLines(array(body["transcript"]))
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(filepath.Join(project, cliID+".jsonl"), transcriptData, 0o600); err != nil {
		return nil, err
	}
	if len(array(body["audit"])) > 0 {
		audit, err := encodeJSONLines(array(body["audit"]))
		if err != nil {
			return nil, err
		}
		if err := atomicWrite(filepath.Join(stage, "audit.jsonl"), audit, 0o600); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(stage, directory); err != nil {
		return nil, err
	}
	headerData, _ := json.Marshal(header)
	if err := atomicWrite(record, append(headerData, '\n'), 0o600); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	rel, _ := filepath.Rel(s.SessionsRoot, record)
	return &SavedSession{Ref: SessionRef{Format: FormatCowork, ID: id, Location: rel, Title: transcript.Meta.Title, CWD: transcript.Meta.CWD, Model: transcript.Meta.Model, Timestamp: transcript.Meta.Timestamp}, Created: true, Warnings: rendered.Warnings}, nil
}

func (s *CoworkStore) Delete(ctx context.Context, ref SessionRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := checkedPath(s.SessionsRoot, ref.Location, true)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(s.SessionsRoot)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, record)
	if err != nil {
		return err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	validDepth := len(parts) == 3 || len(parts) == 4 && parts[2] == "agent"
	if !validDepth || !strings.HasPrefix(filepath.Base(record), "local_") || filepath.Ext(record) != ".json" {
		return ErrUnsafePath
	}
	if err := os.RemoveAll(strings.TrimSuffix(record, ".json")); err != nil {
		return err
	}
	return os.Remove(record)
}
