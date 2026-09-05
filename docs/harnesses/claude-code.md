# Claude Code adapter reference

This reference describes Moirai's `claude_code` adapter and its current source
behavior. The [native compatibility boundary](../COMPATIBILITY.md) remains the
contract: conversion carries durable conversation context, not a byte-for-byte
copy of native runtime or UI state. Claude Code supports reading, writing,
discovery, saving, deletion, and continuation (`ClaudeCodeCodec.Info`).

Source: [codec_claude.go](../../codec_claude.go).

## Native JSONL shape

Sessions use one JSON object per line at
`<root>/<encoded-cwd>/<sessionId>.jsonl`. Conversational records have type
`user` or `assistant`; `summary` supplies a title. Non-conversational records,
such as `file-history-snapshot`, can appear alongside them.

Conversational keys include `type`, `sessionId`, `cwd`, `uuid`, `parentUuid`,
`timestamp`, `gitBranch`, `version`, and `message`. The message payload contains
`role` and `content`, with optional `id`, `model`, `stop_reason`, and `usage`.
These keys describe the native shape; they are not all required or retained by
`ClaudeCodeCodec.Parse`.

The following example is entirely synthetic, modeled on the structure of
[testdata/native/claude_code.jsonl](../../testdata/native/claude_code.jsonl).
Its paths, IDs, model label, and text are invented; it contains no real session
data, credentials, customer data, hidden reasoning, or proprietary source.

```jsonl
{"type":"file-history-snapshot","messageId":"example-snapshot","snapshot":{"trackedFileBackups":{}}}
{"type":"user","sessionId":"bbbbbbbb-2222-4333-8444-555555555555","cwd":"/tmp/adapter.demo","uuid":"example-user","parentUuid":null,"timestamp":"2026-01-02T12:00:00Z","gitBranch":"main","version":"example-version","message":{"role":"user","content":[{"type":"text","text":"Say hello for this synthetic example."}]}}
{"type":"assistant","sessionId":"bbbbbbbb-2222-4333-8444-555555555555","cwd":"/tmp/adapter.demo","uuid":"example-assistant","parentUuid":"example-user","timestamp":"2026-01-02T12:00:01Z","gitBranch":"main","version":"example-version","message":{"id":"msg_example","role":"assistant","content":[{"type":"text","text":"Hello from the example."}],"model":"example-model","stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":5}}}
{"type":"summary","summary":"Synthetic adapter example","leafUuid":"example-assistant"}
```

Sources: `ClaudeCodeCodec.Parse` and `ClaudeCodeCodec.Render` in
[codec_claude.go](../../codec_claude.go); `claudeLayout` in
[local_store.go](../../local_store.go).

## Store root, discovery, and limits

`DefaultStores` computes the root as
`envOr("CLAUDE_CONFIG_DIR", ~/.claude)/projects`. Normally this is
`~/.claude/projects`; setting `CLAUDE_CONFIG_DIR` to `/tmp/example-claude`
selects `/tmp/example-claude/projects` for discovery and writes. `envOr` uses
`expandHome`, which expands a `~/...` prefix on Unix (using the platform path
separator). A bare `~` currently joins the home directory with a literal `~`;
use an absolute path or `~/...` when setting the variable.

`LocalFileStore.DiscoverWithLimits` walks JSONL files while skipping symlinks.
For Claude Code it prunes `subagents`, `tool-results`, and directories shadowed
by a sibling JSONL file: for example, `example/` is pruned when `example.jsonl`
exists next to it. Directory pruning produces no per-sidechain warning.
Discovery deduplicates session IDs and retains the shallowest location, using
`locationDepth`; an equally deep later candidate does not replace the first.

Discovery is distinct from parsing:

- `shallowJSONLMetadata` processes at most 512 lines and does not validate the
  full transcript. It skips lines it cannot decode and does not filter inline
  `isSidechain` records.
- It starts with the filename stem as the ID. Each conversational row assigns
  `meta.ID = firstNonEmpty(record.sessionId, meta.ID)`, so a later non-empty
  `sessionId` can replace an earlier ID or the filename fallback.
- It exits early once a conversational row has been seen and ID and timestamp
  are available. A model or summary title appearing later can therefore be
  absent from discovery even though a full parse would read it.
