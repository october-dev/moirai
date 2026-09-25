# MCP server

`moirai mcp` serves local agent histories through the Model Context Protocol over
stdio. It exposes four read-only tools. The server does not save or delete
sessions, launch harnesses, execute commands, fetch URLs, or accept standalone
file paths. Protocol messages go to stdout; diagnostics go to stderr.

## Client configuration

Configure an MCP client to start this process:

```json
{"command":"moirai","args":["mcp"]}
```

For clients with an `mcpServers` configuration map:

```json
{
  "mcpServers": {
    "moirai": {"command": "moirai", "args": ["mcp"]}
  }
}
```

Use an absolute executable path if `moirai` is not on the client's `PATH`. The
process inherits the client's environment and resolves the same store roots as
the other Moirai commands, including their environment overrides. There are no
MCP-specific command-line flags, network listeners, or authentication settings.

## Tools and successful results

All arguments are JSON objects. Unknown properties are rejected. A supplied
`format` must be a canonical name returned by `formats`, such as `claude_code`.
Omitting `format` on list/search includes all registered stores.

| Tool | Arguments | `structuredContent` |
| --- | --- | --- |
| `formats` | `{}` | `{ "formats": [...] }`, harness information and capabilities in canonical registry order |
| `list_sessions` | Optional `format`, `limit` (default 50, maximum 100) | `{ "sessions": [...], "warnings": [...], "truncated": false }` |
| `show_session` | Required `format`, `selector`; optional `thinking` (default false), `tools` (default true) | `{ "transcript": {...}, "warnings": [...] }` |
| `search_sessions` | Required nonblank `query`; optional `format`, `limit` (default 20, maximum 100) | `{ "hits": [{ "session": {...}, "hit": {...} }], "warnings": [...] }` |

Successful results also contain a readable `content` text block. Each tool
advertises an output schema for its successful structured result. Empty result
collections and warnings are `[]`, never `null`. Canonical transcript fields keep
their existing optional-field semantics.

List order is newest first, using the store registry's modification/start times.
`truncated` indicates that the list's limit omitted references; it is not a
pagination cursor. Search visits sessions in that same order and uses Moirai's
existing case-insensitive substring/fuzzy search, with score ordering within each
session. `limit` caps total hits, not sessions scanned. A hit includes the session
reference, one-based message/block indices, role, kind, text snippet, and score.
Discovery and parse warnings are surfaced; a failed search load adds a
`store_load_failed` warning while other sessions can still produce hits.

`show_session` resolves only discovered references in the requested store. It
accepts an exact ID, unambiguous ID/title prefix, exact title, or relative store
location. Absolute paths and parent-directory traversal are refused. A known
format without a store returns an `unsupported` tool error.

Selectors support one-based message spans and retain Moirai's existing rules
against separating tool calls from their results:

```json
{"format":"claude_code","selector":"SESSION_ID#3-6"}
```

`SESSION_ID#3-` selects through the last message. A span is applied after loading
the source, so it reduces output size without reducing source parsing work.

## Error contract and session lifecycle

Invalid argument shapes/types, unknown formats, and malformed selector syntax
return JSON-RPC `-32602` (invalid params). Domain failures return a tool result
with `isError: true`, a readable explanation, and a stable code under
`_meta["moirai/error_code"]`. Error results omit `structuredContent`; the success
schema does not describe errors.

For example, an oversized `show_session` result has these fields (the SDK may
also supply protocol-version-specific result fields):

```json
{
  "isError": true,
  "_meta": {"moirai/error_code": "response_too_large"},
  "content": [{
    "type": "text",
    "text": "Result exceeds 1 MiB; use a smaller selector span such as SESSION_ID#1-10."
  }]
}
```

| Code | Meaning |
| --- | --- |
| `not_found` | No discovered session matches the selector. |
| `unsupported` | The format has no supported store operation. |
| `invalid_session` | Invalid transcript, ambiguous selector, or invalid selection boundaries. |
| `unsafe_path` | Absolute/traversing selector or a store path safety check failed. |
| `limit_exceeded` | Tool arguments or a store parser exceeded a safety limit. |
| `response_too_large` | The complete encoded tool result exceeds 1 MiB. |
| `busy` | Four tool calls are already running; retry when one finishes. |
| `cancelled` | The client cancelled the operation. |
| `deadline_exceeded` | The tool's cooperative execution deadline elapsed. |
| `store_error` | Another store operation failed. |
| `internal_error` | An unexpected tool panic or result-encoding failure was contained. |

A failed tool call leaves the session available for another call or a ping.
Panic recovery is at the tool boundary, and its stderr diagnostic records the
tool name without dumping the panic value or transcript. Client cancellation is
forwarded to store operations; a cancelling client may stop awaiting the result.
Closing stdin or cancelling the server context closes the transport and waits
for active handlers to finish cooperatively.

Malformed transport input terminates the session. This release does not impose an application-defined transport input-size cap; tool arguments/results and store parsing have their separately documented limits.

