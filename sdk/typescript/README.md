# @october-dev/moirai

Typed canonical sessions and portable transcript operations for Moirai.

```ts
import { SimpleCodec, search, toText } from "@october-dev/moirai";

const { transcript, warnings } = new SimpleCodec().parse(source);
console.log(toText(transcript, { includeTools: true }));
console.log(search(transcript, "failing test"));
```

The SDK validates the versioned canonical schema, safety limits, tool-call
pairing, UTF-8-bounded text projection, message ranges, fuzzy search, and
SHA-256 integrity archives. It includes the Claude Code JSONL codec; the other
native harness codecs and local stores are provided only by the Go library and
`moirai` CLI.

Canonical schema `1.1` also represents ordinary chats: `system`, `user`, and
`assistant` messages, with no tools or workspace required. Empty `content: []`
preserves an empty message. Model/provider, timestamps, usage and attachments
are optional; parsing `1.1` never supplies a missing source timestamp. Existing
`1.0` agent documents retain their behavior. The generic chat JSON and Concord
native adapters live in Go/the CLI; use `SimpleCodec` and archives in this SDK
to exchange their canonical documents. See [CHAT.md](../../docs/CHAT.md).
