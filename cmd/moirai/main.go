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
	"strconv"
	"strings"
	"time"

	moirai "github.com/october-dev/moirai"
)

const version = "0.2.0"

type app struct {
	in        io.ReadCloser
	out       io.Writer
	err       io.Writer
	newStores func() (*moirai.StoreRegistry, error)
	launch    func(context.Context, moirai.LaunchCommand) error
}

func main() {
	a := app{in: os.Stdin, out: os.Stdout, err: os.Stderr}
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
	case "completion":
		return a.completion(args[1:])
	case "formats":
		return a.formats(args[1:])
	case "doctor":
		return a.doctor(args[1:])
	case "mcp":
		return a.mcp(ctx, args[1:])
	case "login":
		return a.cloudLogin(ctx, args[1:])
	case "logout":
		return a.cloudLogout(ctx, args[1:])
	case "whoami":
		c, err := loadCloud("")
		if err != nil {
			return err
		}
		data, _, err := c.request(ctx, "GET", "/v1/me", nil, "")
		if err != nil {
			return err
		}
		_, err = a.out.Write(append(data, '\n'))
		return err
	case "team":
		return a.team(ctx, args[1:])
	case "publish":
		return a.publish(ctx, args[1:])
	case "pull":
		return a.pull(ctx, args[1:])
	case "fork":
		return a.fork(ctx, args[1:])
	case "unpublish", "cloud-delete", "invite":
		return a.cloudMutation(ctx, args[0], args[1:])
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
  moirai completion <bash|zsh|fish>
  moirai doctor [--json]
  moirai mcp
  moirai login [--server https://moirai.to]
  moirai logout
  moirai whoami
  moirai team list|create|members|invite|remove [arguments]
  moirai publish <file|session-id> [--from format] [--preview-out file] [--yes]
  moirai pull <share-url|id> --out session.moirai
  moirai fork <share-url|id> --yes
  moirai invite <id> --login github-handle
  moirai unpublish <id> --yes
  moirai cloud-delete <id> --yes
  moirai formats [--json]
  moirai inspect <file|-> [--from format] [--json]
  moirai convert <file|-> --to format [--from format] [--out file]
  moirai list [--format format] [--cwd path] [--since rfc3339] [--until rfc3339] [--limit n] [--json]
  moirai show <session-id> --format format [--json]
  moirai search <query> [--format format] [--limit n] [--json]
  moirai export <session-id> --format format [--out file]
  moirai import <file|-> --to format [--from format] [--dry-run] [--json]
  moirai continue <file|session-id> --with format [--from format] [--no-launch] [--dry-run] [--json]
  moirai delete <session-id> --format format --yes
  moirai archive create <file|-> [--from format] --out file.moirai
  moirai archive verify <file.moirai>
  moirai archive inspect <file.moirai> [--json]`)
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
		fmt.Fprintf(a.out, "%-18s %-22s %s\n", info.Format, info.DisplayName, strings.Join(capabilityNames(info.Capability), ","))
	}
	return nil
}

func capabilityNames(capability moirai.Capability) []string {
	names := []string{}
	if capability.Read {
		names = append(names, "read")
	}
	if capability.Write {
		names = append(names, "write")
	}
	if capability.Discover {
		names = append(names, "discover")
	}
	if capability.Continue {
		names = append(names, "continue")
	}
	if capability.SourceOnly {
		names = append(names, "source-only")
	}
	return names
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

func (a app) storeRegistry() (*moirai.StoreRegistry, error) {
	if a.newStores != nil {
		return a.newStores()
	}
	return stores()
}

func (a app) launchCmd(ctx context.Context, command moirai.LaunchCommand) error {
	if a.launch != nil {
		return a.launch(ctx, command)
	}
	return moirai.Launch(ctx, command)
}

func (a app) list(ctx context.Context, args []string) error {
	fs := newFlags("list", a.err)
	format := fs.String("format", "", "filter format")
	cwd := fs.String("cwd", "", "only sessions whose working directory is this path or below it")
	since := fs.String("since", "", "only sessions modified at or after this RFC 3339 time")
	until := fs.String("until", "", "only sessions modified at or before this RFC 3339 time")
	limit := fs.String("limit", "", "print at most this many sessions")
	asJSON := fs.Bool("json", false, "emit JSON")
	maxInput := fs.Int64("max-input-bytes", 0, "maximum bytes per stored session")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	filter, err := parseListFilter(*cwd, *since, *until, *limit)
	if err != nil {
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
	refs = filter.apply(refs)
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

// listFilter narrows discovered sessions after the registry has ordered them.
type listFilter struct {
	cwd          string     // absolute and cleaned; "" leaves the filter inactive
	since, until *time.Time // nil leaves the bound inactive; the zero instant is a valid bound
	limit        int        // 0 means unlimited
}

func parseListFilter(cwd, since, until, limit string) (listFilter, error) {
	var filter listFilter
	var err error
	if cwd != "" {
		if filter.cwd, err = filepath.Abs(cwd); err != nil {
			return listFilter{}, err
		}
	}
	if filter.since, err = parseListTime("since", since); err != nil {
		return listFilter{}, err
	}
	if filter.until, err = parseListTime("until", until); err != nil {
		return listFilter{}, err
	}
	if filter.since != nil && filter.until != nil && filter.since.After(*filter.until) {
		return listFilter{}, errors.New("--since must not be after --until")
	}
	if limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil || n <= 0 {
			return listFilter{}, fmt.Errorf("invalid --limit %q: must be a positive integer", limit)
		}
		filter.limit = n
	}
	return filter, nil
}

func parseListTime(name, value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("invalid --%s %q: expected an RFC 3339 timestamp such as 2026-01-02T15:04:05Z", name, value)
	}
	return &parsed, nil
}

// apply keeps refs in their incoming order and stops once limit entries match.
// Time bounds are inclusive and read the session's last-modified time, falling
// back to its start time; a session with neither, or with a malformed
// modified time, never matches an active bound.
func (f listFilter) apply(refs []moirai.SessionRef) []moirai.SessionRef {
	var out []moirai.SessionRef
	for _, ref := range refs {
		if f.cwd != "" && (ref.CWD == "" || !underDirectory(f.cwd, ref.CWD)) {
			continue
		}
		if f.since != nil || f.until != nil {
			modified, err := time.Parse(time.RFC3339, first(ref.ModifiedAt, ref.Timestamp))
			if err != nil || f.since != nil && modified.Before(*f.since) || f.until != nil && modified.After(*f.until) {
				continue
			}
		}
		out = append(out, ref)
		if f.limit > 0 && len(out) == f.limit {
			break
		}
	}
	return out
}

// underDirectory reports whether path is root itself or lies below it.
func underDirectory(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	dryRun := fs.Bool("dry-run", false, "render and preview without saving or launching")
	asJSON := fs.Bool("json", false, "emit a machine-readable preview")
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
	// Plain chat destinations save a new local conversation; there is no
	// application process to launch and no implicit remote account.
	if moirai.Format(target) == moirai.FormatChat {
		*noLaunch = true
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
	var selectedRange *moirai.Span
	if *from != "" && !isInputFile(fs.Arg(0)) {
		parsed, err := loadStored(ctx, fs.Arg(0), moirai.Format(*from), storeLimits(*maxInput))
		if err != nil {
			return err
		}
		transcript = parsed.Transcript
		warnings = append(warnings, parsed.Warnings...)
		sourceFormat = moirai.Format(*from)
		if *dryRun {
			selector, err := moirai.ParseSelector(fs.Arg(0))
			if err != nil {
				return err
			}
			selectedRange = selector.Span
		}
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
	if info, statErr := os.Stat(copy.Meta.CWD); moirai.Format(target) != moirai.FormatChat && moirai.Format(target) != moirai.FormatConcord && copy.SchemaVersion != moirai.ChatSchemaVersion && (copy.Meta.CWD == "" || statErr != nil || !info.IsDir()) {
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
	registry, err := a.storeRegistry()
	if err != nil {
		return err
	}
	store, err := registry.Store(moirai.Format(target))
	if err != nil {
		return err
	}
	if *dryRun {
		codec, err := moirai.DefaultRegistry.Codec(moirai.Format(target))
		if err != nil {
			return err
		}
		if !codec.Info().Capability.Save {
			return moirai.ErrUnsupported
		}
		rendered, err := codec.Render(&copy, moirai.RenderOptions{Limits: storeLimits(*maxInput), ID: id})
		if err != nil {
			return err
		}
		warnings = append(warnings, rendered.Warnings...)
		if selectedRange != nil {
			selectedRange.End = selectedRange.Start + len(copy.Messages) - 1
		}
		report := map[string]any{"dry_run": true, "source_format": sourceFormat, "target_format": target, "destination_store": store.Root(), "messages": len(copy.Messages), "rendered_bytes": len(rendered.Data), "launch": false, "warnings": warnings, "provenance": provenance, "range": selectedRange}
		if *asJSON {
			return writeJSON(a.out, report)
		}
		a.printWarnings(warnings)
		fmt.Fprintf(a.out, "Preview: %s → %s, %d messages, %d bytes\nDestination: %s\nLaunch: false\n", sourceFormat, target, len(copy.Messages), len(rendered.Data), moirai.ScrubTerminal(store.Root()))
		if selectedRange == nil {
			fmt.Fprintf(a.out, "Range: all %d messages\n", len(copy.Messages))
		} else {
			fmt.Fprintf(a.out, "Range: messages %d-%d\n", selectedRange.Start, selectedRange.End)
		}
		return nil
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
	return a.launchCmd(ctx, command)
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
		return errors.New("archive requires create, verify, or inspect")
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
	case "inspect":
		fs := newFlags("archive inspect", a.err)
		asJSON := fs.Bool("json", false, "emit JSON")
		maxInput := fs.Int64("max-input-bytes", 0, "maximum archive bytes")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("archive inspect requires one archive")
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
		summary, err := summarizeArchive(data, transcript)
		if err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(a.out, summary)
		}
		a.printArchiveSummary(summary)
		return nil
	default:
		return fmt.Errorf("unknown archive operation %q", args[0])
	}
}

// archiveSummary is the exact allowlist of what archive inspect reveals about a
// verified archive. Message bodies, block payloads, and every extra field stay
// out unless a field is added here deliberately.
type archiveSummary struct {
	Format        string                   `json:"format"`
	Version       string                   `json:"version"`
	SchemaVersion string                   `json:"schema_version"`
	CreatedAt     string                   `json:"created_at,omitempty"`
	SHA256        string                   `json:"sha256"`
	Valid         bool                     `json:"valid"`
	ID            string                   `json:"id"`
	Title         string                   `json:"title,omitempty"`
	Timestamp     string                   `json:"timestamp,omitempty"`
	UpdatedAt     string                   `json:"updated_at,omitempty"`
	CWD           string                   `json:"cwd,omitempty"`
	Model         string                   `json:"model,omitempty"`
	Provenance    *moirai.Provenance       `json:"provenance,omitempty"`
	Messages      int                      `json:"messages"`
	Blocks        map[moirai.BlockType]int `json:"blocks"`
	// Warnings counts warnings raised by this inspection. Archives do not
	// retain source-conversion warnings, so it is zero for every archive that
	// DecodeArchive accepts today.
	Warnings int `json:"warnings"`
}

// archiveBlockTypes fixes the human output order. Validate rejects any other
// block type, so the list is complete for a decoded archive.
var archiveBlockTypes = []moirai.BlockType{moirai.BlockText, moirai.BlockThinking, moirai.BlockToolUse, moirai.BlockToolResult, moirai.BlockImage, moirai.BlockArtifact, moirai.BlockUnknown}

// summarizeArchive builds the summary for an archive that DecodeArchive has
// already accepted, so the envelope is re-read only after the digest passed.
func summarizeArchive(data []byte, transcript *moirai.Transcript) (archiveSummary, error) {
	var envelope struct {
		CreatedAt string `json:"created_at"`
		SHA256    string `json:"sha256"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return archiveSummary{}, err
	}
	blocks := make(map[moirai.BlockType]int, len(archiveBlockTypes))
	for _, blockType := range archiveBlockTypes {
		blocks[blockType] = 0
	}
	for _, message := range transcript.Messages {
		for _, block := range message.Content {
			blocks[block.Type]++
		}
	}
	return archiveSummary{
		Format:        "moirai.session",
		Version:       moirai.ArchiveVersion,
		SchemaVersion: transcript.SchemaVersion,
		CreatedAt:     envelope.CreatedAt,
		SHA256:        envelope.SHA256,
		Valid:         true,
		ID:            transcript.Meta.ID,
		Title:         transcript.Meta.Title,
		Timestamp:     transcript.Meta.Timestamp,
		UpdatedAt:     transcript.Meta.UpdatedAt,
		CWD:           transcript.Meta.CWD,
		Model:         transcript.Meta.Model,
		Provenance:    transcript.Meta.Provenance,
		Messages:      len(transcript.Messages),
		Blocks:        blocks,
	}, nil
}

