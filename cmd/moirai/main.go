package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	moirai "github.com/october-dev/moirai"
)

const version = "0.1.2"

type app struct {
	out io.Writer
	err io.Writer
}

func main() {
	a := app{out: os.Stdout, err: os.Stderr}
	if err := a.run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(a.err, "moirai:", moirai.ScrubTerminal(err.Error()))
		os.Exit(1)
	}
}

func (a app) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		a.usage()
		return flag.ErrHelp
	}
	switch args[0] {
	case "help", "-h", "--help":
		a.usage()
		return nil
	case "version", "--version":
		fmt.Fprintln(a.out, version)
		return nil
	case "formats":
		return a.formats(args[1:])
	case "inspect":
		return a.inspect(args[1:])
	case "convert":
		return a.convert(args[1:])
	case "list":
		return a.list(ctx, args[1:])
	case "show":
		return a.show(ctx, args[1:])
	case "search":
		return a.search(ctx, args[1:])
	case "export":
		return a.export(ctx, args[1:])
	case "import":
		return a.importSession(ctx, args[1:], false)
	case "continue":
		return a.importSession(ctx, args[1:], true)
	case "delete":
		return a.delete(ctx, args[1:])
	case "archive":
		return a.archive(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a app) usage() {
	fmt.Fprintln(a.err, `Moirai moves resumable AI-agent sessions between supported harnesses.

Usage:
  moirai formats [--json]
  moirai inspect <file|-> [--from format] [--json]
  moirai convert <file|-> --to format [--from format] [--out file]
  moirai list [--format format] [--json]
  moirai show <session-id> --format format [--json]
  moirai search <query> [--format format] [--limit n] [--json]
  moirai export <session-id> --format format [--out file]
  moirai import <file|-> --to format [--from format]
  moirai continue <file|session-id> --with format [--from format] [--no-launch]
  moirai delete <session-id> --format format --yes
  moirai archive create <file|-> [--from format] --out file.moirai
  moirai archive verify <file.moirai>`)
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		if argument == "-" || !strings.HasPrefix(argument, "-") {
			positionals = append(positionals, argument)
			continue
		}
		name := strings.TrimLeft(argument, "-")
		if before, _, found := strings.Cut(name, "="); found {
			name = before
		}
		definition := fs.Lookup(name)
		if definition == nil {
			return fmt.Errorf("unknown flag %q", argument)
		}
		flags = append(flags, argument)
		boolFlag, isBool := definition.Value.(interface{ IsBoolFlag() bool })
		if !strings.Contains(argument, "=") && !(isBool && boolFlag.IsBoolFlag()) {
			if index+1 >= len(args) {
				return fmt.Errorf("flag needs an argument: %s", argument)
			}
			index++
			flags = append(flags, args[index])
		}
	}
	return fs.Parse(append(flags, positionals...))
}

func (a app) formats(args []string) error {
	fs := newFlags("formats", a.err)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	infos := moirai.DefaultRegistry.Harnesses()
	if *asJSON {
		return writeJSON(a.out, infos)
	}
	for _, info := range infos {
		capabilities := []string{}
		if info.Capability.Read {
			capabilities = append(capabilities, "read")
		}
		if info.Capability.Write {
			capabilities = append(capabilities, "write")
		}
		if info.Capability.Discover {
			capabilities = append(capabilities, "discover")
		}
		if info.Capability.Continue {
			capabilities = append(capabilities, "continue")
		}
		if info.Capability.SourceOnly {
			capabilities = append(capabilities, "source-only")
		}
		fmt.Fprintf(a.out, "%-18s %-22s %s\n", info.Format, info.DisplayName, strings.Join(capabilities, ","))
	}
	return nil
}

func (a app) inspect(args []string) error {
	fs := newFlags("inspect", a.err)
	from := fs.String("from", "", "source format")
	asJSON := fs.Bool("json", false, "emit JSON")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum input bytes")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("inspect requires one input")
	}
	limits := inputLimits(*maxInput)
	parsed, format, err := parseFile(fs.Arg(0), moirai.Format(*from), limits)
	if err != nil {
		return err
	}
	result := struct {
		Format   moirai.Format    `json:"format"`
		Meta     moirai.Metadata  `json:"meta"`
		Messages int              `json:"messages"`
		Warnings []moirai.Warning `json:"warnings,omitempty"`
	}{format, parsed.Transcript.Meta, len(parsed.Transcript.Messages), parsed.Warnings}
	if *asJSON {
		return writeJSON(a.out, result)
	}
	fmt.Fprintf(a.out, "Format: %s\nID: %s\nMessages: %d\n", format, moirai.ScrubTerminal(result.Meta.ID), result.Messages)
	if result.Meta.Title != "" {
		fmt.Fprintln(a.out, "Title:", moirai.ScrubTerminal(result.Meta.Title))
	}
	if result.Meta.CWD != "" {
		fmt.Fprintln(a.out, "Working directory:", moirai.ScrubTerminal(result.Meta.CWD))
	}
	return nil
}

