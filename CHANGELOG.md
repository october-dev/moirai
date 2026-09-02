# Changelog

All notable changes are documented here. Moirai follows semantic versioning for
the Go and TypeScript APIs; the portable document version is declared separately
by `schema_version`.

## 0.1.2

- Match Claude Code's project-directory encoding for every non-alphanumeric
  character and honor `CLAUDE_CONFIG_DIR`.
- Match ECMAScript shortest-form number serialization across Go and TypeScript,
  including rounding ties.
- Recover abandoned session reservation locks without weakening active-write
  collision protection.
- Add the Omarchy bar-widget plugin for local session discovery and handoff.
- Add static native-shaped reader fixtures, cross-SDK archive checks, security
  regression tests, and automated release gates.

## 0.1.1

- Exclude Claude Code subagent side-files from session discovery.
- Make archive verification canonical and interoperable across Go and
  TypeScript, with strict rejection of unknown archive fields.
- Harden Codex conversion, terminal output, ranges, large-session discovery,
  concurrent writes, Cursor SQLite matching, and third-party database access.

## 0.1.0

- Add the canonical session model and schema `1.0`.
- Add native codecs for 16 local and exported conversation formats.
- Add guarded local file, bundle, and SQLite session stores.
- Add cross-harness import, continuation, native launch, discovery, search,
  range selection, deletion, conversion, and archive commands.
- Add SHA-256 verified portable archives.
- Add the production TypeScript model, codec, archive, search, text, and range
  APIs.