func (a app) printArchiveSummary(summary archiveSummary) {
	a.printField("Format", fmt.Sprintf("%s %s (schema %s)", summary.Format, summary.Version, summary.SchemaVersion))
	a.printField("Transcript digest", "sha256 "+summary.SHA256+" verified")
	a.printField("Created", summary.CreatedAt)
	a.printField("ID", summary.ID)
	a.printField("Title", summary.Title)
	a.printField("Timestamp", summary.Timestamp)
	a.printField("Updated", summary.UpdatedAt)
	a.printField("Working directory", summary.CWD)
	a.printField("Model", summary.Model)
	if provenance := summary.Provenance; provenance != nil {
		a.printField("Source format", string(provenance.SourceFormat))
		a.printField("Source session", provenance.SourceSessionID)
		a.printField("Imported", provenance.ImportedAt)
		a.printField("Parent session", provenance.ParentSessionID)
		a.printField("Parent checkpoint", provenance.ParentCheckpoint)
		a.printField("Source working directory", provenance.SourceCWD)
	}
	total := 0
	for _, count := range summary.Blocks {
		total += count
	}
	fmt.Fprintf(a.out, "Messages: %d\nBlocks: %d\n", summary.Messages, total)
	for _, blockType := range archiveBlockTypes {
		fmt.Fprintf(a.out, "  %s: %d\n", blockType, summary.Blocks[blockType])
	}
	fmt.Fprintf(a.out, "Warnings: %d\n", summary.Warnings)
}

// printField writes one "Label: value" line and skips empty values. The value
// is terminal-scrubbed and its newlines collapsed, so untrusted text such as
// "x\nID: spoof" cannot forge a second field.
func (a app) printField(label, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(a.out, "%s: %s\n", label, strings.ReplaceAll(moirai.ScrubTerminal(value), "\n", " "))
}

func loadStored(ctx context.Context, selector string, format moirai.Format, limits moirai.Limits) (*moirai.ParseResult, error) {
	registry, err := stores()
	if err != nil {
		return nil, err
	}
	return loadStoredFrom(ctx, registry, selector, format, limits)
}

func loadStoredFrom(ctx context.Context, registry *moirai.StoreRegistry, selector string, format moirai.Format, limits moirai.Limits) (*moirai.ParseResult, error) {
	parsedSelector, err := moirai.ParseSelector(selector)
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
	var envelope struct {
		Format string `json:"format"`
	}
	if json.Unmarshal(data, &envelope) == nil && envelope.Format == "moirai.session" {
		transcript, err := moirai.DecodeArchive(data, limits)
		if err != nil {
			return nil, "", err
		}
		return &moirai.ParseResult{Transcript: transcript}, moirai.FormatSimple, nil
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
