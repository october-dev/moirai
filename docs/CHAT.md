# Plain chats and Concord

Moirai can discover, convert, and archive ordinary chats locally. No account,
upload, network listener, background service, or model call is needed. Keep API keys
in the destination app's own credential store; they are never needed for conversion.

## Generic chat JSON (`chat`)

```json
{
  "model": "example/model",
  "messages": [
    { "role": "system", "content": "Be concise." },
    { "role": "user", "content": "Hello" },
    { "role": "assistant", "content": "Hi" }
  ]
}
```

This is an OpenAI-style **conversation object**, not a completion response, SSE
stream, or promise of support for every OpenAI API field/content variant.

- Roles are exactly `system`, `user`, and `assistant`; order is preserved.
- `content` is a string (including empty), or an array of nonempty `text` parts and
  `image_url` parts with an `image_url.url`. Empty arrays are preserved.
- Optional conversation fields: `id`, `title`, `model`, `provider`, `created_at`,
  `updated_at`. Optional message fields: `id`, `model`, `created_at`, `usage`.
  A conversation ID, when provided, must not be empty.
- Dates, when supplied, are RFC 3339 strings; no timestamps are invented on import.
- `usage.prompt_tokens` and `usage.completion_tokens` map to canonical input/output
  counts. Zero, absent counts, and additional usage keys are preserved.
- An optional message `attachments` array uses the canonical artifact shape
  (`name`, optional `source`, media type and digest). It follows message content.
- Gateway names, aggregate usage, and additional document/message fields are retained
  in bounded `extra.chat` extensions. Content-part shape and image detail fields are
  retained there too. Message text and image URLs have a single canonical copy.
- Unsupported roles, null/malformed content, unknown content-part types and invalid
  known field types fail explicitly. They are never silently treated as empty chats.

Auto-detection recognizes a messages/model object without canonical `meta` or
`schema_version` and with supported plain content. Use `--from chat` when the input
is ambiguous or has no model yet. An unknown model is never guessed.

```sh
moirai convert conversation.json --from chat --to simple --out canonical.json
moirai convert canonical.json --to chat --out conversation-copy.json
moirai archive create conversation.json --from chat --out conversation.moirai
moirai archive verify conversation.moirai
moirai continue conversation.moirai --with chat
moirai list --format chat
```

The chat destination stores a new JSON file under `~/.moirai/chats` (override with
`MOIRAI_CHAT_DIR`). `continue --with chat` saves without launching an application;
its capability listing therefore does not advertise a native launcher. Existing
files are never overwritten. Point the directory override at a collection of
generic JSON files to discover an existing library.

## Concord (`concord`)

The reference is Concord's actual Swift `ChatModels.swift` and
`ConversationStore.swift` at commit `e614c60936378dc8da481393bc18d19ac1ea821a`.
Its storage is an array, not a messages/model object:

```json
[
  {
    "id": "11111111-1111-4111-8111-111111111111",
    "providerID": "openrouter",
    "title": "Example",
    "model": "example/model",
    "createdAt": "2026-09-01T12:00:00Z",
    "updatedAt": "2026-09-01T12:01:00Z",
    "messages": [
      { "id": "22222222-2222-4222-8222-222222222222", "role": "user", "text": "Hello" }
    ]
  }
]
```

UUIDs, provider ID, title, model, dates, message roles/order/IDs/text, and empty
messages round-trip exactly through canonical JSON and `.moirai`. Concord stores
Markdown images in message text; it has no native attachment or per-message usage
fields. Unknown native fields fail explicitly rather than being silently discarded.

```sh
moirai list --format concord
moirai show CONVERSATION_UUID --from concord
moirai continue CONVERSATION_UUID --from concord --with chat
moirai convert one-conversation.json --from concord --to simple --out canonical.json
moirai convert canonical.json --to concord --out concord-copy.json
moirai continue conversation.moirai --with concord
```

Discovery reads `~/Library/Application Support/dev.october.concord/conversations.json`.
Override the exact file with `CONCORD_CONVERSATIONS_FILE`. Each conversation is a
separate discovered session; a multi-conversation file must not silently convert
only its first conversation. Direct parsing accepts one object or a one-item array;
Go callers can select a multi-item array using `ParseOptions.SourceID`.

**Safe destination handoff:** Moirai writes a new import file under
`~/.moirai/concord-imports` (`MOIRAI_CONCORD_IMPORT_DIR` overrides it). On macOS it
opens Concord's JSON import dialog; `--no-launch` only stages the file. Other OSes
can stage or convert files but cannot launch the macOS app.

This requires the Concord build with **Import chats…** / JSON document handling.
Confirm the import in Concord. Unknown gateways require an explicit provider choice.
Conflicting conversation IDs reject the whole import; identical existing chats are
left unchanged. Moirai never edits or deletes Concord's live database. Its pending
import file is not a claim that the app has already accepted the conversation.

## Conversion boundaries

- **Agent → generic chat:** preserve text, supported image URLs and artifacts;
  omit reasoning/tool/unknown blocks with a path-specific warning. Keep every
  message in order, even when all its blocks are omitted. Agent-only metadata and
  unrepresentable usage fields also generate warnings.
- **Agent → Concord:** preserve text and system roles; flatten reasoning and tool
  calls/results into explicitly labeled text, images into Markdown, and warn about
  every flattened/omitted block and unsupported metadata. No tool is executed.
- **Chat → agent:** preserve supported content without adding tools, reasoning or
  workspace state. System roles are kept in position as labeled user context with
  `system_role_flattened`, because agent instruction semantics are not equivalent.
  Native codecs may require their own record IDs and bookkeeping; this is not
  evidence that those fields were present in the source chat.
  Absent numeric timestamps are written as null, not the current time. A native
  harness may require you to select a model or configure its workspace before
  resuming. Empty messages are not reliably portable to harnesses and are warned
  about; use a chat destination or archive when exact chat retention is required.
- **New Concord records:** Concord requires UUIDs and dates. Missing/unusable IDs
  get deterministic destination UUIDs with warnings. Missing dates use the actual
  import time with `destination_time_created`, not a claimed original creation
  date. Fractional timestamp precision is reduced to Concord's whole-second format
  with a warning. Missing provider/model choices are not fabricated.

The generic and Concord codec/archive round-trips preserve existing chat identity.
`continue` intentionally creates a new destination session, as it does for agents.
No adapter reads local attachment paths, downloads URLs, uploads data, or merges or
rewrites source message text. Concord itself may fetch Markdown images when a user
opens the imported chat. Review untrusted imported content before acting on it.