- `discoverMetadata` falls back to full `Parse` only when the shallow pass
  returns no usable metadata (no conversational row with an ID). A missing
  title, model, or timestamp alone does not trigger fallback. A scanner error
  is returned directly, and discovery skips that file; it does not retry with
  full parsing.

`Discover` uses `DefaultStoreLimits`, whose `DefaultStoreMaxInputBytes` is
512 MiB. Store `Load` also uses this cap when no positive input limit is
supplied. Direct `Parse` uses the lower 32 MiB input default from
`DefaultLimits`. Callers can supply limits; other validation limits still
apply. Files exceeding the discovery cap are skipped while other references
are returned, with a `DiscoveryError` carrying code `skipped_oversize`, a path,
a count, and `ErrLimitExceeded`. `StoreRegistry.DiscoverWithLimits` exposes
that code as a warning.

Sources: `DefaultStores`, `envOr`, `expandHome`, `LocalFileStore.Discover`,
`LocalFileStore.DiscoverWithLimits`, `discoverMetadata`, `shallowJSONLMetadata`,
`locationDepth`, and `LocalFileStore.Load` in
[local_store.go](../../local_store.go); `DefaultLimits`, `DefaultStoreLimits`,
and `Limits.storeNormalized` in [limits.go](../../limits.go);
`StoreRegistry.DiscoverWithLimits` in [store.go](../../store.go).

## Project encoding and message order

