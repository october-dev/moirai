# Contributing to Moirai

Thank you for helping make agent sessions portable across tools.

## Before you start

1. Search existing issues and pull requests.
2. Open an issue before changing schema `1.0`, a public API, a native store
   layout, or security-sensitive behavior.
3. Never include real credentials, private sessions, customer data, cookies,
   account databases, or hidden model reasoning in fixtures.

## Compatibility rules

- A native reader must be tested with a small, synthetic fixture shaped like
  the harness output—not only with output from its paired writer.
- A writable adapter must pass canonical → native → canonical round trips for
  text, tool calls/results, timestamps, metadata, and supported media.
- Unsupported native records produce a warning or an `unknown` block. Do not
  silently discard data and call the conversion lossless.
- Source-only stores must return `ErrSourceOnly` for mutation attempts.
- A new handoff must mint a new ID and retain source provenance.
- Range selection must never separate a known tool call from its result.
- Store operations must reject absolute paths, traversal, and symlinks.

## Development

The reference implementation requires Go 1.25.8. The TypeScript SDK requires
Node.js 20 or newer.

```bash
go test -race ./...
go vet ./...
go build ./cmd/moirai
npm --prefix sdk/typescript ci
npm --prefix sdk/typescript test
npm --prefix sdk/typescript pack --dry-run
```

When changing the canonical model, update all of the following in the same pull
request:

- Go structs and validation;
- TypeScript types and validation;
- `schema/moirai-session.schema.json`;
- `docs/FORMAT.md`;
- cross-language archive fixtures and tests, where applicable.

## Pull requests

Explain the user-facing problem, format/store assumptions, expected information
loss, and verification performed. Keep generated build output and dependencies
out of commits. By contributing, you agree that your work may be distributed
under the [Apache License 2.0](LICENSE).
