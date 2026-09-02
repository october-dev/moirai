import { readFile, writeFile } from "node:fs/promises";
import { decodeArchive, encodeArchive, SimpleCodec } from "../../sdk/typescript/dist/index.js";

const [goArchive, typescriptArchive, source] = process.argv.slice(2);
if (!goArchive || !typescriptArchive || !source) throw new Error("usage: archive-interop.mjs <go-archive> <typescript-archive> <source>");

const transcript = new SimpleCodec().parse(await readFile(source)).transcript;
const decoded = await decodeArchive(await readFile(goArchive));
if (decoded.meta.id !== transcript.meta.id || decoded.messages.length !== transcript.messages.length) {
  throw new Error("Go archive decoded incorrectly in TypeScript");
}
await writeFile(typescriptArchive, await encodeArchive(transcript), { mode: 0o600 });
