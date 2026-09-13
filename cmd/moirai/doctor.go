package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strings"

	moirai "github.com/october-dev/moirai"
)

// doctorRow is one harness in the doctor report. A nullable field is omitted
// from JSON when its check is unknown or does not apply to the harness.
type doctorRow struct {
	Format         moirai.Format     `json:"format"`
	DisplayName    string            `json:"display_name"`
	Capabilities   moirai.Capability `json:"capabilities"`
	Executable     string            `json:"executable,omitempty"`
	Installed      *bool             `json:"installed,omitempty"`
	Store          string            `json:"store,omitempty"`
	StoreOverrides []string          `json:"store_overrides,omitempty"`
	ActiveOverride string            `json:"active_override,omitempty"`
	Exists         *bool             `json:"exists,omitempty"`
	Readable       *bool             `json:"readable,omitempty"`
	Writable       *bool             `json:"writable,omitempty"`
	Warnings       []moirai.Warning  `json:"warnings,omitempty"`
}

var fileBackedFormats = map[moirai.Format]bool{
	moirai.FormatConcord:  true,
	moirai.FormatOpenCode: true,
	moirai.FormatHermes:   true,
}

func (a app) doctor(args []string) error {
	flags := newFlags("doctor", a.err)
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor takes no arguments")
	}
	registry, err := stores()
	if err != nil {
		return err
	}
	rows := make([]doctorRow, 0, len(moirai.Formats))
	for _, info := range moirai.DefaultRegistry.Harnesses() {
		row := doctorRow{Format: info.Format, DisplayName: info.DisplayName, Capabilities: info.Capability}
		if command, err := moirai.CommandFor(info.Format, moirai.SessionRef{}); err == nil {
			row.checkExecutable(command.Program)
		}
		if store, err := registry.Store(info.Format); err == nil {
			row.checkStore(store)
		}
		rows = append(rows, row)
	}
	if *asJSON {
		return writeJSON(a.out, rows)
	}
	for index, row := range rows {
		if index > 0 {
			fmt.Fprintln(a.out)
		}
		row.print(a.out)
	}
	return nil
}

func (r *doctorRow) checkExecutable(program string) {
	r.Executable = program
	_, err := exec.LookPath(program)
	r.Installed = boolPtr(err == nil)
	if err != nil {
		r.warn("", "executable_missing", fmt.Sprintf("%s is not on PATH; install %s or add it to PATH", program, r.DisplayName))
	}
}

// checkStore reports on the store root without opening anything whose type is
// unexpected. Stat follows symlinks to legitimate roots while allowing the
// kind gate to reject pipes, sockets, and devices before opening the root.
func (r *doctorRow) checkStore(store moirai.Store) {
	root := store.Root()
	r.Store = root
	r.StoreOverrides = storeOverrides(r.Format)
	r.ActiveOverride = activeOverride(r.StoreOverrides)
	wantFile := fileBackedFormats[r.Format]
	info, err := os.Stat(root)
	if errors.Is(err, fs.ErrNotExist) {
		r.Exists = boolPtr(false)
		r.warn(root, "store_missing", "store root does not exist; run "+r.DisplayName+" once"+r.overrideHint())
		return
	}
	if err != nil {
		r.warn(root, "store_stat_failed", fmt.Sprintf("cannot inspect store root: %v; check ownership and permissions of its parent directories", underlyingError(err)))
		return
	}
	r.Exists = boolPtr(true)
	mode := info.Mode()
	if wantFile && !mode.IsRegular() || !wantFile && !mode.IsDir() {
		expected := "a directory"
		if wantFile {
			expected = "a database file"
		}
		r.warn(root, "store_wrong_type", fmt.Sprintf("store root is %s but %s is expected; move it aside%s", describeMode(mode), expected, r.overrideHint()))
		return
	}
	file, err := os.Open(root)
	if err == nil {
		err = file.Close()
	}
	r.Readable = boolPtr(err == nil)
	if err != nil {
		r.warn(root, "store_unreadable", fmt.Sprintf("store root readability check failed: %v; fix its ownership or permissions", underlyingError(err)))
	}
	if wantFile || !r.Capabilities.Save {
		return
	}
	r.Writable = writableDir(root)
	if r.Writable != nil && !*r.Writable {
		r.warn(root, "store_unwritable", "store root is not writable by the current user; fix its ownership or permissions")
	}
}

func (r *doctorRow) warn(path, code, message string) {
	r.Warnings = append(r.Warnings, moirai.Warning{Path: path, Code: code, Message: message})
}

func (r doctorRow) overrideHint() string {
	if len(r.StoreOverrides) == 0 {
		return ""
	}
	return ", or set " + strings.Join(r.StoreOverrides, " or ") + " if its data lives elsewhere"
}

