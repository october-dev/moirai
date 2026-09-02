package moirai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
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
	canonical, err := canonicalTranscriptJSON(t)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
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
	if err := decodeTranscriptStrict(archive.Transcript, &transcript); err != nil {
		return nil, fmt.Errorf("%w: transcript: %v", ErrInvalidTranscript, err)
	}
	canonical, err := canonicalTranscriptJSON(&transcript)
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

// canonicalTranscriptJSON defines the language-independent archive digest
// input. Object keys are sorted, strings are UTF-8 JSON strings without HTML
// escaping, and every finite JSON number uses ECMAScript's shortest form.
func canonicalTranscriptJSON(transcript *Transcript) ([]byte, error) {
	payload, err := json.Marshal(transcript)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := writeCanonicalJSON(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeCanonicalJSON(output *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(value))
	case string:
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			return err
		}
		text := strings.TrimSuffix(encoded.String(), "\n")
		text = strings.ReplaceAll(text, `\u2028`, string(rune(0x2028)))
		text = strings.ReplaceAll(text, `\u2029`, string(rune(0x2029)))
		output.WriteString(text)
	case json.Number:
		encoded, err := canonicalJSONNumber(value)
		if err != nil {
			return err
		}
		output.WriteString(encoded)
	case []any:
		output.WriteByte('[')
		for index, child := range value {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonicalJSON(output, child); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonicalJSON(output, key); err != nil {
				return err
			}
			output.WriteByte(':')
			if err := writeCanonicalJSON(output, value[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("%w: unsupported canonical JSON value %T", ErrInvalidTranscript, value)
	}
	return nil
}

func canonicalJSONNumber(value json.Number) (string, error) {
	number, err := strconv.ParseFloat(string(value), 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
		return "", fmt.Errorf("%w: invalid JSON number", ErrInvalidTranscript)
	}
	if number == 0 {
		return "0", nil
	}
	negative := number < 0
	if negative {
		number = -number
	}
	mantissa, exponentText, found := strings.Cut(strconv.FormatFloat(number, 'e', -1, 64), "e")
	if !found {
		return "", fmt.Errorf("%w: invalid JSON number", ErrInvalidTranscript)
	}
	exponent, err := strconv.Atoi(exponentText)
	if err != nil {
		return "", fmt.Errorf("%w: invalid JSON number", ErrInvalidTranscript)
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	k := len(digits)
	n := exponent + 1
	var output strings.Builder
	if negative {
		output.WriteByte('-')
	}
	switch {
	case k <= n && n <= 21:
		output.WriteString(digits)
		output.WriteString(strings.Repeat("0", n-k))
	case 0 < n && n <= 21:
		output.WriteString(digits[:n])
		output.WriteByte('.')
		output.WriteString(digits[n:])
	case -6 < n && n <= 0:
		output.WriteString("0.")
		output.WriteString(strings.Repeat("0", -n))
		output.WriteString(digits)
	default:
		output.WriteByte(digits[0])
		if k > 1 {
			output.WriteByte('.')
			output.WriteString(digits[1:])
		}
		output.WriteByte('e')
		scientificExponent := n - 1
		if scientificExponent >= 0 {
			output.WriteByte('+')
		} else {
			output.WriteByte('-')
			scientificExponent = -scientificExponent
		}
		output.WriteString(strconv.Itoa(scientificExponent))
	}
	return output.String(), nil
}

func decodeTranscriptStrict(data []byte, transcript *Transcript) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(transcript); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple transcript documents")
		}
		return err
	}
	return nil
}
