<div align="center">

<img src="assets/moirai-wordmark-v7.svg" alt="Moirai terminal wordmark" width="760">

# Moirai

**Move an AI-agent session to another harness and keep working.**

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

## Free and hosted versions

Moirai is building a collaboration layer for agent work—something like GitHub
for agent sessions. Local sessions remain ordinary files under your control,
while an optional hosted service will make those sessions shareable and
collaborative across people, machines, and agent harnesses. The model is
similar to Git: work locally for free, publish only what you choose, and use a
remote when you want distribution and collaboration.

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

### Moirai Cloud: free and paid hosted plans (planned)

Moirai Cloud will be the optional managed remote for agent sessions. It is
intended for people who want cross-machine sharing and collaboration without
operating storage, APIs, servers, or deployment infrastructure themselves. A
free hosted tier and paid plans are planned; exact limits, pricing, and launch
availability will be published before the service opens.

The planned hosted workflow is:

1. A user explicitly publishes a selected session checkpoint and receives a
   shareable link.
2. Someone on another machine opens that link, chooses a supported harness,
   and continues the session as a new local session.
3. A user can fork a shared session at a checkpoint and explore a different
   approach without changing the original.
4. Contributors can share work derived from a session back with its owner,
   while Moirai retains the ancestry needed to understand where it came from.

The analogy to Git is about workflow, not storage format:

| Agent workflow | Git-like idea |
| --- | --- |
| Local harness session | Working copy |
| Portable session checkpoint | Commit |
| Published hosted session | Remote history |
| Continue from a shareable link | Clone and check out |
| Start an independent continuation | Fork or branch |
| Share derived agent work with the owner | Contribution based on common ancestry |

Hosted collaboration will build on the same canonical transcript and
provenance model as the local tools. Receiving a shared session will create a
new destination session rather than overwrite the publisher's original.
Different harnesses still expose different runtime state, so hosted handoffs
will report the same compatibility warnings and continuity boundary described
below.

Local discovery never implies cloud upload. Publishing will be an explicit
action because agent histories may contain source code, tool output,
credentials, or other sensitive material. Visibility, access, retention, and
security behavior for hosted sessions will be documented before launch. Until
then, the hosted capabilities described here are product direction, not a
claim that the service is currently available.

## Install

Go 1.25.8 or newer:

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

## Continue a session

List sessions that are already on the machine:

```bash
moirai list
moirai list --format claude_code
```

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
```

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
Inspect an export before sharing it; Moirai intentionally does not guess which
project data should be redacted. See [SECURITY.md](SECURITY.md).

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

Built in the open by [October](https://october.dev) · [GitHub](https://github.com/october-dev)

</div>
