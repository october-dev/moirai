package moirai

type Limits struct {
	MaxInputBytes       int64
	MaxMessages         int
	MaxBlocks           int
	MaxTextBytes        int
	MaxInlineMediaBytes int
	MaxMetadataBytes    int
	MaxNestingDepth     int
}

// DefaultStoreMaxInputBytes is deliberately separate from the lower default
// used for untrusted standalone input. Agent histories commonly exceed 32 MiB.
const DefaultStoreMaxInputBytes int64 = 512 << 20

func DefaultLimits() Limits {
	return Limits{MaxInputBytes: 32 << 20, MaxMessages: 100_000, MaxBlocks: 500_000, MaxTextBytes: 1 << 20, MaxInlineMediaBytes: 16 << 20, MaxMetadataBytes: 1 << 20, MaxNestingDepth: 64}
}

func DefaultStoreLimits() Limits {
	limits := DefaultLimits()
	limits.MaxInputBytes = DefaultStoreMaxInputBytes
	return limits
}

func (l Limits) storeNormalized() Limits {
	if l.MaxInputBytes <= 0 {
		l.MaxInputBytes = DefaultStoreMaxInputBytes
	}
	return l.normalized()
}

func (l Limits) normalized() Limits {
	d := DefaultLimits()
	if l.MaxInputBytes <= 0 {
		l.MaxInputBytes = d.MaxInputBytes
	}
	if l.MaxMessages <= 0 {
		l.MaxMessages = d.MaxMessages
	}
	if l.MaxBlocks <= 0 {
		l.MaxBlocks = d.MaxBlocks
	}
	if l.MaxTextBytes <= 0 {
		l.MaxTextBytes = d.MaxTextBytes
	}
	if l.MaxInlineMediaBytes <= 0 {
		l.MaxInlineMediaBytes = d.MaxInlineMediaBytes
	}
	if l.MaxMetadataBytes <= 0 {
		l.MaxMetadataBytes = d.MaxMetadataBytes
	}
	if l.MaxNestingDepth <= 0 {
		l.MaxNestingDepth = d.MaxNestingDepth
	}
	return l
}

type ParseOptions struct {
	Limits   Limits
	SourceID string
	Now      func() string
}
type RenderOptions struct {
	Limits Limits
	ID     string
	Now    func() string
}
