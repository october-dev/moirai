package moirai

import (
	"sort"
	"strings"
	"unicode"
)

type SearchHit struct {
	MessageIndex int    `json:"message_index"`
	BlockIndex   int    `json:"block_index"`
	Role         Role   `json:"role"`
	Kind         string `json:"kind"`
	Text         string `json:"text"`
	Score        int    `json:"score"`
}

func Search(t *Transcript, query string, limit int) []SearchHit {
	query = normalizeSearch(query)
	if t == nil || query == "" {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	hits := make([]SearchHit, 0)
	add := func(mi, bi int, role Role, kind, value string) {
		if score := matchScore(normalizeSearch(value), query); score > 0 {
			hits = append(hits, SearchHit{MessageIndex: mi + 1, BlockIndex: bi + 1, Role: role, Kind: kind, Text: boundedTail(value, 2_000), Score: score})
		}
	}
	for mi, message := range t.Messages {
		for bi, block := range message.Content {
			switch block.Type {
			case BlockText, BlockThinking:
				add(mi, bi, message.Role, string(block.Type), block.Text)
			case BlockToolUse:
				add(mi, bi, message.Role, "tool_use", block.Name+" "+string(block.Input))
			case BlockToolResult:
				add(mi, bi, message.Role, "tool_result", string(block.Content))
			case BlockArtifact:
				if block.Artifact != nil {
					add(mi, bi, message.Role, "artifact", block.Artifact.Name+" "+block.Artifact.Description)
				}
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].MessageIndex != hits[j].MessageIndex {
			return hits[i].MessageIndex > hits[j].MessageIndex
		}
		return hits[i].BlockIndex < hits[j].BlockIndex
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func normalizeSearch(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(value))
}

func matchScore(value, query string) int {
	if value == "" || query == "" {
		return 0
	}
	if index := strings.Index(value, query); index >= 0 {
		return 10_000 - minInt(index, 5_000) + len(query)
	}
	qi, gaps, last := 0, 0, -1
	for vi, r := range value {
		if qi >= len(query) {
			break
		}
		qr, width := utf8RuneAt(query, qi)
		if r == qr {
			if last >= 0 {
				gaps += vi - last - 1
			}
			last = vi
			qi += width
		}
	}
	if qi != len(query) {
		return 0
	}
	return maxInt(1, 1_000-gaps)
}

func utf8RuneAt(value string, index int) (rune, int) {
	for _, r := range value[index:] {
		return r, len(string(r))
	}
	return 0, 0
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
