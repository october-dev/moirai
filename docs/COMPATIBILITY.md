# Native compatibility boundary

Moirai converts the durable conversation subset represented by schema `1.0`.
Native session files contain additional runtime and UI state that another
harness cannot safely consume. Conversion is semantic, not byte-for-byte.

Every conversion result includes warnings for unsupported canonical blocks and
known source omissions. Applications must display them. Native readers do not
currently populate `extra` or `unknown` for all discarded fields.

| Format | Portable context | Not carried to other harnesses |
| --- | --- | --- |
| Claude Code | user/assistant text, signed or redacted thinking, tool calls/results, images, model, usage, title and workspace | file snapshots, progress/system/queue records, parent graph, request/user/agent bookkeeping, inline and file-based sidechains |
| Codex | conversational response items, reasoning, calls/results, model and workspace | setup-policy messages, token-count events, compaction state, full turn policy/context, duplicate display events |
| Pi and Campfire | messages, thinking, calls/results, model changes, title and workspace | provider runtime state, cost accounting, arbitrary custom records other than portable text |
| Amp | messages, thinking, calls/results, images, model, usage and workspace | client UI state, environment details outside the first workspace, run bookkeeping |
| OpenCode | text, images, reasoning, calls/results, model, usage and workspace | synthetic parts, step lifecycle details, provider-specific metadata |
| Cursor Agent | text, reasoning, calls/results, model, title and workspace | blob graph identity, UI state, approval policy and execution-runtime state |
| Cursor desktop | text, thinking, calls/results, model, title and workspace | composer UI state, checkpoints, auxiliary key/value records and approval state |
| Grok CLI | text, reasoning, calls/results, model, title and workspace | streaming update detail, prompt counters and UI/runtime metadata |
| Hermes Agent | text, reasoning, calls/results, model, title and workspace | effect/runtime bookkeeping and inactive rows |
| Antigravity CLI | text, signed thinking, calls/results, images, usage and workspace | trajectory renderer state, permissions, executor metadata and non-conversational steps |
| Claude Cowork | embedded Claude conversation plus session title/workspace | audit events, uploads, outputs, process/runtime state and sidechains |
| fx | committed user/assistant turns and calls/results | authority/commit internals, file execution state and non-history events |
| Claude Chat export | active-branch messages, calls/results, reasoning, model and title | account/project state, attachments without portable media, alternate branches |
| ChatGPT export | active-branch text, calls/results, reasoning, model and title | hidden UI messages, alternate branches, citations and product-specific metadata |
| Simple JSON | every schema `1.0` field | unknown object fields are rejected by archives; unsupported native targets warn on omission |

Images are carried only when the destination has a compatible URL or media
representation. Plain thinking without a native signature is converted to
visible assistant text for Claude Code; redacted thinking remains redacted.

Cursor desktop and Cowork can receive imported session data, but their public
launch commands only open the application, not a specific imported session.
They are therefore not advertised as session-specific continuation targets.
