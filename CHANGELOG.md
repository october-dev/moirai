# Changelog

All notable changes are documented here. Moirai follows semantic versioning for
the Go and TypeScript APIs; the portable document version is declared separately
by `schema_version`.

## 0.1.1

- Correct Claude Code project paths and exclude subagent side-files from
  discovery.
- Make archive verification canonical and interoperable across Go and
  TypeScript, with strict rejection of unknown archive fields.
- Harden Codex conversion, terminal output, ranges, large-session discovery,
  concurrent writes, Cursor SQLite matching, and third-party database access.
- Add static native-shaped reader fixtures, cross-SDK archive checks, security
  regression tests, and automated release gates.

## 0.1.0

- Add the canonical session model and schema `1.0`.
- Add native codecs for 16 local and exported conversation formats.
- Add guarded local file, bundle, and SQLite session stores.
- Add cross-harness import, continuation, native launch, discovery, search,
  range selection, deletion, conversion, and archive commands.
- Add SHA-256 verified portable archives.
- Add the production TypeScript model, codec, archive, search, text, and range
  APIs.
