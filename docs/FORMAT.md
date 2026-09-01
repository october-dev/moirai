# Moirai session format 1.0

Moirai's canonical transcript is the stable boundary between native harness
adapters. JSON is UTF-8. Unknown object fields may be retained by an
implementation, but readers must reject an unsupported `schema_version`.

The machine-readable definition is
[`schema/moirai-session.schema.json`](../schema/moirai-session.schema.json).

## Document

```json
{
  "schema_version": "1.0",
  "meta": {
    "id": "019c...",
    "timestamp": "2026-01-01T00:00:00Z",
    "cwd": "/workspace/project",
    "git_branch": "main",
    "provenance": {
      "source_format": "claude_code",
      "source_session_id": "previous-id",
      "imported_at": "2026-01-01T00:30:00Z"
    }
  },
  "messages": [
    {
      "role": "user",
      "content": [{ "type": "text", "text": "Run the tests" }]
    },
    {
      "role": "assistant",
      "content": [
        {
          "type": "tool_use",
          "id": "call-1",
          "name": "shell",
          "input": { "command": "go test ./..." }
        }
      ]
    },
    {
      "role": "user",
      "content": [
        {
          "type": "tool_result",
          "tool_use_id": "call-1",
          "content": { "stdout": "ok" }
        }
      ]
    }
  ]
}
```

## Blocks

| Type | Required fields | Meaning |
| --- | --- | --- |
| `text` | `text` | Visible message text |
| `thinking` | `text` or `encrypted` | Reasoning explicitly present in native data |
| `tool_use` | `id`, `name` | Tool invocation; `input` is arbitrary JSON |
| `tool_result` | — | Tool output; `tool_use_id` links to an earlier call |
| `image` | `source` | Inline, file, or URL media reference |
| `artifact` | `artifact.name` | Named output with optional source and digest |
| `unknown` | `data` | Native event retained without pretending equivalence |

Known `tool_use.id` values are unique. A non-empty `tool_result.tool_use_id`
must reference an earlier call. Range operations reject selections that split a
known pair.

## Identity and ancestry

Importing into another native store creates a fresh `meta.id`. Provenance
records the source format, source ID, and import time. A range selection also
records `parent_session_id` and a checkpoint such as `messages:12-38`.

Timestamps use RFC 3339. Message ordering is authoritative when individual
message timestamps are absent.

## Extensions and information loss

`extra` accepts format-specific JSON at transcript, metadata, and message level,
and `unknown` can retain a native event supplied by a caller. The current native
readers do not populate these fields for every unmodelled record. They emit
warnings for known omissions and repairs; callers must surface those warnings
and must not treat native conversion as byte-lossless. The exact adapter
boundary is listed in [COMPATIBILITY.md](COMPATIBILITY.md).

Hidden model state, live processes, terminal state, credentials, and data that
the source did not persist are outside the format.

## Archives

A `.moirai` archive is a JSON envelope:

```json
{
  "format": "moirai.session",
  "version": "1",
  "created_at": "2026-01-01T01:00:00Z",
  "transcript": { "schema_version": "1.0", "meta": { "id": "session-id" }, "messages": [] },
  "sha256": "hex digest of the compact canonical transcript JSON"
}
```

The digest input is the schema-known transcript after strict decoding: object
keys are recursively sorted, strings are UTF-8 JSON strings without HTML
escaping, numbers use a fixed 17-digit IEEE-754 exponent form, and no whitespace
is included. Unknown transcript fields are rejected. This canonical form is
identical in the Go and TypeScript SDKs.

The digest protects integrity, not authenticity or confidentiality. Verify it
before accepting the transcript, and protect the archive separately in storage
and transit.

## Default safety limits

| Resource | Limit |
| --- | ---: |
| Standalone input | 32 MiB |
| Local-store session | 512 MiB |
| Messages | 100,000 |
| Blocks | 500,000 |
| Text or structured block payload | 1 MiB |
| Inline media | 16 MiB decoded |
| Metadata/extension payload | 1 MiB |
| JSON nesting | 64 levels |

Applications may choose stricter positive values. The CLI accepts
`--max-input-bytes`; relaxing limits for untrusted input should be treated as a
security decision.