`encodeClaudeProject` preserves only ASCII `A-Z`, `a-z`, and `0-9`, replacing
every other rune with `-`. It first strips a Windows extended-path prefix
`\\?\`; an empty working directory maps to `-`. For example,
`/tmp/adapter.demo` becomes `-tmp-adapter-demo`. This encoding is lossy and
cannot reconstruct the original working directory. `claudeLayout` combines
the encoded directory with the filename `<sessionId>.jsonl`.

`ClaudeCodeCodec.Parse` does not use `parentUuid` or `leafUuid` to reconstruct
or select a branch. It keeps retained messages in file order.
`ClaudeCodeCodec.Render` creates a fresh linear `parentUuid` chain in that
order: the first parent is null and each following parent is the previous
message's UUID. When a title is set, it appends a summary whose `leafUuid` is
the final message's UUID. This is the concrete effect of excluding the native
parent graph from the portable context.

Sources: `encodeClaudeProject` and `claudeLayout` in
[local_store.go](../../local_store.go); `ClaudeCodeCodec.Parse` and
`ClaudeCodeCodec.Render` in [codec_claude.go](../../codec_claude.go).

## Fields read into the canonical model

For `Parse`, metadata below is collected from non-sidechain `user` and
`assistant` rows, except for the title. The first non-empty value wins.
The shallow discovery pass uses the same metadata fields within its scanned
prefix, but has the ID precedence and sidechain differences described above.

| Canonical field | Native source and scope |
| --- | --- |
| `Meta.ID` | Top-level `sessionId`; first non-empty in `Parse`, latest non-empty scanned value in discovery |
| `Meta.CWD` | Top-level `cwd` |
| `Meta.GitBranch` | Top-level `gitBranch` |
| `Meta.CLIVersion` | Top-level `version` |
| `Meta.Timestamp` | Top-level `timestamp`, after timestamp conversion |
| `Meta.Title` | A `summary` record's top-level `summary` field, not a nested message field |
| `Meta.Model` | `message.model`; `Parse` reads it from retained messages, discovery from scanned conversational rows |
| Message `ID` | Top-level `uuid`, not `message.id` |
| Message `Role` | Record `type`, overridden by `message.role` only if that role is `user` or `assistant` |
| Message `Content` | `message.content` through `parseAnthropicContent` |
| Message `Timestamp`, `Model`, `StopReason` | Top-level `timestamp`, `message.model`, `message.stop_reason`, respectively |
| Message `Usage` | `message.usage` through `usageFromMap`, including input/output and cache input token counts |

`Parse` collects the top-level metadata before content parsing. A row whose
blocks are all dropped can still supply that metadata, but not `Meta.Model`.
Message fields describe individual retained messages and are not selected
across rows by the metadata first-non-empty rule.

`timestampValue` accepts RFC3339 timestamps (including fractional seconds) or
integer epochs supplied as JSON numbers or strings. Epochs mean seconds unless
the value is greater than `100_000_000_000`, when they mean milliseconds.
Recognized values become UTC RFC3339Nano strings.

After parsing, `normalizeTranscript` fills a missing session ID from
`ParseOptions.SourceID`, then calls `NewID` if it is still missing. For Claude
Code, a missing metadata timestamp becomes the current time. A missing message
timestamp inherits the previous available message timestamp, initially the
metadata timestamp. This normalization does not synthesize message UUIDs.

Sources: `ClaudeCodeCodec.Parse` in [codec_claude.go](../../codec_claude.go);
`firstNonEmpty`, `timestampValue`, `unixTimestamp`, `usageFromMap`, and
`normalizeTranscript` in [codec_helpers.go](../../codec_helpers.go);
`shallowJSONLMetadata` in [local_store.go](../../local_store.go);
`NewID` in [identity.go](../../identity.go).

## Fields and blocks written

`ClaudeCodeCodec.Render` validates the transcript and uses `RenderOptions.ID`
when supplied, otherwise `Meta.ID`. It emits one record per message with
`type`, `uuid`, the linear `parentUuid`, `sessionId`, `timestamp`, `cwd`,
`gitBranch`, `version`, and a `message` payload containing `role` and `content`.
A missing message UUID is generated with `uuidFromSeed`; a missing message
timestamp falls back to `Meta.Timestamp`.

Assistant payloads additionally receive `id` as `"msg_" + stableID(...)`,
`type: "message"`, and `model` from the message, then `Meta.Model`, then
`"unknown"`. `stop_reason` keeps the supplied value or defaults to `"tool_use"`
when a tool-use block is present, otherwise `"end_turn"`. Usage is emitted only
on assistant messages with non-nil `Usage`. A non-empty title produces the
trailing top-level summary record described above.

`parseAnthropicContent` and `renderClaudeContent` handle these block forms:

| Content | Parse/render behavior |
| --- | --- |
| Text | A string becomes a text block; native text blocks render as `text` |
| Tool use | Missing call IDs use `syntheticToolID(messageIndex, blockIndex)`, such as `tool-1-1`; rendering emits `tool_use` with ID, name, and decoded JSON input |
| Tool result | A missing result ID consumes the oldest ID in the pending-call FIFO; rendering emits `tool_result` with `tool_use_id`, content, and `is_error` |
| Signed thinking | Renders as `thinking` with `thinking` text and `signature` |
| Encrypted thinking without a signature | Renders as `redacted_thinking` with opaque `data`; redacted thinking stays redacted |
| Plain thinking without signature or encrypted data | Renders as visible text prefixed with `[Reasoning]\n` |
| Image | Renders an `image` with `source` only when the canonical `Source` is non-nil |

The pending-call queue is a fallback for missing IDs, not graph reconstruction:
an explicitly identified result does not consume an entry from that queue.

`claudeToolResultContent` keeps a JSON string as a string and keeps arrays whose
entries all have type `text` or `image`. Other decoded JSON values become
JSON-encoded strings. Empty raw content becomes an empty string; raw bytes that
cannot be decoded as JSON are returned as text by the helper.

Sources: `ClaudeCodeCodec.Render`, `renderClaudeContent`, and
`claudeToolResultContent` in [codec_claude.go](../../codec_claude.go);
`parseAnthropicContent`, `stableID`, and `uuidFromSeed` in
[codec_helpers.go](../../codec_helpers.go); `syntheticToolID` in
[simple.go](../../simple.go).

## Omissions, warnings, and errors

Applications **must display warnings**, as required by the
[compatibility boundary](../COMPATIBILITY.md). The following describes common
cases, not an exhaustive list or a promise that every omission warns.

| Code or outcome | Current behavior |
| --- | --- |
| `native_record_omitted` | Once per non-empty omitted record kind, including file snapshots and progress/system/queue kinds encountered by `Parse`; inline sidechains are dropped and reported under the `sidechain` kind |
| `native_fields_omitted` | Once when any non-sidechain `user`/`assistant` record has non-null `isMeta`, `userType`, `requestId`, `agentId`, or `toolUseResult`, even if the value is false; the check precedes content parsing, so a row whose blocks are all dropped can trigger it |
| `unknown_block` | `parseAnthropicContent` omits an unrecognized block kind |
| `invalid_block` | It omits empty text, thinking with neither text nor encrypted data, or a tool call without a name |
| `invalid_json` | `decodeJSONLines` warns for malformed non-blank lines and continues; input/depth limit failures can instead terminate parsing |
| `extension_omitted` / `unsupported_block` | Render-side `finalizeRender` / `renderLossWarnings` report canonical extension data or blocks the destination cannot represent |
| `ErrInvalidTranscript` | `Parse` rejects a conversation with no retained messages |

Rows with an empty or missing type are silently skipped. Summary rows are
consumed as titles without an omission warning. Empty-string message content
is silently ignored. Discovery directory pruning has no per-sidechain warning,
and shallow discovery does not return the parse-time omission warnings. Native
readers do not preserve every discarded field in `extra` or `unknown`.

Sources: `ClaudeCodeCodec.Parse` in [codec_claude.go](../../codec_claude.go);
`parseAnthropicContent`, `decodeJSONLines`, `finalizeRender`, `renderLossWarnings`,
and `renderSupportsBlock` in [codec_helpers.go](../../codec_helpers.go);
`LocalFileStore.DiscoverWithLimits` and `shallowJSONLMetadata` in
[local_store.go](../../local_store.go).

## Saving and continuation

`LocalFileStore.Save` writes to the encoded project directory with the supplied
ID (`RenderOptions.ID`, falling back to `Meta.ID`). It uses
`atomicWriteExclusive` with mode `0600` and refuses to overwrite an existing
destination with `ErrSessionExists`. Saving does not itself mint a fresh ID.

`CommandFor` produces `claude --resume <sessionId>` with `Dir = saved.CWD`.
`Launch` uses the process working directory when that directory is empty,
unavailable, or not a directory.

Fresh session identity and provenance belong to the handoff layer:
`Registry.Convert` assigns a new ID and source provenance when the source and
target formats differ. CLI `importSession` creates a new ID and provenance for
an import or continuation; when the original working directory is unavailable,
it records `SourceCWD`, uses the process working directory, and emits
`cwd_rehomed`. Claude rendering does not serialize provenance, so it is absent
after a native Claude render/parse round trip.

Sources: `LocalFileStore.Save` and `claudeLayout` in
[local_store.go](../../local_store.go); `atomicWriteExclusive` in
[store.go](../../store.go); `CommandFor` and `Launch` in
[launch.go](../../launch.go); `Registry.Convert` in
[registry.go](../../registry.go); `importSession` in
[cmd/moirai/main.go](../../cmd/moirai/main.go); `ClaudeCodeCodec.Render` in
[codec_claude.go](../../codec_claude.go).

## Contributor fixtures and checks

Use the synthetic [native fixture](../../testdata/native/claude_code.jsonl)
as a structural reference. Keep additions synthetic: no real transcripts,
credentials, customer data, hidden reasoning, or proprietary source.

Relevant existing tests are:

- `TestNativeShapedReaderFixtures` and `TestNativeCodecRoundTrips` in
  [codecs_test.go](../../codecs_test.go).
- `TestClaudeNativeLayoutAndDiscovery`, `TestClaudeDetectionAndSidechainContract`,
  `TestClaudeToolResultContract`, and `TestLargeClaudeSessionRemainsDiscoverable`
  in [production_regression_test.go](../../production_regression_test.go).
- `TestDefaultStoresHonorClaudeConfigDir` in [store_test.go](../../store_test.go).
- `TestNativeFileStoresLifecycle` in [native_stores_test.go](../../native_stores_test.go).

Run these contributor checks from the repository root:

```bash
go test -race ./...
go vet ./...
go test -run 'Test(Claude|LargeClaude|DefaultStoresHonorClaudeConfigDir|NativeShapedReaderFixtures|NativeCodecRoundTrips|NativeFileStoresLifecycle)' ./...
```
