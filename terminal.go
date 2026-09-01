package moirai

import "strings"

// ScrubTerminal removes terminal control characters from untrusted transcript
// text while preserving line breaks for human-readable output.
func ScrubTerminal(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r >= 0x7f && r <= 0x9f {
			return ' '
		}
		return r
	}, value)
}
