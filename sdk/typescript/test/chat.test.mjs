import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { SimpleCodec, ClaudeCodeCodec, validate, encodeArchive, decodeArchive, toText } from "../dist/index.js";

test("canonical plain chat preserves system roles, empty messages and absent timestamps", async () => {
  const original = { schema_version: "1.1", meta: { id: "chat", model: "model", model_provider: "provider" }, messages: [
    { role: "system", content: [{ type: "text", text: "Be concise." }] },
    { role: "user", content: [] },
    { role: "assistant", content: [{ type: "text", text: "Hi" }], usage: { input_tokens: 1, output_tokens: 1 } },
  ] };
  const codec = new SimpleCodec();
  const parsed = codec.parse(JSON.stringify(original));
  assert.deepEqual(parsed.transcript, original);
  assert.deepEqual(await decodeArchive(await encodeArchive(parsed.transcript)), original);
  assert.throws(() => validate({ ...original, schema_version: "1.0" }), /role/);
  const rendered = new ClaudeCodeCodec().render(original);
  assert(rendered.warnings.some(w => w.code === "system_role_flattened"));
  assert.equal(original.messages[0].role, "system");
  assert.equal(toText(original), "System [1]: Be concise.\n\nAssistant [3]: Hi");
  assert.throws(() => codec.parse(JSON.stringify({ ...original, accidental: "not an extension" })), /unknown/);
  const ajv = new Ajv2020({ strict: false });
  addFormats(ajv);
  const schema = JSON.parse(await readFile(new URL("../../../schema/moirai-session.schema.json", import.meta.url), "utf8"));
  const check = ajv.compile(schema);
  assert(check(original), JSON.stringify(check.errors));
  assert.equal(check({ ...original, schema_version: "1.0" }), false);
});
