import { readFile, writeFile } from "node:fs/promises";
import { decodeArchive, encodeArchive, SimpleCodec } from "../../sdk/typescript/dist/index.js";

const [goArchive, typescriptArchive, source] = process.argv.slice(2);
if (!goArchive || !typescriptArchive || !source) throw new Error("usage: archive-interop.mjs <go-archive> <typescript-archive> <source>");

const decoded = await decodeArchive(await readFile(goArchive));
if (decoded.meta.id !== "interop" || decoded.messages.length !== 3) throw new Error("Go archive decoded incorrectly in TypeScript");

const transcript = new SimpleCodec().parse(await readFile(source)).transcript;
await writeFile(typescriptArchive, await encodeArchive(transcript), { mode: 0o600 });
