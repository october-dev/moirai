import { createRequire } from "node:module";
import { readFile } from "node:fs/promises";

const require = createRequire(new URL("../../sdk/typescript/package.json", import.meta.url));
const Ajv2020 = require("ajv/dist/2020").default;
const addFormats = require("ajv-formats");

const schema = JSON.parse(await readFile(new URL("../../schema/moirai-session.schema.json", import.meta.url), "utf8"));
const ajv = new Ajv2020({ allErrors: true, strict: true, strictRequired: false });
addFormats(ajv);
const validate = ajv.compile(schema);

for (const name of ["session-v1.json", "archive-interop-session.json"]) {
  const value = JSON.parse(await readFile(new URL(`../../testdata/${name}`, import.meta.url), "utf8"));
  if (!validate(value)) throw new Error(`${name}: ${ajv.errorsText(validate.errors, { separator: "\n" })}`);
}
