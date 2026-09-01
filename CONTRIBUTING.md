# Contributing to Moirai

Thank you for helping make agent sessions portable across tools.

Moirai is in its design stage. Format, privacy, and security decisions made now will be difficult to change later, so please open an issue before starting a large change.

## Good contributions

We welcome:

- portable session and checkpoint schemas;
- safe local storage improvements;
- archive import and export support;
- Go reference implementation work;
- SDKs and harness integrations;
- examples and clear documentation;
- privacy, security, and interoperability improvements.

Keep contributions focused on recording, moving, and continuing agent sessions. Product-specific orchestration and private model reasoning are outside the project's scope.

## Before you start

1. Search existing issues and pull requests.
2. Open an issue for new format fields, public APIs, storage changes, or security-sensitive behavior.
3. Keep the proposal small enough to review and verify.
4. Never include real credentials, private sessions, customer data, or hidden chain-of-thought in examples or tests.

## Development

The reference implementation uses Go 1.25 or newer.

```bash
go test ./...
go vet ./...
```

The TypeScript package is currently prerelease metadata only. Its public API will arrive with the first SDK implementation.

## Pull requests

A good pull request:

- explains the user-facing problem;
- stays focused on one change;
- includes tests for behavior changes;
- updates public documentation or schemas when the format changes;
- preserves backward compatibility once a format version is declared stable;
- passes the repository checks.

Use clear commit messages. Do not include credentials, private data, or unrelated generated files.

## Licensing

By submitting a contribution, you agree that it may be distributed under the [Apache License 2.0](LICENSE).
