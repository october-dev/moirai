package moirai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const ArchiveVersion = "1"

type Archive struct {
	Format     string          `json:"format"`
	Version    string          `json:"version"`
	CreatedAt  string          `json:"created_at"`
	Transcript json.RawMessage `json:"transcript"`
	SHA256     string          `json:"sha256"`
}

func EncodeArchive(t *Transcript, limits Limits) ([]byte, error) {
	if err := Validate(t, limits); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	archive := Archive{Format: "moirai.session", Version: ArchiveVersion, CreatedAt: nowRFC3339(), Transcript: payload, SHA256: hex.EncodeToString(digest[:])}
	encoded, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func DecodeArchive(data []byte, limits Limits) (*Transcript, error) {
	limits = limits.normalized()
	if int64(len(data)) > limits.MaxInputBytes {
		return nil, ErrLimitExceeded
	}
	if err := checkJSONDepth(data, limits.MaxNestingDepth+1); err != nil {
		return nil, err
	}
	var archive Archive
	if err := json.Unmarshal(data, &archive); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTranscript, err)
	}
	if archive.Format != "moirai.session" || archive.Version != ArchiveVersion {
		return nil, fmt.Errorf("%w: archive %q/%q", ErrUnsupportedVersion, archive.Format, archive.Version)
	}
	var transcript Transcript
	if err := json.Unmarshal(archive.Transcript, &transcript); err != nil {
		return nil, fmt.Errorf("%w: transcript: %v", ErrInvalidTranscript, err)
	}
	canonical, err := json.Marshal(&transcript)
	if err != nil {
		return nil, fmt.Errorf("%w: transcript: %v", ErrInvalidTranscript, err)
	}
	digest := sha256.Sum256(canonical)
	if archive.SHA256 != hex.EncodeToString(digest[:]) {
		return nil, ErrIntegrity
	}
	if err := Validate(&transcript, limits); err != nil {
		return nil, err
	}
	return &transcript, nil
}
