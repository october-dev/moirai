package moirai

import (
	"fmt"
	"sync"
)

type Codec interface {
	Format() Format
	Info() HarnessInfo
	Parse(data []byte, opts ParseOptions) (*ParseResult, error)
	Render(t *Transcript, opts RenderOptions) (*RenderResult, error)
}

type Registry struct {
	mu     sync.RWMutex
	codecs map[Format]Codec
}

func NewRegistry(codecs ...Codec) *Registry {
	r := &Registry{codecs: make(map[Format]Codec, len(codecs))}
	for _, codec := range codecs {
		_ = r.Register(codec)
	}
	return r
}

func (r *Registry) Register(codec Codec) error {
	if codec == nil || codec.Format() == "" {
		return fmt.Errorf("%w: empty codec", ErrUnknownFormat)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.codecs[codec.Format()]; exists {
		return fmt.Errorf("codec already registered: %s", codec.Format())
	}
	r.codecs[codec.Format()] = codec
	return nil
}

func (r *Registry) Codec(format Format) (Codec, error) {
	r.mu.RLock()
	codec := r.codecs[format]
	r.mu.RUnlock()
	if codec == nil {
		return nil, &FormatError{Format: format, Op: "lookup", Err: ErrUnknownFormat}
	}
	return codec, nil
}

func (r *Registry) Harnesses() []HarnessInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]HarnessInfo, 0, len(r.codecs))
	for _, f := range Formats {
		if codec := r.codecs[f]; codec != nil {
			result = append(result, codec.Info())
		}
	}
	return result
}

func (r *Registry) Convert(data []byte, from, to Format, opts ParseOptions) (*RenderResult, error) {
	source, err := r.Codec(from)
	if err != nil {
		return nil, err
	}
	target, err := r.Codec(to)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > opts.Limits.normalized().MaxInputBytes {
		return nil, ErrLimitExceeded
	}
	parsed, err := source.Parse(data, opts)
	if err != nil {
		return nil, &FormatError{Format: from, Op: "parse", Err: err}
	}
	if from != to {
		original := parsed.Transcript.Meta.ID
		id, idErr := NewID()
		if idErr != nil {
			return nil, idErr
		}
		parsed.Transcript.Meta.ID = id
		parsed.Transcript.Meta.Provenance = &Provenance{SourceFormat: from, SourceSessionID: original, ImportedAt: nowRFC3339()}
	}
	rendered, err := target.Render(parsed.Transcript, RenderOptions{Limits: opts.Limits})
	if err != nil {
		return nil, &FormatError{Format: to, Op: "render", Err: err}
	}
	rendered.Warnings = append(parsed.Warnings, rendered.Warnings...)
	return rendered, nil
}

var DefaultRegistry = NewRegistry()

func Register(codec Codec) error { return DefaultRegistry.Register(codec) }
func Convert(data []byte, from, to Format) (*RenderResult, error) {
	return DefaultRegistry.Convert(data, from, to, ParseOptions{Limits: DefaultLimits()})
}
