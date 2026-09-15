package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	moirai "github.com/october-dev/moirai"
)

// isolatedDoctorEnv points every store at an empty home, clears every
// override variable, and replaces PATH with an empty directory. It returns the
// home and bin directories.
func isolatedDoctorEnv(t *testing.T) (home, bin string) {
	t.Helper()
	home = t.TempDir()
	bin = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range append(allOverrideNames(), "APPDATA") {
		t.Setenv(name, "")
	}
	t.Setenv("PATH", bin)
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	}
	return home, bin
}

func allOverrideNames() []string {
	var names []string
	for _, format := range moirai.Formats {
		for _, name := range storeOverrides(format) {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

func fakeExecutable(t *testing.T, bin, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

type doctorResult struct {
	rows  map[moirai.Format]doctorRow
	raw   map[moirai.Format]map[string]json.RawMessage
	human string
}

func doctorJSON() ([]doctorRow, []map[string]json.RawMessage, error) {
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"doctor", "--json"}); err != nil {
		return nil, nil, err
	}
	var rows []doctorRow
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		return nil, nil, err
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, nil, err
	}
	return rows, raw, nil
}

func runDoctor(t *testing.T) doctorResult {
	t.Helper()
	rows, raw, err := doctorJSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(moirai.Formats) {
		t.Fatalf("doctor reported %d rows, want %d", len(rows), len(moirai.Formats))
	}
	result := doctorResult{rows: map[moirai.Format]doctorRow{}, raw: map[moirai.Format]map[string]json.RawMessage{}}
	for index, row := range rows {
		if row.Format != moirai.Formats[index] {
			t.Fatalf("row %d: format %s, want %s", index, row.Format, moirai.Formats[index])
		}
		result.rows[row.Format] = row
		result.raw[row.Format] = raw[index]
	}
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	result.human = stdout.String()
	previous := -1
	for _, row := range rows {
		index := strings.Index(result.human, string(row.Format)+"  "+row.DisplayName+"  ")
		if index <= previous {
			t.Fatalf("human output is missing %s or out of registry order:\n%s", row.Format, result.human)
		}
		previous = index
	}
	return result
}

func (r doctorResult) row(t *testing.T, format moirai.Format) doctorRow {
	t.Helper()
	row, ok := r.rows[format]
	if !ok {
		t.Fatalf("no doctor row for %s", format)
	}
	return row
}

func (r doctorResult) hasKey(format moirai.Format, key string) bool {
	_, ok := r.raw[format][key]
	return ok
}

func isTrue(value *bool) bool  { return value != nil && *value }
func isFalse(value *bool) bool { return value != nil && !*value }

func warningCodes(row doctorRow) []string {
	codes := []string{}
	for _, warning := range row.Warnings {
		codes = append(codes, warning.Code)
	}
	return codes
}

func findWarning(row doctorRow, code string) (moirai.Warning, bool) {
	for _, warning := range row.Warnings {
		if warning.Code == code {
			return warning, true
		}
	}
	return moirai.Warning{}, false
}

func TestDoctorReportsStatuses(t *testing.T) {
	home, bin := isolatedDoctorEnv(t)
	fakeExecutable(t, bin, "claude")
	config := filepath.Join(home, "claude-config")
	projects := filepath.Join(config, "projects")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	codexHome := filepath.Join(home, "absent")
	t.Setenv("CODEX_HOME", codexHome)

	result := runDoctor(t)
	claude := result.row(t, moirai.FormatClaudeCode)
	if claude.DisplayName != "Claude Code" || claude.Executable != "claude" || !isTrue(claude.Installed) {
		t.Fatalf("claude executable: %+v", claude)
	}
	if claude.Store != projects || claude.ActiveOverride != "CLAUDE_CONFIG_DIR" || !slices.Equal(claude.StoreOverrides, []string{"CLAUDE_CONFIG_DIR"}) {
		t.Fatalf("claude store: %+v", claude)
	}
	if !isTrue(claude.Exists) || !isTrue(claude.Readable) || len(claude.Warnings) != 0 {
		t.Fatalf("claude status: %+v", claude)
	}
	if runtime.GOOS == "windows" {
		if result.hasKey(moirai.FormatClaudeCode, "writable") || !strings.Contains(result.human, "status: readable, write access not checked\n") {
			t.Fatalf("Windows write access must be not checked: %+v\n%s", claude, result.human)
		}
	}
	codex := result.row(t, moirai.FormatCodex)
	codexRoot := filepath.Join(codexHome, "sessions")
	if codex.Executable != "codex" || !isFalse(codex.Installed) || codex.Store != codexRoot || !isFalse(codex.Exists) {
		t.Fatalf("codex row: %+v", codex)
	}
	if result.hasKey(moirai.FormatCodex, "readable") || result.hasKey(moirai.FormatCodex, "writable") {
		t.Fatalf("missing codex store must not report readable/writable: %v", result.raw[moirai.FormatCodex])
	}
	if !slices.Equal(warningCodes(codex), []string{"executable_missing", "store_missing"}) {
		t.Fatalf("codex warnings: %+v", codex.Warnings)
	}
	missing, _ := findWarning(codex, "store_missing")
	if missing.Path != codexRoot || !strings.Contains(missing.Message, "CODEX_HOME") {
		t.Fatalf("store_missing warning: %+v", missing)
	}

	for _, want := range []string{
		"claude_code  Claude Code  read,write,discover,continue\n  executable: claude (found)\n  store: " + projects + " (CLAUDE_CONFIG_DIR is set)\n  status: readable",
		"codex  Codex  read,write,discover,continue\n  executable: codex (not on PATH)\n  store: " + codexRoot + " (CODEX_HOME is set)\n  status: missing\n",
		"  store: " + filepath.Join(home, ".fx", "sessions") + " (set FX_HOME to override)\n  status: missing\n",
		"  warning: codex is not on PATH; install Codex or add it to PATH (executable_missing)\n",
		"  warning: " + codexRoot + ": store root does not exist; run Codex once, or set CODEX_HOME if its data lives elsewhere (store_missing)\n",
		"simple  Simple  read,write\n  store: none\n  status: none\n",
	} {
		if !strings.Contains(result.human, want) {
			t.Errorf("human output lacks %q:\n%s", want, result.human)
		}
	}
}

func TestDoctorOmitsInapplicableKeys(t *testing.T) {
	home, _ := isolatedDoctorEnv(t)
	ampRoot := filepath.Join(home, "amp-threads")
	if err := os.Mkdir(ampRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AMP_THREADS_DIR", ampRoot)
	openCodeDB := filepath.Join(home, "opencode.db")
	hermesHome := filepath.Join(home, "hermes")
	if err := os.MkdirAll(hermesHome, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{openCodeDB, filepath.Join(hermesHome, "state.db")} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPENCODE_DB", openCodeDB)
	t.Setenv("HERMES_HOME", hermesHome)

	result := runDoctor(t)
	for _, format := range []moirai.Format{moirai.FormatSimple, moirai.FormatClaudeChat, moirai.FormatChatGPT} {
		for _, key := range []string{"executable", "installed", "store", "store_overrides", "active_override", "exists", "readable", "writable", "warnings"} {
			if result.hasKey(format, key) {
				t.Errorf("%s must not report %s: %s", format, key, result.raw[format][key])
			}
		}
	}
	for _, format := range []moirai.Format{moirai.FormatOpenCode, moirai.FormatHermes, moirai.FormatAmp} {
		row := result.row(t, format)
		wantCodes := []string{}
		if format == moirai.FormatOpenCode {
			wantCodes = append(wantCodes, "executable_missing")
		}
		if !isTrue(row.Exists) || !isTrue(row.Readable) || !slices.Equal(warningCodes(row), wantCodes) {
			t.Errorf("%s row: %+v", format, row)
		}
		if result.hasKey(format, "writable") {
			t.Errorf("%s is file-backed or source-only and must not report writable: %s", format, result.raw[format]["writable"])
		}
	}
	if !strings.Contains(result.human, "opencode  OpenCode  read,write,discover,continue\n  executable: opencode (not on PATH)\n  store: "+openCodeDB+" (OPENCODE_DB is set)\n  status: readable\n") {
		t.Errorf("opencode block:\n%s", result.human)
	}
}

func TestDoctorIsReadOnly(t *testing.T) {
	home, _ := isolatedDoctorEnv(t)
	config := filepath.Join(home, "claude-config")
	project := filepath.Join(config, "projects", "-tmp-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)

	before := snapshotTree(t, home)
	result := runDoctor(t)
	if after := snapshotTree(t, home); !slices.Equal(before, after) {
		t.Fatalf("doctor changed the home tree:\nbefore %+v\nafter  %+v", before, after)
	}
	missing := 0
	for _, row := range result.rows {
		if !isFalse(row.Exists) {
			continue
		}
		missing++
		if _, err := os.Lstat(row.Store); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: missing root %s was created (err %v)", row.Format, row.Store, err)
		}
	}
	if missing == 0 {
		t.Fatal("expected at least one missing store root")
	}
}

// treeEntry records what a read-only command must leave untouched: the set of
// paths, their modes, and the contents of regular files. Modification times
// are excluded on purpose: NTFS propagates directory timestamps lazily, so a
// walk immediately after creating fixtures can report different directory
// times from a walk a moment later without anything having been written.
type treeEntry struct {
	Path    string
	Mode    fs.FileMode
	Size    int64
	Content [sha256.Size]byte
}

func snapshotTree(t *testing.T, root string) []treeEntry {
	t.Helper()
	var entries []treeEntry
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		e := treeEntry{Path: rel, Mode: info.Mode(), Size: info.Size()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			e.Content = sha256.Sum256(data)
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestDoctorReportsWrongType(t *testing.T) {
	home, _ := isolatedDoctorEnv(t)
	config := filepath.Join(home, "claude-config")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "projects"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	dbDir := filepath.Join(home, "opencode.db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_DB", dbDir)

	result := runDoctor(t)
	for format, fragment := range map[moirai.Format]string{moirai.FormatClaudeCode: "a regular file but a directory is expected", moirai.FormatOpenCode: "a directory but a database file is expected"} {
		row := result.row(t, format)
		wrong, ok := findWarning(row, "store_wrong_type")
		if !isTrue(row.Exists) || !ok || wrong.Path != row.Store || !strings.Contains(wrong.Message, fragment) {
			t.Errorf("%s row: %+v", format, row)
		}
		if result.hasKey(format, "readable") || result.hasKey(format, "writable") {
			t.Errorf("%s must not be opened when its type is wrong: %v", format, result.raw[format])
		}
	}
	if strings.Count(result.human, "  status: wrong type\n") != 2 {
		t.Errorf("human output:\n%s", result.human)
	}
}

func TestDoctorStoreOverrides(t *testing.T) {
	type override struct {
		format   moirai.Format
		variable string
		suffix   string
	}
	cases := []override{
		{moirai.FormatClaudeCode, "CLAUDE_CONFIG_DIR", "projects"},
		{moirai.FormatCodex, "CODEX_HOME", "sessions"},
		{moirai.FormatPi, "PI_CODING_AGENT_SESSION_DIR", ""},
		{moirai.FormatPi, "PI_CODING_AGENT_DIR", "sessions"},
		{moirai.FormatCampfire, "CAMPFIRE_CODING_AGENT_SESSION_DIR", ""},
		{moirai.FormatCampfire, "CAMPFIRE_CODING_AGENT_DIR", "sessions"},
		{moirai.FormatAmp, "AMP_THREADS_DIR", ""},
		{moirai.FormatAmp, "XDG_DATA_HOME", filepath.Join("amp", "threads")},
		{moirai.FormatGrok, "GROK_HOME", "sessions"},
		{moirai.FormatFX, "FX_HOME", "sessions"},
		{moirai.FormatOpenCode, "OPENCODE_DB", ""},
		{moirai.FormatOpenCode, "XDG_DATA_HOME", filepath.Join("opencode", "opencode.db")},
		{moirai.FormatHermes, "HERMES_HOME", "state.db"},
		{moirai.FormatCursorDesktop, "CURSOR_DESKTOP_USER_DIR", ""},
		{moirai.FormatCowork, "COWORK_SESSIONS_DIR", ""},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, override{moirai.FormatCowork, "APPDATA", filepath.Join("Claude", "local-agent-mode-sessions")})
	}
	for _, c := range cases {
		t.Run(string(c.format)+"/"+c.variable, func(t *testing.T) {
			isolatedDoctorEnv(t)
			value := filepath.Join(t.TempDir(), "override")
			if c.variable == "OPENCODE_DB" {
				value += ".db"
			}
			t.Setenv(c.variable, value)
			row := runDoctor(t).row(t, c.format)
			if want := filepath.Join(value, c.suffix); row.Store != want {
				t.Fatalf("store %q, want %q", row.Store, want)
			}
			if row.ActiveOverride != c.variable || !slices.Contains(row.StoreOverrides, c.variable) {
				t.Fatalf("override reporting: active %q candidates %v", row.ActiveOverride, row.StoreOverrides)
			}
		})
	}
}

func TestDoctorOverridePrecedence(t *testing.T) {
	type precedence struct {
		format        moirai.Format
		first, second string
		fallback      string
	}
	cases := []precedence{
		{moirai.FormatPi, "PI_CODING_AGENT_SESSION_DIR", "PI_CODING_AGENT_DIR", "sessions"},
		{moirai.FormatCampfire, "CAMPFIRE_CODING_AGENT_SESSION_DIR", "CAMPFIRE_CODING_AGENT_DIR", "sessions"},
		{moirai.FormatAmp, "AMP_THREADS_DIR", "XDG_DATA_HOME", filepath.Join("amp", "threads")},
		{moirai.FormatOpenCode, "OPENCODE_DB", "XDG_DATA_HOME", filepath.Join("opencode", "opencode.db")},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, precedence{moirai.FormatCowork, "COWORK_SESSIONS_DIR", "APPDATA", filepath.Join("Claude", "local-agent-mode-sessions")})
	}
	for _, c := range cases {
		t.Run(string(c.format), func(t *testing.T) {
			isolatedDoctorEnv(t)
			defaults := runDoctor(t)
			if defaults.hasKey(c.format, "active_override") {
				t.Fatal("empty overrides must not report an active_override")
			}
			winner := filepath.Join(t.TempDir(), "winner")
			if c.first == "OPENCODE_DB" {
				winner += ".db"
			}
			t.Setenv(c.first, winner)
			second := filepath.Join(t.TempDir(), "shadowed-override-value")
			t.Setenv(c.second, second)
			result := runDoctor(t)
			row := result.row(t, c.format)
			if row.Store != winner || row.ActiveOverride != c.first || !slices.Equal(row.StoreOverrides, []string{c.first, c.second}) {
				t.Fatalf("row: %+v", row)
			}
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "shadowed-override-value") {
				t.Fatalf("row prints the shadowed override value: %s", encoded)
			}
			t.Setenv(c.first, "")
			row = runDoctor(t).row(t, c.format)
			if row.Store != filepath.Join(second, c.fallback) || row.ActiveOverride != c.second {
				t.Fatalf("empty first override must fall back to second: %+v", row)
			}
			t.Setenv(c.second, "")
			result = runDoctor(t)
			if row = result.row(t, c.format); row.Store != defaults.rows[c.format].Store || result.hasKey(c.format, "active_override") {
				t.Fatalf("empty overrides must fall back to the default root: %+v", row)
			}
		})
	}
}

func TestDoctorOverridesCoverEveryStore(t *testing.T) {
	isolatedDoctorEnv(t)
	registry, err := moirai.DefaultStores()
	if err != nil {
		t.Fatal(err)
	}
	noOverride := map[moirai.Format]bool{moirai.FormatAntigravity: true, moirai.FormatCursor: true}
	for _, format := range moirai.Formats {
		if _, err := registry.Store(format); err != nil {
			if len(storeOverrides(format)) != 0 {
				t.Errorf("%s has no store but lists overrides", format)
			}
			continue
		}
		if listed := len(storeOverrides(format)) > 0; listed == noOverride[format] {
			t.Errorf("%s: overrides listed=%t, expected no-override=%t", format, listed, noOverride[format])
		}
	}
}

func TestDoctorScrubsHumanOutput(t *testing.T) {
	home, _ := isolatedDoctorEnv(t)
	hostile := filepath.Join(home, "abs\x1b[2Jent\a")
	t.Setenv("CODEX_HOME", hostile)
	result := runDoctor(t)
	if codex := result.row(t, moirai.FormatCodex); codex.Store != filepath.Join(hostile, "sessions") {
		t.Fatalf("JSON must keep the raw path, got %q", codex.Store)
	}
	if strings.ContainsAny(result.human, "\x1b\a") {
		t.Fatalf("human output leaks control characters:\n%q", result.human)
	}
	if !strings.Contains(result.human, "(CODEX_HOME is set)") {
		t.Fatalf("human output must name CODEX_HOME:\n%s", result.human)
	}
}

func TestDoctorRejectsArguments(t *testing.T) {
	isolatedDoctorEnv(t)
	for _, args := range [][]string{{"doctor", "extra"}, {"doctor", "--bogus"}} {
		var stdout, stderr bytes.Buffer
		a := app{out: &stdout, err: &stderr}
		if err := a.run(context.Background(), args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestDoctorExecutables(t *testing.T) {
	for _, installed := range []bool{false, true} {
		name := "empty PATH"
		if installed {
			name = "fake executables"
		}
		t.Run(name, func(t *testing.T) {
			_, bin := isolatedDoctorEnv(t)
			if installed {
				for _, info := range moirai.DefaultRegistry.Harnesses() {
					if command, err := moirai.CommandFor(info.Format, moirai.SessionRef{}); err == nil {
						fakeExecutable(t, bin, command.Program)
					}
				}
				// A binary alone cannot make a source-only harness launchable.
				fakeExecutable(t, bin, "amp")
				fakeExecutable(t, bin, "hermes")
			}
			result := runDoctor(t)
			for _, info := range moirai.DefaultRegistry.Harnesses() {
				row := result.row(t, info.Format)
				if row.DisplayName != info.DisplayName || row.Capabilities != info.Capability {
					t.Errorf("%s metadata differs from registry: %+v", info.Format, row)
				}
				command, err := moirai.CommandFor(info.Format, moirai.SessionRef{})
				if err != nil {
					if result.hasKey(info.Format, "executable") || result.hasKey(info.Format, "installed") {
						t.Errorf("%s has no launch command but reports executable status: %+v", info.Format, row)
					}
					if _, ok := findWarning(row, "executable_missing"); ok {
						t.Errorf("%s has no launch command but warns about an executable", info.Format)
					}
					continue
				}
				if row.Executable != command.Program || row.Installed == nil || *row.Installed != installed {
					t.Errorf("%s executable status: %+v", info.Format, row)
				}
				if _, ok := findWarning(row, "executable_missing"); ok == installed {
					t.Errorf("%s executable warning: %+v", info.Format, row.Warnings)
				}
				if info.Format == moirai.FormatCowork || info.Format == moirai.FormatCursorDesktop {
					if !strings.Contains(result.human, "  launcher: "+command.Program+" (") {
						t.Errorf("%s must label the program as a launcher:\n%s", info.Format, result.human)
					}
				}
			}
		})
	}
}

func TestDoctorFollowsStoreSymlinks(t *testing.T) {
	for _, file := range []bool{false, true} {
		name := "directory"
		if file {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			home, bin := isolatedDoctorEnv(t)
			target := filepath.Join(home, "target")
			link := filepath.Join(home, "store")
			format, variable := moirai.FormatPi, "PI_CODING_AGENT_SESSION_DIR"
			if file {
				format, variable = moirai.FormatOpenCode, "OPENCODE_DB"
				if err := os.WriteFile(target, []byte("not a database; doctor must not parse it"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("symlinks unavailable: %v", err)
				}
				t.Fatal(err)
			}
			command, err := moirai.CommandFor(format, moirai.SessionRef{})
			if err != nil {
				t.Fatal(err)
			}
			fakeExecutable(t, bin, command.Program)
			t.Setenv(variable, link)
			result := runDoctor(t)
			row := result.row(t, format)
			if row.Store != link || !isTrue(row.Exists) || !isTrue(row.Readable) || len(row.Warnings) != 0 {
				t.Fatalf("symlink to %s: %+v", name, row)
			}
			if file && result.hasKey(format, "writable") {
				t.Fatal("file-backed symlink must not report writable")
			}
		})
	}
}

func TestFormatsHumanOutputUnchanged(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := app{out: &stdout, err: &stderr}
	if err := a.run(context.Background(), []string{"formats"}); err != nil {
		t.Fatal(err)
	}
	// Captured from main before extracting capabilityNames.
	want := `simple             Simple                 read,write
claude_code        Claude Code            read,write,discover,continue
codex              Codex                  read,write,discover,continue
pi                 pi                     read,write,discover,continue
amp                Amp                    read,write,discover,source-only
opencode           OpenCode               read,write,discover,continue
cursor             Cursor Agent           read,write,discover,continue
cursor_desktop     Cursor                 read,write,discover
grok               Grok CLI               read,write,discover,continue
hermes             Hermes Agent           read,write,discover,source-only
antigravity        Antigravity CLI        read,write,discover,continue
campfire           Campfire               read,write,discover,continue
cowork             Claude Cowork          read,write,discover
fx                 fx                     read,write,discover,continue
claude_chat        Claude Chat            read,source-only
chatgpt            ChatGPT                read,source-only
chat               Plain chat             read,write,discover
`
	if runtime.GOOS == "darwin" {
		want += "concord            Concord                read,write,discover,continue\n"
	} else {
		want += "concord            Concord                read,write,discover\n"
	}
	if stdout.String() != want {
		t.Fatalf("formats output changed:\ngot  %q\nwant %q", stdout.String(), want)
	}
}
