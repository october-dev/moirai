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
SHA-256 integrity archives. It does not include native harness codecs or local
stores; those are provided only by the Go library and `moirai` CLI.
