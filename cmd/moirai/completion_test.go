package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	moirai "github.com/october-dev/moirai"
)

// Inspect declarations in their immediate lexical scope. In particular, the
// two archive cases each own a different fs, while importSession owns one fs
// that serves two commands. Unknown constructions fail rather than disappearing.
func TestCompletionDrift(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	commands := map[string]bool{}
	actual := map[string]map[string]bool{}
	handled := map[*ast.CallExpr]bool{}
	literal := func(expr ast.Expr) string {
		t.Helper()
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Fatalf("expected string literal, got %T", expr)
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if fn.Name.Name == "run" || fn.Name.Name == "archive" {
			for _, stmt := range fn.Body.List {
				sw, ok := stmt.(*ast.SwitchStmt)
				if !ok {
					continue
				}
				for _, stmt := range sw.Body.List {
					clause := stmt.(*ast.CaseClause)
					for _, expr := range clause.List {
						name := literal(expr)
						switch name {
						case "-h", "--help":
							name = "help"
						case "--version":
							name = "version"
						}
						if fn.Name.Name == "archive" {
							name = "archive " + name
						}
						commands[name] = true
					}
				}
			}
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			var statements []ast.Stmt
			switch n := node.(type) {
			case *ast.BlockStmt:
				statements = n.List
			case *ast.CaseClause:
				statements = n.Body
			default:
				return true
			}
			for _, stmt := range statements {
				assign, ok := stmt.(*ast.AssignStmt)
				if !ok {
					continue
				}
				for _, rhs := range assign.Rhs {
					call, ok := rhs.(*ast.CallExpr)
					if !ok {
						continue
					}
					id, ok := call.Fun.(*ast.Ident)
					if !ok || id.Name != "newFlags" {
						continue
					}
					handled[call] = true
					if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || len(call.Args) != 2 {
						t.Fatal("unrecognized newFlags assignment")
					}
					variable, ok := assign.Lhs[0].(*ast.Ident)
					if !ok {
						t.Fatal("unrecognized flag set binding")
					}
					var names []string
					switch arg := call.Args[0].(type) {
					case *ast.BasicLit:
						names = []string{literal(arg)}
					case *ast.Ident:
						if fn.Name.Name != "importSession" || arg.Name != "name" {
							t.Fatalf("unrecognized dynamic newFlags in %s", fn.Name.Name)
						}
						names = []string{"import", "continue"}
					default:
						t.Fatalf("unrecognized newFlags argument %T", arg)
					}
					flags := map[string]bool{}
					for _, sibling := range statements {
						ast.Inspect(sibling, func(n ast.Node) bool {
							// Never cross into a nested lexical scope.
							switch n.(type) {
							case *ast.BlockStmt, *ast.CaseClause, *ast.FuncLit:
								return false
							}
							invocation, ok := n.(*ast.CallExpr)
							if !ok {
								return true
							}
							sel, ok := invocation.Fun.(*ast.SelectorExpr)
							if !ok {
								return true
							}
							receiver, ok := sel.X.(*ast.Ident)
							if !ok || receiver.Name != variable.Name {
								return true
							}
							switch sel.Sel.Name {
							case "Bool", "String", "Int", "Int64":
								if len(invocation.Args) == 0 {
									t.Fatal("flag declaration without name")
								}
								name := literal(invocation.Args[0])
								if _, exists := flags[name]; exists {
									t.Fatalf("duplicate flag %s", name)
								}
								flags[name] = sel.Sel.Name == "Bool"
							default:
								// Reading or parsing a FlagSet is fine; new registration methods
								// must be explicitly taught to this test.
								switch sel.Sel.Name {
								case "NArg", "Arg", "Args", "Parse", "SetOutput", "Lookup":
								default:
									t.Fatalf("unrecognized fs method %s", sel.Sel.Name)
								}
							}
							return true
						})
					}
					for _, name := range names {
						if _, exists := actual[name]; exists {
							t.Fatalf("duplicate flag set %s", name)
						}
						actual[name] = flags
					}
				}
			}
			return true
		})
	}
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if ok && id.Name == "newFlags" && !handled[call] {
			t.Fatal("newFlags construction not accounted for")
		}
		return true
	})
	expectedCommands := map[string]bool{}
	eachCommand(func(name string, c commandSpec) {
		if expectedCommands[name] {
			t.Fatalf("duplicate spec %s", name)
		}
		expectedCommands[name] = true
		expected := map[string]bool{}
		for _, f := range c.flags {
			if _, exists := expected[f.name]; exists {
				t.Fatalf("duplicate spec flag %s %s", name, f.name)
			}
			switch f.valueType {
			case boolValue, formatValue, fileValue, opaqueValue:
			default:
				t.Fatalf("unknown flag type %q", f.valueType)
			}
			expected[f.name] = f.valueType == boolValue
		}
		got := actual[name]
		if got == nil {
			got = map[string]bool{}
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("%s flag arity drift: CLI %v, completion %v", name, got, expected)
		}
	})
	if !reflect.DeepEqual(commands, expectedCommands) {
		t.Errorf("command drift: CLI %v, completion %v", commands, expectedCommands)
	}
	for name := range actual {
		if !expectedCommands[name] {
			t.Errorf("flag set %q missing from spec", name)
		}
	}
	completion, err := parser.ParseFile(token.NewFileSet(), "completion.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	shells := []string{}
	for _, decl := range completion.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "completion" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if ok {
				for _, expr := range clause.List {
					shells = append(shells, literal(expr))
				}
			}
			return true
		})
	}
	slices.Sort(shells)
	expectedShells := slices.Clone(completionShells)
	slices.Sort(expectedShells)
	if !slices.Equal(shells, expectedShells) {
		t.Fatalf("shell dispatch drift: %v != %v", shells, expectedShells)
	}
}