func (a app) convert(args []string) error {
	fs := newFlags("convert", a.err)
	from := fs.String("from", "", "source format")
	to := fs.String("to", "", "target format")
	out := fs.String("out", "-", "output path")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum input bytes")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *to == "" {
		return errors.New("convert requires one input and --to")
	}
	limits := inputLimits(*maxInput)
	data, err := readInput(fs.Arg(0), limits.MaxInputBytes)
	if err != nil {
		return err
	}
	format := moirai.Format(*from)
	if format == "" {
		format, err = moirai.DetectFormatWithLimits(data, limits)
		if err != nil {
			return err
		}
	}
	result, err := moirai.DefaultRegistry.Convert(data, format, moirai.Format(*to), moirai.ParseOptions{Limits: limits})
	if err != nil {
		return err
	}
	a.printWarnings(result.Warnings)
	return writeOutput(*out, result.Data, a.out)
}

func stores() (*moirai.StoreRegistry, error) { return moirai.DefaultStores() }

func (a app) list(ctx context.Context, args []string) error {
	fs := newFlags("list", a.err)
	format := fs.String("format", "", "filter format")
	asJSON := fs.Bool("json", false, "emit JSON")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum bytes per stored session")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	registry, err := stores()
	if err != nil {
		return err
	}
	var formats []moirai.Format
	if *format != "" {
		formats = []moirai.Format{moirai.Format(*format)}
	}
	limits := storeLimits(*maxInput)
	refs, warnings, err := registry.DiscoverWithLimits(ctx, limits, formats...)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(a.out, map[string]any{"sessions": refs, "warnings": warnings})
	}
	for _, ref := range refs {
		fmt.Fprintf(a.out, "%-14s %-38s %s\n", ref.Format, moirai.ScrubTerminal(ref.ID), moirai.ScrubTerminal(first(ref.Title, ref.CWD, ref.Timestamp)))
	}
	for _, warning := range warnings {
		fmt.Fprintln(a.err, "warning:", moirai.ScrubTerminal(warning.Message))
	}
	return nil
}

func (a app) show(ctx context.Context, args []string) error {
	fs := newFlags("show", a.err)
	format := fs.String("format", "", "session format")
	asJSON := fs.Bool("json", false, "emit canonical JSON")
	includeThinking := fs.Bool("thinking", false, "include reasoning in text")
	includeTools := fs.Bool("tools", true, "include tools in text")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum stored session bytes")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *format == "" {
		return errors.New("show requires a session id and --format")
	}
	parsed, err := loadStored(ctx, fs.Arg(0), moirai.Format(*format), storeLimits(*maxInput))
	if err != nil {
		return err
	}
	a.printWarnings(parsed.Warnings)
	if *asJSON {
		return writeJSON(a.out, parsed.Transcript)
	}
	fmt.Fprintln(a.out, moirai.ScrubTerminal(moirai.ToText(parsed.Transcript, moirai.TextOptions{IncludeMetadata: true, IncludeThinking: *includeThinking, IncludeTools: *includeTools, MaxBytes: 1 << 20})))
	return nil
}

func (a app) search(ctx context.Context, args []string) error {
	fs := newFlags("search", a.err)
	format := fs.String("format", "", "filter format")
	limit := fs.Int("limit", 20, "maximum hits")
	asJSON := fs.Bool("json", false, "emit JSON")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum bytes per stored session")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("search requires one query")
	}
	registry, err := stores()
	if err != nil {
		return err
	}
	var formats []moirai.Format
	if *format != "" {
		formats = []moirai.Format{moirai.Format(*format)}
	}
	limits := storeLimits(*maxInput)
	refs, _, err := registry.DiscoverWithLimits(ctx, limits, formats...)
	if err != nil {
		return err
	}
	type sessionHit struct {
		Session moirai.SessionRef `json:"session"`
		Hit     moirai.SearchHit  `json:"hit"`
	}
	var hits []sessionHit
	for _, ref := range refs {
		store, _ := registry.Store(ref.Format)
		parsed, loadErr := store.Load(ctx, ref, moirai.ParseOptions{Limits: limits})
		if loadErr != nil {
			continue
		}
		for _, hit := range moirai.Search(parsed.Transcript, fs.Arg(0), *limit) {
			hits = append(hits, sessionHit{ref, hit})
			if len(hits) >= *limit {
				break
			}
		}
		if len(hits) >= *limit {
			break
		}
	}
	if *asJSON {
		return writeJSON(a.out, hits)
	}
	for _, hit := range hits {
		fmt.Fprintf(a.out, "%s:%s#%d %s\n", hit.Session.Format, moirai.ScrubTerminal(hit.Session.ID), hit.Hit.MessageIndex, moirai.ScrubTerminal(hit.Hit.Text))
	}
	return nil
}

