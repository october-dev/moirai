package moirai

import (
	"errors"
	"fmt"
)

var (
	ErrUnknownFormat      = errors.New("unknown session format")
	ErrInvalidTranscript  = errors.New("invalid transcript")
	ErrUnsupported        = errors.New("operation is not supported")
	ErrSourceOnly         = errors.New("format is source-only")
	ErrLimitExceeded      = errors.New("session safety limit exceeded")
	ErrUnsafePath         = errors.New("unsafe path")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionExists      = errors.New("session already exists")
	ErrIntegrity          = errors.New("archive integrity check failed")
	ErrUnsupportedVersion = errors.New("unsupported format version")
)

type PathError struct {
	Path string
	Err  error
}

func (e *PathError) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }
func (e *PathError) Unwrap() error { return e.Err }

type FormatError struct {
	Format Format
	Op     string
	Err    error
}

func (e *FormatError) Error() string { return fmt.Sprintf("%s %s: %v", e.Format, e.Op, e.Err) }
func (e *FormatError) Unwrap() error { return e.Err }

type DiscoveryError struct {
	Code  string
	Path  string
	Count int
	Err   error
}

func (e *DiscoveryError) Error() string {
	if e.Count > 1 {
		return fmt.Sprintf("%s (and %d more): %v", e.Path, e.Count-1, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Path, e.Err)
}

func (e *DiscoveryError) Unwrap() error { return e.Err }