func completionScript(t *testing.T, shell string) string {
	t.Helper()
	var out bytes.Buffer
	if err := (app{out: &out, err: io.Discard}).run(context.Background(), []string{"completion", shell}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

type completionFailWriter struct{ err error }

func (w completionFailWriter) Write([]byte) (int, error) { return 0, w.err }
func TestCompletionHandler(t *testing.T) {
	for _, args := range [][]string{{"completion"}, {"completion", "nope"}, {"completion", "bash", "extra"}} {
		var out bytes.Buffer
		if err := (app{out: &out, err: io.Discard}).run(context.Background(), args); err == nil || out.Len() != 0 {
			t.Fatalf("args %v: error %v, stdout %q", args, err, out.String())
		}
	}
	for _, shell := range completionShells {
		t.Run(shell, func(t *testing.T) {
			first := completionScript(t, shell)
			if first != completionScript(t, shell) {
				t.Fatal("nondeterministic output")
			}
			for _, format := range moirai.Formats {
				if !strings.Contains(first, string(format)) {
					t.Errorf("missing format %s", format)
				}
			}
			failure := errors.New("write failed")
			if err := (app{out: completionFailWriter{failure}, err: io.Discard}).run(context.Background(), []string{"completion", shell}); !errors.Is(err, failure) {
				t.Fatalf("write error not propagated: %v", err)
			}
		})
	}
}

func completionSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"a file.json", "-input.json", "create", "archive"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fake := "#!/bin/sh\nprintf invoked >> \"$MOIRAI_COMPLETION_LOG\"\nexit 97\n"
	if err := os.WriteFile(filepath.Join(dir, "moirai"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "invocations")
	t.Setenv("MOIRAI_COMPLETION_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if data, err := os.ReadFile(log); err == nil {
			t.Errorf("completion invoked moirai: %s", data)
		} else if !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	return dir
}
func assertCandidates(t *testing.T, got, want, absent []string, empty bool) {
	t.Helper()
	for _, v := range want {
		if !slices.Contains(got, v) {
			t.Errorf("missing %q in %q", v, got)
		}
	}
	for _, v := range absent {
		if slices.Contains(got, v) {
			t.Errorf("unexpected %q in %q", v, got)
		}
	}
	if empty && len(got) > 0 {
		t.Errorf("expected no candidates, got %q", got)
	}
}
func TestBashCompletion(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	dir := completionSandbox(t)
	script := completionScript(t, "bash")
	formats := []string{}
	for _, f := range moirai.Formats {
		formats = append(formats, string(f))
	}
	tests := []struct {
		name                string
		words, want, absent []string
		empty               bool
	}{
		{name: "top", words: []string{"moirai", "co"}, want: []string{"convert", "continue", "completion"}},
		{name: "archive", words: []string{"moirai", "archive", ""}, want: []string{"create", "verify"}},
		{name: "create flags", words: []string{"moirai", "archive", "create", "--"}, want: []string{"--from", "--out"}},
		{name: "verify flags", words: []string{"moirai", "archive", "verify", "--"}, want: []string{"--max-input-bytes"}, absent: []string{"--from", "--out"}},
		{name: "format", words: []string{"moirai", "convert", "--from", ""}, want: formats},
		{name: "equals", words: []string{"moirai", "convert", "--from=co"}, want: []string{"--from=codex", "--from=cowork"}},
		{name: "split equals", words: []string{"moirai", "convert", "--from", "=", "co"}, want: []string{"codex", "cowork"}},
		{name: "split equals empty", words: []string{"moirai", "convert", "--from", "="}, want: formats},
		{name: "single dash", words: []string{"moirai", "convert", "-from", "co"}, want: []string{"codex"}},
		{name: "single dash equals", words: []string{"moirai", "convert", "-from=co"}, want: []string{"-from=codex"}},
		{name: "positional then flag", words: []string{"moirai", "inspect", "a file.json", "--"}, want: []string{"--from", "--json"}},
		{name: "search collision", words: []string{"moirai", "search", "archive", "--"}, want: []string{"--format", "--limit"}, absent: []string{"--from", "--out"}},
		{name: "convert collision", words: []string{"moirai", "convert", "create", "--"}, want: []string{"--from", "--to"}},
		{name: "value collision", words: []string{"moirai", "search", "--format", "archive", "--"}, want: []string{"--limit"}, absent: []string{"--out"}},
		{name: "bool consumes nothing", words: []string{"moirai", "show", "--json", "--"}, want: []string{"--format"}},
		{name: "opaque value", words: []string{"moirai", "search", "--limit", ""}, empty: true},
		{name: "out file", words: []string{"moirai", "export", "--out", "a"}, want: []string{"a file.json"}},
		{name: "space file", words: []string{"moirai", "inspect", "a "}, want: []string{"a file.json"}},
		{name: "after terminator file", words: []string{"moirai", "convert", "--", "-"}, want: []string{"-input.json"}, absent: []string{"--from", "--out"}},
		{name: "after terminator format", words: []string{"moirai", "convert", "--", "--from", "co"}, empty: true},
		{name: "after terminator id", words: []string{"moirai", "show", "--", ""}, empty: true},
		{name: "after terminator query", words: []string{"moirai", "search", "--", ""}, empty: true},
		{name: "continue file", words: []string{"moirai", "continue", "--", "a "}, want: []string{"a file.json"}},
		{name: "shell enum", words: []string{"moirai", "completion", ""}, want: completionShells},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quoted := make([]string, len(tt.words))
			for i, w := range tt.words {
				quoted[i] = shellQuote(w)
			}
			fixture := script + "\nCOMP_WORDS=(" + strings.Join(quoted, " ") + ")\nCOMP_CWORD=" + strconv.Itoa(len(tt.words)-1) + "\nCOMP_LINE=" + shellQuote(strings.Join(tt.words, " ")) + "\nCOMP_POINT=${#COMP_LINE}\n_moirai\nif (( ${#COMPREPLY[@]} )); then printf '%s\\0' \"${COMPREPLY[@]}\"; fi\n"
			cmd := exec.Command(bash, "--noprofile", "--norc")
			cmd.Stdin = strings.NewReader(fixture)
			cmd.Dir = dir
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			data, err := cmd.Output()
			if err != nil {
				t.Fatalf("bash: %v: %s", err, stderr.String())
			}
			var got []string
			if len(data) > 0 {
				got = strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			}
			assertCandidates(t, got, tt.want, tt.absent, tt.empty)
		})
	}
}

