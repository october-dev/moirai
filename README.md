<div align="center">

<img src="assets/moirai-wordmark-v7.svg" alt="Moirai terminal wordmark" width="760">

# Moirai

**Move an AI-agent session to another harness and keep working.**

[moirai.to](https://moirai.to) · An [October open-source project](https://october.dev/open-source).

[![CI](https://github.com/october-dev/moirai/actions/workflows/ci.yml/badge.svg)](https://github.com/october-dev/moirai/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-7C6CF0.svg)](LICENSE)
[![Schema: 1.0](https://img.shields.io/badge/schema-1.0-28B8D8.svg)](docs/FORMAT.md)

</div>

Moirai discovers local agent sessions, translates their portable context into a
canonical transcript, writes the destination's native session representation,
and can launch the destination harness. Messages, tool calls and results,
reasoning that the source explicitly stored, images, artifacts, usage, workspace
metadata, and ancestry survive when the destination can represent them.

It is a local-first Go library and CLI with a typed TypeScript SDK. The
open-source tools require no Moirai account, daemon, hosted service, or
credential collector.

## Ordinary chats, too

Moirai also handles plain `system` / `user` / `assistant` conversations. Import
OpenAI-style chat JSON or discover Concord's saved chats, preserve message order
and model information, and create portable `.moirai` archives—all locally.

```sh
moirai list --format concord
moirai convert conversation.json --from chat --to simple --out canonical.json
moirai archive create conversation.json --from chat --out conversation.moirai
moirai continue conversation.moirai --with chat
```

The `chat` destination saves local JSON without starting a process. The Concord
destination stages a new import file and opens the app's confirmation dialog on
macOS; it never rewrites the app's live database. See [chat formats and setup](docs/CHAT.md)
for the required Concord build, exact round-trip contract, and degradation warnings.

## Free and hosted versions

Use Moirai locally to switch harnesses, or use the optional Cloud service to
keep session files online. Local conversion never requires an account or upload.
The website is [moirai.to](https://moirai.to); Cloud is currently a restricted
pilot, not open registration or a paid subscription.

### Open source: free and local

The CLI, Go library, canonical schema, and TypeScript SDK in this repository
are free and open source. They discover and convert sessions on the same
machine without sending them to October. You can:

- continue a session in another supported harness;
- create portable, integrity-checked `.moirai` archives;
- move an archive to another machine using storage or transport you control;
- build integrations directly on the Go and TypeScript APIs; and
- keep working without an account, hosted dependency, or usage plan.

This local version is the foundation of Moirai, not a limited client for the
hosted product. It remains useful on its own and keeps session data under the
user's control.

### Private Cloud backups (restricted pilot)

The pilot backs up original Codex and Claude Code files, separately from
converted `.moirai` archives. Uploads are explicit, encrypted in storage, and
accessible only to their owner through the service. Original files are not
redacted and can contain secrets; storage encryption is not end-to-end encryption.

- Chunked uploads resume interrupted transfers.
- Each successful upload is downloaded and SHA-256 checked against the original.
- Restores create a new file without overwriting an existing one.
- Local sessions are never automatically deleted.

Approved pilot accounts can [open their backups](https://moirai-cloud.onrender.com/backups).
The dashboard lists files and backup status; it is not yet a conversation reader.
The pilot uses a separate `moirai-sync` companion maintained with the private
backend, not a command included in the public CLI release. It backs up files
under selected session directories, not project repositories or a whole computer.
Keep local originals until you have independently checked a restored copy.

### Cloud sharing client

This public repository contains the core library, CLI, TypeScript SDK, and local
sharing preparation. The hosted backend, landing page, dashboard, and deployment
configuration are maintained separately in a private repository. The CLI can
connect to a configured Moirai service. The current pilot runs at
`https://moirai-cloud.onrender.com`; `moirai.to` serves the public website.
Only approved accounts can sign in to the pilot. Local functionality requires
no hosted account. Shared checkpoints below are separate from original-file backups.

Prepare and inspect an archive locally before publishing:

```bash
moirai login --server https://moirai-cloud.onrender.com
moirai publish 'SESSION_ID#12-38' --from claude_code --preview-out reviewed.moirai
moirai publish reviewed.moirai --visibility private --yes
moirai invite PUBLICATION_ID --login teammate
```

The preparation step strips workspace metadata and persisted thinking, omits
local media references, and redacts recognizable token patterns. Inspect the
entire prepared archive: tool output, metadata, and inline media can still contain
sensitive material. Uploads are explicit, immutable, and private by default.
Use `--visibility unlisted` for anyone-with-the-link access or `public` for public
access. The default expiry is seven days; `--expires 0` disables expiry.

On another machine:

```bash
moirai pull https://YOUR_MOIRAI_ORIGIN/s/PUBLICATION_ID --out session.moirai
moirai continue session.moirai --with codex --dry-run
moirai continue session.moirai --with codex
moirai publish DERIVED_FILE --parent PUBLICATION_ID --yes
```

Prepare the destination repository separately. Continuing creates a fresh native
session and surfaces compatibility warnings. `moirai unpublish ID --yes` revokes
future service access; `moirai cloud-delete ID --yes` deletes the live archive.
Neither operation can recall downloaded copies or independent forks.

`moirai team create NAME` creates a shared workspace. Use `moirai whoami` to get
your stable account ID, `team invite TEAM --user ACCOUNT_ID --role writer` to add
a collaborator, and `publish FILE --team TEAM --yes` for team-owned checkpoints.
Team owners administer access; writers publish and read; readers only read.

See [installation/release instructions](docs/INSTALL.md). Configure the service
origin supplied by your operator; hosted deployment is not part of this repo.

## Install

Download prebuilt CLI archives from [GitHub Releases](https://github.com/october-dev/moirai/releases)
for Linux amd64/arm64, macOS amd64/arm64, and Windows amd64. Each release from
v0.2.0 includes `SHA256SUMS` and provenance. See [installation instructions](docs/INSTALL.md)
for verification and extraction. The npm package is the TypeScript SDK, not the
native CLI.

Go 1.26.8 or newer:

```bash
go install github.com/october-dev/moirai/cmd/moirai@latest
```

TypeScript (Node.js 20 or newer):

```bash
npm install @october-dev/moirai
```

To build the CLI from source:

```bash
go build -o moirai ./cmd/moirai
```

### Shell completion

Generate and install the script for your shell:

Bash (load it from `~/.bashrc`):

```bash
mkdir -p ~/.local/share/bash-completion/completions
moirai completion bash > ~/.local/share/bash-completion/completions/moirai
# Add this line to ~/.bashrc:
source ~/.local/share/bash-completion/completions/moirai
```

Zsh (put the completion directory on `fpath` before running `compinit` in
`~/.zshrc`):

```zsh
mkdir -p ~/.zsh/completions
moirai completion zsh > ~/.zsh/completions/_moirai
# Add these lines to ~/.zshrc (or adjust your existing compinit setup):
fpath=(~/.zsh/completions $fpath)
autoload -Uz compinit
compinit
```

Fish (automatically loads scripts from this directory):

```fish
mkdir -p ~/.config/fish/completions
moirai completion fish > ~/.config/fish/completions/moirai.fish
```

Start a new shell after installation. Regenerate the installed script after
upgrading Moirai to refresh command, flag, and format suggestions.

Generating or using completions performs no session-store discovery and never
invokes the moirai binary at completion time. File arguments use your shell's
filename completion; session IDs and search queries are not discovered.

## Continue a session

List sessions that are already on the machine:

```bash
moirai list
moirai list --format claude_code
moirai list --cwd ~/src/project --limit 5
moirai list --since 2026-09-01T00:00:00Z --until 2026-09-08T00:00:00Z --json
```

`--cwd` keeps sessions whose working directory is that path or a subdirectory
of it. `--since` and `--until` are inclusive RFC 3339 bounds on a session's
last-modified time, falling back to its start time; sessions with neither are
excluded. `--limit` keeps the first N sessions in the list's existing order.

Move one into another installed harness and launch it:

```bash
moirai continue SESSION_ID --from claude_code --with codex
```

Save the destination session without launching it:

```bash
moirai continue SESSION_ID --from codex --with cursor --no-launch
```

Continue only a message range. Ranges are one-based and reject cuts that split
a tool call from its result:

```bash
moirai continue 'SESSION_ID#12-38' --from claude_code --with pi
```

Each cross-harness handoff receives a fresh session ID and provenance pointing
to its source. The original session is not modified.

Preview an import before saving it, with conversion warnings and the resolved
destination store:

```bash
moirai import session.json --to claude_code --dry-run
moirai import session.json --to codex --dry-run --json
moirai continue 'SESSION_ID#3-' --from claude_code --with codex --dry-run --json
```

Dry-run renders and validates the conversion without saving a session or
launching a harness. JSON reports `launch: false` and a `range` with one-based,
inclusive `start` and `end` bounds for a selected message range; open-ended
ranges resolve to the last selected message. File inputs and stored sessions
without a range report `range: null`. Human output shows the resolved range,
for example `Range: messages 3-6`, or `Range: all N messages` when no range is
selected.

## Read sessions from an MCP client

`moirai mcp` exposes `formats`, `list_sessions`, `show_session`, and
`search_sessions` as read-only MCP tools over stdio. Configure your client to
start it with:

```json
{"command":"moirai","args":["mcp"]}
```

It reads the same local stores as the CLI and performs no saves, deletes,
launches, or network requests. Read-only access does not redact histories or
restrict them to the current project. The `thinking` and `tools` options affect
text rendering only; structured transcripts retain the selected content.

See [MCP setup, tools, limits, and privacy](docs/MCP.md) before connecting a client.

## Troubleshooting

`moirai doctor` reports, for every supported harness, whether its executable is
on `PATH`, where Moirai expects its session store, which environment variables
relocate that store, and whether the store root exists and can be read and
written by the current account:

```bash
moirai doctor
moirai doctor --json
```

Each harness prints one block:

```text
codex  Codex  read,write,discover,continue
  executable: codex (not on PATH)
  store: /home/me/.codex/sessions (set CODEX_HOME to override)
  status: missing
  warning: codex is not on PATH; install Codex or add it to PATH (executable_missing)
  warning: /home/me/.codex/sessions: store root does not exist; run Codex once, or set CODEX_HOME if its data lives elsewhere (store_missing)
```

The `executable` line appears only when Moirai has a launch command for the
harness. Claude Cowork and Cursor Desktop label this line `launcher`: Cowork
uses `open` on macOS, `cmd` on Windows, and `claude-desktop` on Linux; Cursor
Desktop uses `cursor`. Finding the program on `PATH` does not verify that the
desktop application is installed. Amp and Hermes have no launch command, so
their executable status is omitted.

The `store` line shows the resolved root and names the override variable that
is set, or the variables that would override it. Harnesses without a local
store (`simple`, `claude_chat`, `chatgpt`) show `store: none`.

The `status` line reports `missing`, `unknown`, `wrong type`, `readable`, or
`not readable`. Directory stores Moirai can save into also report `writable`,
`not writable`, or `write access not checked` (on non-Unix platforms, including
Windows). File-backed stores and source-only stores omit the write status.
Harnesses without a store show `status: none`.

Warnings use these codes:

| Code | Meaning | Fix |
|---|---|---|
| `executable_missing` | The launch program was not found on `PATH`. | Install it or add it to `PATH`. |
| `store_missing` | The store root does not exist. | Run the harness once, or set the named override if the data lives elsewhere. |
| `store_stat_failed` | The root could not be inspected; existence is unknown. | Check parent-directory permissions and symlink targets. |
| `store_wrong_type` | The root has the wrong kind: a file where a directory belongs, a directory where a database file belongs, or a pipe, socket, or device. | Point the store override at the correct root. |
| `store_unreadable` | Opening and closing the root failed. | Check its ownership and permissions. |
| `store_unwritable` | The directory failed the write and search permission query. | Check ownership, permissions, and whether the filesystem is read-only. |

These are diagnostic statuses, including missing optional harnesses and stores;
they do not make the command exit with an error.

`moirai doctor --json` emits an array with one object per harness holding
`format`, `display_name`, `capabilities`, `executable`, `installed`, `store`,
`store_overrides`, `active_override`, `exists`, `readable`, `writable`, and
`warnings`. `capabilities` contains the registry's boolean capability object;
`installed`, `exists`, `readable`, and `writable` are optional booleans. A key is
absent when its check is unknown or does not apply, so scripts should test for
presence rather than assume `false`. Empty strings and lists are also omitted.
For example, a missing Codex store with a launch program on `PATH` looks like:

```json
[
  {
    "format": "codex",
    "display_name": "Codex",
    "capabilities": {
      "read": true, "write": true, "discover": true, "save": true,
      "delete": true, "continue": true, "remote": false, "source_only": false
    },
    "executable": "codex",
    "installed": true,
    "store": "/home/me/.codex/sessions",
    "store_overrides": ["CODEX_HOME"],
    "exists": false,
    "warnings": [{
      "path": "/home/me/.codex/sessions",
      "code": "store_missing",
      "message": "store root does not exist; run Codex once, or set CODEX_HOME if its data lives elsewhere"
    }]
  }
]
```

The full array follows registry order. JSON `installed` means only that the
launch program can be found on `PATH`, including desktop launchers. Each
`store_overrides` list contains environment-variable names in precedence order,
including `XDG_DATA_HOME` for Amp and OpenCode and `APPDATA` for Cowork on
Windows. `active_override`, when present, names the first nonempty variable;
empty and shadowed variables do not become active. JSON preserves paths and
error text; the human report scrubs terminal control characters.

Doctor is read-only. It stats each root, following symlinks, and only opens and
closes roots whose type matches the store: a regular file for OpenCode and
Hermes, a directory for other stores. Pipes, sockets, and devices fail the type
check before opening. Doctor never lists directories, discovers or loads
sessions, reads transcripts, runs launchers, writes files, or touches the
network. It does not print environment-variable values separately; the resolved
store path may still reveal all or part of the active override value.

On Unix, writability is queried with `access(2)` using `W_OK | X_OK` and the
process's **real** user/group credentials. Readability uses `os.Open` and the
process's **effective** credentials. Writability is advisory for the invoking
account: root can bypass ordinary mode bits, but a read-only filesystem can
reject even root. The check does not cover nested destination directories,
disk space, or a harness's own import. Database-file stores are never checked
for writability; OpenCode saves go through `opencode import`, and Hermes is
source-only.

## Supported formats

`read` parses native data into the canonical model. `write` renders native
data. `local` means Moirai discovers the harness's default on-disk store.
`continue` means it can save a new native session and start the installed
harness. Source-only stores are never modified.

| Format | Read | Write | Local | Continue | Notes |
| --- | :---: | :---: | :---: | :---: | --- |
| Claude Code | yes | yes | yes | yes | JSONL projects |
| Codex | yes | yes | yes | yes | rollout JSONL |
| Pi | yes | yes | yes | yes | session JSONL |
| Campfire | yes | yes | yes | yes | session JSONL |
| OpenCode | yes | yes | yes | yes | SQLite discovery; native CLI import |
| Cursor Agent | yes | yes | yes | yes | content-addressed chat store |
| Cursor desktop | yes | yes | yes | no | import writes workspace state; opening the app is not session-specific resume |
| Grok CLI | yes | yes | yes | yes | session bundles |
| Antigravity CLI | yes | yes | yes | yes | protobuf records in SQLite |
| Claude Cowork | yes | yes | yes | no | import writes session trees; opening the app is not session-specific resume |
| fx | yes | yes | yes | yes | event bundles |
| Amp | yes | yes | read-only | no | local threads remain untouched |
| Hermes Agent | yes | yes | read-only | no | local SQLite remains untouched |
| Claude Chat export | yes | no | no | no | explicit supplied export only |
| ChatGPT export | yes | no | no | no | explicit supplied export only |
| Simple JSON | yes | yes | no | no | portable interchange format |

Claude Code discovery and writes honor `CLAUDE_CONFIG_DIR`; otherwise Moirai
uses `~/.claude/projects`.

See the [Claude Code adapter reference](docs/harnesses/claude-code.md).

Run `moirai formats` or `moirai formats --json` for the machine-readable
capability registry.

## Inspect, convert, search, and archive

Format detection is automatic for supported JSON and JSONL files; use `--from`
when a source is ambiguous.

```bash
moirai inspect session.jsonl
moirai convert session.jsonl --to codex --out rollout.jsonl
moirai show SESSION_ID --format codex
moirai search 'database migration' --format claude_code
moirai export SESSION_ID --format cursor --out session.json
moirai import session.json --to pi
```

Portable `.moirai` archives contain canonical session JSON and a SHA-256
integrity digest:

```bash
moirai archive create session.json --out session.moirai
moirai archive verify session.moirai
moirai archive inspect session.moirai
moirai archive inspect session.moirai --json
```

`archive inspect` verifies the transcript digest and prints selected metadata
and block counts without printing message bodies or block payloads. Archives do
not retain source-conversion warnings, so a zero warning count does not mean the
original conversion was lossless.

Deletion is deliberately explicit:

```bash
moirai delete SESSION_ID --format pi --yes
```

For OpenCode, `delete` uses the harness's native archive flag rather than
physically removing the session.

## Go API

```go
package main

import (
	"fmt"
	"os"

	moirai "github.com/october-dev/moirai"
)

func main() {
	data, _ := os.ReadFile("session.jsonl")
	parsed, format, err := moirai.Parse(data, "", moirai.ParseOptions{
		Limits: moirai.DefaultLimits(),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(format, len(parsed.Transcript.Messages))
}
```

The Go package also exposes the codec and store registries, transcript
validation, safe range selection, bounded text projection, fuzzy search,
archive verification, and launch-command generation.

## TypeScript API

```ts
import {
  decodeArchive,
  encodeArchive,
  search,
  select,
  SimpleCodec,
  toText,
} from "@october-dev/moirai";

const transcript = new SimpleCodec().parse(input).transcript;
const excerpt = select(transcript, { start: 5, end: 20 });
const prompt = toText(excerpt, { maxBytes: 64 * 1024, includeTools: true });
const hits = search(transcript, "failing migration");
const archive = await encodeArchive(transcript);
await decodeArchive(archive); // validates the digest and transcript
```

The SDK uses strict types, Web Crypto, configurable safety limits, and the same
schema and archive representation as the Go implementation.

## Continuity boundary

Different harnesses do not expose identical runtime state. Moirai carries the
durable, inspectable portion represented by the canonical model and reports
known omissions as warnings. Native readers currently do not synthesize
`unknown` blocks or extension fields for every unmodelled record, so conversion
is not byte-lossless. See [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) for the
per-format boundary. Moirai does not recreate model-side state, in-memory
processes, terminal state, or hidden reasoning that a harness never persisted.

Moirai does not authenticate to remote chat services. Web conversation support
accepts only exports supplied directly by the user.

## Safety

- Input size, message, block, metadata, nesting, text, and inline-media limits
  are enforced before untrusted content is accepted. Standalone files default
  to 32 MiB; local-store sessions default to 512 MiB. Commands expose
  `--max-input-bytes` for an explicit override.
- Local writes are atomic, private (`0600` files and `0700` directories), and
  constrained to validated store paths.
- Symlink and traversal checks protect store load, save, and deletion.
- Existing source sessions are not overwritten during cross-harness handoff.
- Archives are integrity checked before use.
- No command embedded in transcript content is executed by conversion.
- Human-readable terminal output removes C0/C1 control sequences; `--json`
  retains the original data.

Session histories can still contain secrets that the source harness recorded.
Local conversion preserves their contents. Cloud publication preparation removes
workspace metadata and persisted thinking by default and redacts known token
patterns, but cannot identify every project secret. Inspect the prepared archive
before confirming upload. See [SECURITY.md](SECURITY.md).

## Format and compatibility

The language-independent canonical format is documented in
[docs/FORMAT.md](docs/FORMAT.md) and published as a
[JSON Schema](schema/moirai-session.schema.json). Schema `1.0` readers reject
unknown major versions. Native codecs preserve the documented canonical subset
and surface known omissions as warnings; the compatibility matrix documents
fields that are intentionally outside that subset.

## Omarchy plugin

This repository is also an [Omarchy](https://omarchy.org) shell plugin
(`manifest.json` at the root, QML under `omarchy/`). It adds a bar widget that
lists the sessions `moirai list --json` finds on the machine and continues the
one you pick in the harness you choose, inside a terminal.

Install the CLI first; the widget only calls the `moirai` binary on your PATH:

```bash
go install github.com/october-dev/moirai/cmd/moirai@latest
omarchy plugin add https://github.com/october-dev/moirai.git --enable
```

Remove it with:

```bash
omarchy plugin remove io.github.october-dev.moirai
```

Settings (Setup › Plugins, or inline on the widget's `shell.json` entry):
`continueWith` (target harness, default `claude_code`), `sourceFormat`
(optional single source format), `refreshIntervalSec` (10–3600, default 120),
`maxSessions` (1–50, default 12).

What it runs: `moirai list --json` on a timer and when the popup opens, and
`omarchy-launch-tui moirai continue <id> --from <format> --with <harness>` when
you pick a session. Every invocation is an argv vector; session ids and format
names are validated against `[A-Za-z0-9._-]` before use, and titles are
stripped of control characters before display. The plugin needs no sudo, no
network access, and writes nothing except what `moirai continue` saves into the
destination harness's own store. External dependencies: the `moirai` CLI and
the destination harness itself.

## Contributing

Looking for a place to start? Browse the
[`good first issue`](https://github.com/october-dev/moirai/labels/good%20first%20issue)
and [`help wanted`](https://github.com/october-dev/moirai/labels/help%20wanted)
queues. Each issue defines its expected behavior, safety boundary, and tests.

Run the complete local checks before opening a pull request:

```bash
go test -race ./...
go vet ./...
go build ./cmd/moirai
npm --prefix sdk/typescript ci
npm --prefix sdk/typescript test
npm --prefix sdk/typescript pack --dry-run
```

Read [CONTRIBUTING.md](CONTRIBUTING.md) for fixture and compatibility rules.
Report vulnerabilities privately through [SECURITY.md](SECURITY.md).

## License

Moirai is licensed under the [Apache License 2.0](LICENSE).

The license applies to the code and documentation, but does not grant rights to
October's names, logos, or brand assets.

---

<div align="center">

An [October open-source project](https://october.dev/open-source) · [Website](https://moirai.to) · [GitHub](https://github.com/october-dev)

</div>