func (r doctorRow) print(w io.Writer) {
	fmt.Fprintf(w, "%s  %s  %s\n", r.Format, r.DisplayName, strings.Join(capabilityNames(r.Capabilities), ","))
	if r.Executable != "" {
		label := "executable"
		if r.Format == moirai.FormatCowork || r.Format == moirai.FormatCursorDesktop {
			label = "launcher"
		}
		state := "found"
		if !*r.Installed {
			state = "not on PATH"
		}
		fmt.Fprintf(w, "  %s: %s (%s)\n", label, r.Executable, state)
	}
	fmt.Fprintf(w, "  store: %s\n", r.storeLine())
	fmt.Fprintf(w, "  status: %s\n", r.statusLine())
	for _, warning := range r.Warnings {
		location := warning.Path
		if location != "" {
			location += ": "
		}
		fmt.Fprintf(w, "  warning: %s%s (%s)\n", moirai.ScrubTerminal(location), moirai.ScrubTerminal(warning.Message), warning.Code)
	}
}

func (r doctorRow) storeLine() string {
	switch {
	case r.Store == "":
		return "none"
	case r.ActiveOverride != "":
		return fmt.Sprintf("%s (%s is set)", moirai.ScrubTerminal(r.Store), r.ActiveOverride)
	case len(r.StoreOverrides) > 0:
		return fmt.Sprintf("%s (set %s to override)", moirai.ScrubTerminal(r.Store), strings.Join(r.StoreOverrides, " or "))
	default:
		return moirai.ScrubTerminal(r.Store)
	}
}

// statusLine derives the read and write phrases independently, so a directory
// that is writable but not listable renders as "not readable, writable".
func (r doctorRow) statusLine() string {
	read := "readable"
	switch {
	case r.Store == "":
		read = "none"
	case r.Exists == nil:
		read = "unknown"
	case !*r.Exists:
		read = "missing"
	case r.Readable == nil:
		read = "wrong type"
	case !*r.Readable:
		read = "not readable"
	}
	if r.Readable == nil || !r.Capabilities.Save || fileBackedFormats[r.Format] {
		return read
	}
	write := "write access not checked"
	if r.Writable != nil {
		write = "writable"
		if !*r.Writable {
			write = "not writable"
		}
	}
	return read + ", " + write
}

// storeOverrides lists the environment variables that relocate a store root,
// in precedence order, mirroring DefaultStores.
func storeOverrides(format moirai.Format) []string {
	switch format {
	case moirai.FormatChat:
		return []string{"MOIRAI_CHAT_DIR"}
	case moirai.FormatConcord:
		return []string{"CONCORD_CONVERSATIONS_FILE"}
	case moirai.FormatClaudeCode:
		return []string{"CLAUDE_CONFIG_DIR"}
	case moirai.FormatCodex:
		return []string{"CODEX_HOME"}
	case moirai.FormatPi:
		return []string{"PI_CODING_AGENT_SESSION_DIR", "PI_CODING_AGENT_DIR"}
	case moirai.FormatCampfire:
		return []string{"CAMPFIRE_CODING_AGENT_SESSION_DIR", "CAMPFIRE_CODING_AGENT_DIR"}
	case moirai.FormatAmp:
		return []string{"AMP_THREADS_DIR", "XDG_DATA_HOME"}
	case moirai.FormatGrok:
		return []string{"GROK_HOME"}
	case moirai.FormatFX:
		return []string{"FX_HOME"}
	case moirai.FormatOpenCode:
		return []string{"OPENCODE_DB", "XDG_DATA_HOME"}
	case moirai.FormatHermes:
		return []string{"HERMES_HOME"}
	case moirai.FormatCursorDesktop:
		return []string{"CURSOR_DESKTOP_USER_DIR"}
	case moirai.FormatCowork:
		if runtime.GOOS == "windows" {
			return []string{"COWORK_SESSIONS_DIR", "APPDATA"}
		}
		return []string{"COWORK_SESSIONS_DIR"}
	default:
		return nil
	}
}

// activeOverride returns the first variable with a non-empty value, which is
// the one DefaultStores honors.
func activeOverride(names []string) string {
	for _, name := range names {
		if os.Getenv(name) != "" {
			return name
		}
	}
	return ""
}

func describeMode(mode fs.FileMode) string {
	switch {
	case mode.IsRegular():
		return "a regular file"
	case mode.IsDir():
		return "a directory"
	case mode&fs.ModeNamedPipe != 0:
		return "a named pipe"
	case mode&fs.ModeSocket != 0:
		return "a socket"
	case mode&fs.ModeDevice != 0:
		return "a device"
	default:
		return "an unsupported file type"
	}
}

// underlyingError drops the operation and path from a *fs.PathError because
// the warning already names the root.
func underlyingError(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func boolPtr(value bool) *bool { return &value }