func (a app) export(ctx context.Context, args []string) error {
	fs := newFlags("export", a.err)
	format := fs.String("format", "", "session format")
	out := fs.String("out", "-", "output path")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum stored session bytes")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *format == "" {
		return errors.New("export requires a session id and --format")
	}
	limits := storeLimits(*maxInput)
	parsed, err := loadStored(ctx, fs.Arg(0), moirai.Format(*format), limits)
	if err != nil {
		return err
	}
	a.printWarnings(parsed.Warnings)
	rendered, err := (moirai.SimpleCodec{}).Render(parsed.Transcript, moirai.RenderOptions{Limits: limits})
	if err != nil {
		return err
	}
	return writeOutput(*out, rendered.Data, a.out)
}

func (a app) importSession(ctx context.Context, args []string, continuing bool) error {
	name := "import"
	if continuing {
		name = "continue"
	}
	fs := newFlags(name, a.err)
	from := fs.String("from", "", "source format for a file, or source store format for an id")
	to := fs.String("to", "", "target format")
	with := fs.String("with", "", "target harness")
	noLaunch := fs.Bool("no-launch", false, "save without starting the target harness")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum input or stored session bytes")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%s requires one input", name)
	}
	target := *to
	if continuing {
		target = *with
	}
	if target == "" {
		return errors.New("target format is required")
	}
	if continuing && !*noLaunch {
		codec, err := moirai.DefaultRegistry.Codec(moirai.Format(target))
		if err != nil {
			return err
		}
		if !codec.Info().Capability.Continue {
			return fmt.Errorf("%w: %s can be imported but cannot be launched into a specific session", moirai.ErrUnsupported, target)
		}
	}
	var transcript *moirai.Transcript
	var warnings []moirai.Warning
	var sourceFormat moirai.Format
	if *from != "" && !isInputFile(fs.Arg(0)) {
		parsed, err := loadStored(ctx, fs.Arg(0), moirai.Format(*from), storeLimits(*maxInput))
		if err != nil {
			return err
		}
		transcript = parsed.Transcript
		warnings = append(warnings, parsed.Warnings...)
		sourceFormat = moirai.Format(*from)
	} else {
		parsed, detected, err := parseFile(fs.Arg(0), moirai.Format(*from), inputLimits(*maxInput))
		if err != nil {
			return err
		}
		transcript = parsed.Transcript
		warnings = append(warnings, parsed.Warnings...)
		sourceFormat = detected
	}
	id, err := moirai.NewID()
	if err != nil {
		return err
	}
	copy := *transcript
	copy.Meta = transcript.Meta
	provenance := moirai.Provenance{}
	if transcript.Meta.Provenance != nil {
		provenance = *transcript.Meta.Provenance
	}
	provenance.SourceFormat = sourceFormat
	provenance.SourceSessionID = transcript.Meta.ID
	provenance.ImportedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if info, statErr := os.Stat(copy.Meta.CWD); copy.Meta.CWD == "" || statErr != nil || !info.IsDir() {
		original := copy.Meta.CWD
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return cwdErr
		}
		provenance.SourceCWD = original
		copy.Meta.CWD = cwd
		warnings = append(warnings, moirai.Warning{Code: "cwd_rehomed", Message: fmt.Sprintf("source working directory %q is unavailable; using %q", original, cwd)})
	}
	copy.Meta.Provenance = &provenance
	copy.Meta.ID = id
	registry, err := stores()
	if err != nil {
		return err
	}
	store, err := registry.Store(moirai.Format(target))
	if err != nil {
		return err
	}
	saved, err := store.Save(ctx, &copy, moirai.RenderOptions{Limits: storeLimits(*maxInput), ID: id})
	if err != nil {
		return err
	}
	saved.Warnings = append(warnings, saved.Warnings...)
	a.printWarnings(saved.Warnings)
	if !continuing || *noLaunch {
		return writeJSON(a.out, saved)
	}
	command, err := moirai.CommandFor(moirai.Format(target), saved.Ref)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.err, "saved %s session %s; launching %s\n", target, saved.Ref.ID, command.Program)
	return moirai.Launch(ctx, command)
}