The official SDK owns JSON-RPC decoding, framing, initialization, and shutdown.
A large valid JSON message can be fully buffered before tool validation. Bad
framing disconnects cleanly; it is not recovered as a tool error.

## Limits and cancellation

| Boundary | Limit |
| --- | --- |
| Raw encoded tool arguments | 16 KiB, checked after SDK transport decoding |
| Argument strings | `format`: 64 characters; `selector`: 4,096; `query`: 1,024 |
| Explicit list/search `limit` | Integer from 1 through 100 |
| Encoded tool result | 1 MiB for the entire populated `CallToolResult` |
| Active tool calls | 4; excess calls receive `busy` without waiting |
| Tool execution deadline | 10 seconds, cooperative |

The result budget includes text, structured output, metadata, JSON escaping, and
fields populated by the SDK. It excludes the enclosing JSON-RPC response
envelope. Enforcement runs after SDK result population. Oversized results are
replaced by the small typed error above, never a silently cut canonical
transcript. `show_session` advises a smaller span; list/search advise reducing
`limit` or choosing one format.

Store discovery and loading use `DefaultStoreLimits`: 512 MiB input, 100,000
messages, 500,000 blocks, 1 MiB text/tool JSON fields, 16 MiB inline media,
1 MiB metadata fields, and nesting depth 64. These are the existing parser and
validation limits, not an aggregate memory allowance for discovery or search.

Deadlines/cancellation reach `Discover` and `Load`, with checks before/after tool
work and between search loads. They cannot forcibly interrupt an existing store
implementation's synchronous file read, JSON/SQLite parsing, text search, or
result encoding midway. A call that ignores its context may outlast 10 seconds;
its concurrency slot stays occupied until it actually returns. Ping remains
available while tool slots are occupied. The result limit bounds returned bytes,
not peak allocation: source parsing, rendering, and encoding may allocate more
before the result is checked. There is no aggregate transport memory cap.

## Privacy and trust boundary

Read-only access is not redaction or a current-working-directory restriction.
The client can read any history exposed by the process's configured stores and
filesystem credentials, including sessions from other projects. Environment
overrides can place those stores outside the working directory. Access is granted
by allowing the client to spawn the process; stdio adds no separate user login.

`thinking` and `tools` only control the readable text rendering of
`show_session`. Its structured transcript always retains all selected blocks,
including persisted reasoning, tool inputs/results, inline media, and metadata.
Search also examines stored reasoning and tool content. Neither option is a
privacy filter. Paths/URLs present in transcript data are returned as data, not
opened or fetched as external resources.

Text content is terminal-scrubbed to remove control sequences. Structured results
retain original strings, escaped by JSON; consuming clients must render them
safely. Titles, messages, and warnings remain untrusted data, even when they
contain apparent instructions. Scrubbing does not remove prompt injections or
secrets. The MCP client may send returned data to its model/provider, so connect
only clients trusted with the histories accessible to this process.

## Dependency and implementation design note

The CLI pins the official `github.com/modelcontextprotocol/go-sdk` at **v1.7.0**
and uses its existing `github.com/google/jsonschema-go` dependency (**v0.4.3**)
directly for schemas and validation. There is no custom framing
adapter. The lower-level `Server.AddTool` API preserves control over the
invalid-params versus domain-error distinction; success schemas describe the
canonical Go result types, including arbitrary embedded JSON. SDK receiving
middleware owns concurrency/deadline admission and checks the final result size
after the SDK has populated protocol fields.

The four tools reuse the existing format registry, store discovery/loading,
selector/selection, search, and text renderer. The CLI's stored-session loader
has a registry-accepting helper for hermetic tests; other commands keep their
default wrapper. Store interfaces, codecs, save/launch behavior, and canonical
formats are unchanged.

The dependency cost was measured against `main` at `cbf5b2a`, using Go 1.26.8,
Linux amd64, `CGO_ENABLED=1`, and identical build flags:

| Measure | Before | With MCP | Increase |
| --- | ---: | ---: | ---: |
| Stripped CLI bytes | 11,518,217 | 13,385,993 | 1,867,776 (16.2%) |
| Compiled dependency packages | 210 | 244 | 34 |
| Selected module graph entries | 30 | 39 | 9 |

Reproduce on each revision with:

```bash
go build -trimpath -ldflags='-s -w' -o /tmp/moirai-measure ./cmd/moirai
wc -c /tmp/moirai-measure
go list -deps ./cmd/moirai | wc -l
go list -m all | wc -l
```

Module graph counts include dependencies that are not linked into the CLI.
These measurements make the SDK cost explicit before merge; they are not a
cross-platform size guarantee. Review the footprint, error semantics, schema
validation, malformed-input shutdown, cancellation, and final-result budget
tests when upgrading the pinned SDK. Protocol ownership stays with the maintained
SDK instead of a parallel implementation in Moirai.
