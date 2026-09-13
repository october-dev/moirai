// Package sharing prepares an explicit publication without modifying its source.
package sharing

import (
	"encoding/json"
	"regexp"

	moirai "github.com/october-dev/moirai"
)

var secrets = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|AKIA[A-Z0-9]{16})`)

type Report struct {
	Messages          int      `json:"messages"`
	ThinkingRemoved   int      `json:"thinking_removed"`
	LocalMediaRemoved int      `json:"local_media_removed"`
	SecretMatches     int      `json:"secret_matches"`
	Warnings          []string `json:"warnings"`
}

func Prepare(t *moirai.Transcript, includeThinking bool) (*moirai.Transcript, Report, error) {
	report := Report{Warnings: []string{"Pattern matching cannot identify every secret. Review the complete prepared archive, especially tool inputs and outputs, metadata, and inline media.", "Local file references are not uploaded; prepare the repository and required files on the destination machine."}}
	data, err := json.Marshal(t)
	if err != nil {
		return nil, report, err
	}
	report.SecretMatches = len(secrets.FindAll(data, -1))
	data = secrets.ReplaceAll(data, []byte("[REDACTED]"))
	var copy moirai.Transcript
	if err = json.Unmarshal(data, &copy); err != nil {
		return nil, report, err
	}
	copy.Meta.CWD = ""
	if copy.Meta.Provenance != nil {
		copy.Meta.Provenance.SourceCWD = ""
	}
	for i := range copy.Messages {
		blocks := []moirai.Block{}
		for _, block := range copy.Messages[i].Content {
			if block.Type == moirai.BlockThinking && !includeThinking {
				report.ThinkingRemoved++
				continue
			}
			if block.Source != nil && block.Source.Type == "path" {
				block = moirai.Block{Type: moirai.BlockText, Text: "[Local media omitted]"}
				report.LocalMediaRemoved++
			}
			if block.Artifact != nil && block.Artifact.Source != nil && block.Artifact.Source.Type == "path" {
				block.Artifact.Source = nil
				report.LocalMediaRemoved++
			}
			blocks = append(blocks, block)
		}
		if len(blocks) == 0 && (copy.SchemaVersion != moirai.ChatSchemaVersion || len(copy.Messages[i].Content) > 0) {
			blocks = append(blocks, moirai.Block{Type: moirai.BlockText, Text: "[Thinking omitted]"})
		}
		copy.Messages[i].Content = blocks
	}
	report.Messages = len(copy.Messages)
	if err = moirai.Validate(&copy, moirai.DefaultLimits()); err != nil {
		return nil, report, err
	}
	return &copy, report, nil
}
