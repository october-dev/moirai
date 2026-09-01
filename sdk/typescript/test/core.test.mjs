import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  decodeArchive, encodeArchive, MoiraiError, parseSelector, search, select,
  SimpleCodec, toText,
} from "../dist/index.js";

const source = JSON.stringify({
  id: "session",
  timestamp: "2026-01-01T00:00:00Z",
  cwd: "/tmp/work",
  messages: [
    { role: "user", content: "repair the parser" },
    { role: "assistant", content: [{ type: "thinking", text: "inspect first" }, { type: "tool_use", id: "call-1", name: "Read", input: { file_path: "parser.go" } }] },
    { role: "user", content: [{ type: "tool_result", tool_use_id: "call-1", content: "package parser" }] },
    { role: "assistant", content: "Parser repaired and tests pass." },
  ],
});

function fixture() { return new SimpleCodec().parse(source).transcript; }

test("simple codec round-trips and pairs tools", () => {
  const codec = new SimpleCodec();
  const parsed = codec.parse(JSON.stringify({ messages: [
    { role: "assistant", content: [{ type: "tool_use", name: "Read", input: {} }] },
    { role: "user", content: [{ type: "tool_result", content: "ok" }] },
  ] }));
  assert.equal(parsed.transcript.messages[1].content[0].tool_use_id, parsed.transcript.messages[0].content[0].id);
  assert.equal(codec.parse(codec.render(parsed.transcript)).transcript.messages.length, 2);
});

test("simple codec accepts and emits canonical metadata", () => {
  const codec = new SimpleCodec();
  const parsed = codec.parse(JSON.stringify({ schema_version: "1.0", meta: { id: "canonical", cwd: "/repo", extra: { source: true } }, messages: [{ role: "user", content: "hello" }] }));
  assert.equal(parsed.transcript.meta.id, "canonical");
  assert.equal(parsed.transcript.meta.cwd, "/repo");
  const rendered = JSON.parse(codec.render(parsed.transcript));
  assert.equal(rendered.meta.id, "canonical");
  assert.equal(rendered.id, undefined);
});

test("selectors and ranges preserve tool boundaries", () => {
  assert.deepEqual(parseSelector("session#2-3"), { sessionId: "session", span: { start: 2, end: 3 } });
  const selected = select(fixture(), { start: 2, end: 3 });
  assert.equal(selected.messages.length, 2);
  assert.equal(selected.meta.provenance.parent_session_id, "session");
  assert.throws(() => select(fixture(), { start: 2, end: 2 }), MoiraiError);
});

test("bounded text and fuzzy search", () => {
  const text = toText(fixture(), { maxBytes: 90, includeTools: true, includeMetadata: true });
  assert.ok(Buffer.byteLength(text) <= 90);
  assert.match(text, /^\[earlier context omitted\]/);
  const hits = search(fixture(), "parser repaired", 10);
  assert.equal(hits.length, 1);
  assert.equal(hits[0].messageIndex, 4);
});

test("archives detect tampering", async () => {
  const encoded = await encodeArchive(fixture());
  assert.equal((await decodeArchive(encoded)).meta.id, "session");
  const corrupt = encoded.replace("repair the parser", "replace the parser");
  await assert.rejects(() => decodeArchive(corrupt), (error) => error instanceof MoiraiError && error.code === "integrity");
});

test("decodes the language-independent archive fixture", async () => {
  const fixture = await readFile("../../testdata/archive-v1.moirai");
  const transcript = await decodeArchive(fixture);
  assert.equal(transcript.meta.id, "interop");
  assert.equal(transcript.messages.length, 1);
});

test("limits reject deep JSON and oversized UTF-8 text", () => {
  const codec = new SimpleCodec();
  assert.throws(() => codec.parse(source, { limits: {
    maxInputBytes: 1 << 20,
    maxMessages: 100,
    maxBlocks: 100,
    maxTextBytes: 1 << 20,
    maxInlineMediaBytes: 1 << 20,
    maxMetadataBytes: 1 << 20,
    maxNestingDepth: 2,
  } }), (error) => error instanceof MoiraiError && error.code === "limit_exceeded");
  const transcript = fixture();
  transcript.messages[0].content[0].text = "😀";
  assert.throws(() => new SimpleCodec().render(transcript, {
    maxInputBytes: 1 << 20,
    maxMessages: 100,
    maxBlocks: 100,
    maxTextBytes: 3,
    maxInlineMediaBytes: 1 << 20,
    maxMetadataBytes: 1 << 20,
    maxNestingDepth: 64,
  }), (error) => error instanceof MoiraiError && error.code === "limit_exceeded");
});

test("validation rejects invalid timestamps, usage, and aggregate payloads", () => {
  const invalidTime = fixture();
  invalidTime.meta.timestamp = "January 1, 2026";
  assert.throws(() => new SimpleCodec().render(invalidTime), (error) => error instanceof MoiraiError && error.code === "invalid_transcript");
  invalidTime.meta.timestamp = "2026-02-31T00:00:00Z";
  assert.throws(() => new SimpleCodec().render(invalidTime), (error) => error instanceof MoiraiError && error.code === "invalid_transcript");

  const invalidUsage = fixture();
  invalidUsage.messages[1].usage = { input_tokens: -1, output_tokens: 1 };
  assert.throws(() => new SimpleCodec().render(invalidUsage), (error) => error instanceof MoiraiError && error.code === "invalid_transcript");

  const limits = {
    maxInputBytes: 10,
    maxMessages: 100,
    maxBlocks: 100,
    maxTextBytes: 1 << 20,
    maxInlineMediaBytes: 1 << 20,
    maxMetadataBytes: 1 << 20,
    maxNestingDepth: 64,
  };
  assert.throws(() => new SimpleCodec().render(fixture(), limits), (error) => error instanceof MoiraiError && error.code === "limit_exceeded");
});