func TestNativeCompletion(t *testing.T) {
	for _, shell := range []string{"zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			executable, err := exec.LookPath(shell)
			if err != nil {
				t.Skip(shell + " unavailable")
			}
			dir := completionSandbox(t)
			path := filepath.Join(dir, "completion."+shell)
			if shell == "zsh" {
				path = filepath.Join(dir, "_moirai")
			}
			if err := os.WriteFile(path, []byte(completionScript(t, shell)), 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(executable, "-n", path).CombinedOutput(); err != nil {
				t.Fatalf("syntax: %v: %s", err, out)
			}
			tests := []struct {
				line         string
				want, absent []string
				empty        bool
			}{
				{line: "moirai co", want: []string{"convert", "continue", "completion"}},
				{line: "moirai archive ", want: []string{"create", "verify"}},
				{line: "moirai archive create --", want: []string{"--from", "--out"}},
				{line: "moirai archive verify --", want: []string{"--max-input-bytes"}, absent: []string{"--from", "--out"}},
				{line: "moirai convert --from=co", want: []string{"codex", "cowork"}},
				{line: "moirai convert --from co", want: []string{"codex", "cowork"}},
				{line: "moirai search archive --", want: []string{"--format", "--limit"}, absent: []string{"--from", "--out"}},
				{line: "moirai convert create --", want: []string{"--from", "--to", "--out"}},
				{line: "moirai inspect 'a file.json' --", want: []string{"--from", "--json"}},
				{line: "moirai convert -- -", want: []string{"-input.json"}, absent: []string{"--from", "--to"}},
				{line: "moirai show -- ", empty: true},
				{line: "moirai search -- ", empty: true},
				{line: "moirai convert -- --from co", absent: []string{"codex", "cowork", "--from=codex", "--from=cowork"}},
				{line: "moirai search --limit ", empty: true},
				{line: "moirai inspect a", want: []string{"a file.json"}},
			}
			for _, tt := range tests {
				t.Run(tt.line, func(t *testing.T) {
					var got []string
					if shell == "fish" {
						cmd := exec.Command(executable, "--no-config", "-c", "source "+shellQuote(path)+"; complete -C "+shellQuote(tt.line))
						cmd.Dir = dir
						data, err := cmd.CombinedOutput()
						if err != nil {
							t.Fatalf("fish: %v: %s", err, data)
						}
						for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
							if line == "" {
								continue
							}
							candidate, _, _ := strings.Cut(line, "\t")
							// Fish returns the whole equals word, while zsh compadd returns the value.
							if strings.Contains(tt.line, "--from=") {
								candidate = strings.TrimPrefix(candidate, "--from=")
							}
							got = append(got, candidate)
						}
					} else {
						got = zshCandidates(t, executable, dir, path, tt.line)
					}
					assertCandidates(t, got, tt.want, tt.absent, tt.empty)
				})
			}
		})
	}
}