func (a app) delete(ctx context.Context, args []string) error {
	fs := newFlags("delete", a.err)
	format := fs.String("format", "", "session format")
	yes := fs.Bool("yes", false, "confirm deletion")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum stored session bytes")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *format == "" || !*yes {
		return errors.New("delete requires a session id, --format, and --yes")
	}
	registry, err := stores()
	if err != nil {
		return err
	}
	store, err := registry.Store(moirai.Format(*format))
	if err != nil {
		return err
	}
	refs, _, err := registry.DiscoverWithLimits(ctx, storeLimits(*maxInput), moirai.Format(*format))
	if err != nil {
		return err
	}
	ref, err := moirai.FindSession(refs, fs.Arg(0), moirai.Format(*format))
	if err != nil {
		return err
	}
	return store.Delete(ctx, ref)
}

func (a app) archive(args []string) error {
	if len(args) == 0 {
		return errors.New("archive requires create or verify")
	}
	switch args[0] {
	case "create":
		fs := newFlags("archive create", a.err)
		from := fs.String("from", "", "source format")
		out := fs.String("out", "", "archive path")
		maxInput := fs.Int64("max-input-bytes", 0, "maximum input bytes")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 || *out == "" {
			return errors.New("archive create requires one input and --out")
		}
		limits := inputLimits(*maxInput)
		parsed, _, err := parseFile(fs.Arg(0), moirai.Format(*from), limits)
		if err != nil {
			return err
		}
		a.printWarnings(parsed.Warnings)
		encoded, err := moirai.EncodeArchive(parsed.Transcript, limits)
		if err != nil {
			return err
		}
		return writeOutput(*out, encoded, a.out)
	case "verify":
		fs := newFlags("archive verify", a.err)
		maxInput := fs.Int64("max-input-bytes", 0, "maximum archive bytes")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("archive verify requires one archive")
		}
		limits := inputLimits(*maxInput)
		data, err := readInput(fs.Arg(0), limits.MaxInputBytes)
		if err != nil {
			return err
		}
		transcript, err := moirai.DecodeArchive(data, limits)
		if err != nil {
			return err
		}
		return writeJSON(a.out, map[string]any{"valid": true, "id": transcript.Meta.ID, "messages": len(transcript.Messages)})
	default:
		return fmt.Errorf("unknown archive operation %q", args[0])
	}
}

func loadStored(ctx context.Context, selector string, format moirai.Format, limits moirai.Limits) (*moirai.ParseResult, error) {
	parsedSelector, err := moirai.ParseSelector(selector)
	if err != nil {
		return nil, err
	}
	registry, err := stores()
	if err != nil {
		return nil, err
	}
	store, err := registry.Store(format)
	if err != nil {
		return nil, err
	}
	refs, _, err := registry.DiscoverWithLimits(ctx, limits, format)
	if err != nil {
		return nil, err
	}
	ref, err := moirai.FindSession(refs, parsedSelector.SessionID, format)
	if err != nil {
		return nil, err
	}
	parsed, err := store.Load(ctx, ref, moirai.ParseOptions{Limits: limits})
	if err != nil || parsedSelector.Span == nil {
		return parsed, err
	}
	selected, err := moirai.Select(parsed.Transcript, *parsedSelector.Span)
	if err != nil {
		return nil, err
	}
	parsed.Transcript = selected
	return parsed, nil
}

func parseFile(path string, format moirai.Format, limits moirai.Limits) (*moirai.ParseResult, moirai.Format, error) {
	data, err := readInput(path, limits.MaxInputBytes)
	if err != nil {
		return nil, "", err
	}
	return moirai.Parse(data, format, moirai.ParseOptions{Limits: limits, SourceID: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))})
}

func readInput(path string, limit int64) ([]byte, error) {
	var reader io.Reader
	if path == "-" {
		reader = os.Stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	if limit <= 0 {
		limit = moirai.DefaultLimits().MaxInputBytes
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, moirai.ErrLimitExceeded
	}
	return data, nil
}

func writeOutput(path string, data []byte, stdout io.Writer) error {
	if path == "" || path == "-" {
		_, err := stdout.Write(data)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".moirai-output-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func (a app) printWarnings(warnings []moirai.Warning) {
	for _, warning := range warnings {
		location := warning.Path
		if location != "" {
			location += ": "
		}
		fmt.Fprintf(a.err, "warning: %s%s (%s)\n", moirai.ScrubTerminal(location), moirai.ScrubTerminal(warning.Message), moirai.ScrubTerminal(warning.Code))
	}
}

func inputLimits(maximum int64) moirai.Limits {
	limits := moirai.DefaultLimits()
	if maximum > 0 {
		limits.MaxInputBytes = maximum
	}
	return limits
}

func storeLimits(maximum int64) moirai.Limits {
	limits := moirai.DefaultStoreLimits()
	if maximum > 0 {
		limits.MaxInputBytes = maximum
	}
	return limits
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isInputFile(value string) bool {
	if value == "-" {
		return true
	}
	info, err := os.Stat(value)
	return err == nil && !info.IsDir()
}
