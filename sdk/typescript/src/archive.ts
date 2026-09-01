import {
  DEFAULT_LIMITS, type Artifact, type Block, type Limits, type MediaSource,
  type Message, MoiraiError, type Provenance, type Transcript, type Usage, validate,
} from "./model.js";

export const ARCHIVE_VERSION = "1" as const;

export interface Archive {
  format: "moirai.session";
  version: typeof ARCHIVE_VERSION;
  created_at: string;
  transcript: Transcript;
  sha256: string;
}

export async function encodeArchive(transcript: Transcript, limits: Limits = { ...DEFAULT_LIMITS }): Promise<string> {
  validate(transcript, limits);
  const normalized = normalizeTranscript(transcript);
  const sha256 = await digest(JSON.stringify(normalized));
  const archive: Archive = { format: "moirai.session", version: ARCHIVE_VERSION, created_at: new Date().toISOString(), transcript: normalized, sha256 };
  return `${JSON.stringify(archive, null, 2)}\n`;
}

export async function decodeArchive(data: string | Uint8Array, limits: Limits = { ...DEFAULT_LIMITS }): Promise<Transcript> {
  const text = typeof data === "string" ? data : new TextDecoder().decode(data);
  if (new TextEncoder().encode(text).length > limits.maxInputBytes) throw new MoiraiError("limit_exceeded", "archive exceeds the safety limit");
  checkJSONDepth(text, limits.maxNestingDepth + 1);
  let value: unknown;
  try { value = JSON.parse(text); } catch (error) { throw new MoiraiError("invalid_transcript", `invalid archive JSON: ${String(error)}`); }
  if (!isRecord(value) || value.format !== "moirai.session" || value.version !== ARCHIVE_VERSION || !isRecord(value.transcript) || typeof value.sha256 !== "string") {
    throw new MoiraiError("unsupported_version", "unsupported session archive");
  }
  const transcript = normalizeTranscript(value.transcript as unknown as Transcript);
  const actual = await digest(JSON.stringify(transcript));
  if (!constantTimeEqual(value.sha256, actual)) throw new MoiraiError("integrity", "archive integrity check failed");
  validate(transcript, limits);
  return transcript;
}

async function digest(value: string): Promise<string> {
  if (!globalThis.crypto?.subtle) throw new MoiraiError("crypto_unavailable", "Web Crypto SHA-256 is required");
  const bytes = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(bytes)].map((part) => part.toString(16).padStart(2, "0")).join("");
}

function constantTimeEqual(left: string, right: string): boolean {
  if (left.length !== right.length) return false;
  let difference = 0;
  for (let index = 0; index < left.length; index += 1) difference |= left.charCodeAt(index) ^ right.charCodeAt(index);
  return difference === 0;
}

// Keep the same field order and omission rules as Go's encoding/json output so
// archives produced by either SDK verify in the other.
function normalizeTranscript(value: Transcript): Transcript {
  const transcript: Transcript = {
    schema_version: value.schema_version,
    meta: orderedMeta(value.meta),
    messages: value.messages.map(orderedMessage),
  };
  if (value.extra !== undefined) transcript.extra = value.extra;
  return transcript;
}

function orderedMeta(value: Transcript["meta"]): Transcript["meta"] {
  const output: Transcript["meta"] = { id: value.id };
  assignString(output, "timestamp", value.timestamp);
  assignString(output, "updated_at", value.updated_at);
  assignString(output, "cwd", value.cwd);
  assignString(output, "git_branch", value.git_branch);
  assignString(output, "title", value.title);
  assignString(output, "model", value.model);
  assignString(output, "cli_version", value.cli_version);
  if (value.provenance) output.provenance = orderedProvenance(value.provenance);
  if (value.extra !== undefined) output.extra = value.extra;
  return output;
}

function orderedProvenance(value: Provenance): Provenance {
  const output: Provenance = {};
  assignString(output, "source_format", value.source_format);
  assignString(output, "source_session_id", value.source_session_id);
  assignString(output, "imported_at", value.imported_at);
  assignString(output, "parent_session_id", value.parent_session_id);
  assignString(output, "parent_checkpoint", value.parent_checkpoint);
  return output;
}

function orderedMessage(value: Message): Message {
  const output: Message = { role: value.role, content: value.content.map(orderedBlock) };
  const reordered: Message = {} as Message;
  assignString(reordered, "id", value.id);
  reordered.role = output.role;
  reordered.content = output.content;
  assignString(reordered, "timestamp", value.timestamp);
  assignString(reordered, "model", value.model);
  if (value.usage) reordered.usage = orderedUsage(value.usage);
  assignString(reordered, "stop_reason", value.stop_reason);
  if (value.extra !== undefined) reordered.extra = value.extra;
  return reordered;
}

function orderedUsage(value: Usage): Usage {
  const output: Usage = { input_tokens: value.input_tokens, output_tokens: value.output_tokens };
  if (value.cache_read_input_tokens) output.cache_read_input_tokens = value.cache_read_input_tokens;
  if (value.cache_creation_input_tokens) output.cache_creation_input_tokens = value.cache_creation_input_tokens;
  return output;
}

function orderedBlock(value: Block): Block {
  const output: Block = { type: value.type };
  assignString(output, "text", value.text);
  assignString(output, "id", value.id);
  assignString(output, "name", value.name);
  if (value.input !== undefined) output.input = value.input;
  assignString(output, "tool_use_id", value.tool_use_id);
  if (value.content !== undefined) output.content = value.content;
  if (value.is_error) output.is_error = true;
  if (value.source) output.source = orderedMedia(value.source);
  if (value.artifact) output.artifact = orderedArtifact(value.artifact);
  if (value.data !== undefined) output.data = value.data;
  assignString(output, "signature", value.signature);
  assignString(output, "encrypted", value.encrypted);
  return output;
}

function orderedMedia(value: MediaSource): MediaSource {
  const output: MediaSource = { type: value.type };
  assignString(output, "media_type", value.media_type);
  assignString(output, "data", value.data);
  assignString(output, "path", value.path);
  assignString(output, "url", value.url);
  return output;
}

function orderedArtifact(value: Artifact): Artifact {
  const output: Artifact = { name: value.name };
  const reordered: Artifact = {} as Artifact;
  assignString(reordered, "id", value.id);
  reordered.name = output.name;
  assignString(reordered, "description", value.description);
  assignString(reordered, "media_type", value.media_type);
  if (value.source) reordered.source = orderedMedia(value.source);
  assignString(reordered, "sha256", value.sha256);
  return reordered;
}

function assignString<T extends object, K extends keyof T>(target: T, key: K, value: T[K] | undefined): void {
  if (typeof value === "string" && value) target[key] = value;
}
function isRecord(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }

function checkJSONDepth(value: string, maximum: number): void {
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (const character of value) {
    if (inString) {
      if (escaped) escaped = false;
      else if (character === "\\") escaped = true;
      else if (character === '"') inString = false;
    } else if (character === '"') inString = true;
    else if (character === "{" || character === "[") {
      depth += 1;
      if (depth > maximum) throw new MoiraiError("limit_exceeded", "JSON nesting exceeds the safety limit");
    } else if (character === "}" || character === "]") depth = Math.max(0, depth - 1);
  }
}