// Exercise the real zsh completion system inside ZLE. Merely mocking
// _arguments would miss native value parsing, '--', and option placement.
func zshCandidates(t *testing.T, executable, dir, script, line string) []string {
	t.Helper()
	fixtureDir := t.TempDir()
	result := filepath.Join(fixtureDir, "result")
	done := filepath.Join(fixtureDir, "done")
	setup := filepath.Join(fixtureDir, "setup.zsh")
	fixture := `fpath=( ` + shellQuote(filepath.Dir(script)) + ` $fpath )
autoload -Uz compinit
compinit -D
compadd() {
    local -a matches
    builtin compadd -A matches "$@"
    collected+=( "${(@Q)matches}" )
    builtin compadd "$@"
}
_capture() {
    collected=()
    _main_complete
    if (( ${#collected} )); then
        print -rl -- "${collected[@]}" > ` + shellQuote(result) + `
    else
        : > ` + shellQuote(result) + `
    fi
    : > ` + shellQuote(done) + `
}
_run_capture() {
    BUFFER=` + shellQuote(line) + `
    CURSOR=${#BUFFER}
    zle _capture
}
zle -C _capture complete-word _capture
zle -N _run_capture
bindkey '^X' _run_capture
`
	if err := os.WriteFile(setup, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	driver := `zmodload zsh/zpty || exit 1
zpty worker ` + shellQuote(executable) + ` -f
zpty -w worker ` + shellQuote("source "+shellQuote(setup)) + `
zpty -w -n worker $'\x18'
for ((i=0; i<100; i++)); do
    [[ -f ` + shellQuote(done) + ` ]] && break
    sleep 0.05
done
zpty -r worker output '*' 2>/dev/null
zpty -d worker
[[ -f ` + shellQuote(done) + ` ]] || { print -r -- "$output"; exit 1; }
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-f", "-c", driver)
	cmd.Dir = dir
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zsh ZLE harness: %v: %s", err, data)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}
