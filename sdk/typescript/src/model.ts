export const SCHEMA_VERSION = "1.0" as const;

export const FORMATS = [
  "simple", "claude_code", "codex", "pi", "amp", "opencode", "cursor",
  "cursor_desktop", "grok", "hermes", "antigravity", "campfire", "cowork",
  "fx", "claude_chat", "chatgpt",
] as const;

export type Format = (typeof FORMATS)[number];
export type Role = "user" | "assistant";
export type BlockType = "text" | "thinking" | "tool_use" | "tool_result" | "image" | "artifact" | "unknown";

export interface MediaSource { type: string; media_type?: string; data?: string; path?: string; url?: string }
export interface Artifact { id?: string; name: string; description?: string; media_type?: string; source?: MediaSource; sha256?: string }
export interface Block {
  type: BlockType; text?: string; id?: string; name?: string; input?: unknown;
  tool_use_id?: string; content?: unknown; is_error?: boolean; source?: MediaSource;
  artifact?: Artifact; data?: unknown; signature?: string; encrypted?: string;
}
export interface Usage { input_tokens: number; output_tokens: number; cache_read_input_tokens?: number; cache_creation_input_tokens?: number }
export interface Message {
  id?: string; role: Role; content: Block[]; timestamp?: string; model?: string;
  usage?: Usage; stop_reason?: string; extra?: unknown;
}
export interface Provenance {
  source_format?: Format; source_session_id?: string; imported_at?: string;
  parent_session_id?: string; parent_checkpoint?: string;
}
export interface Metadata {
  id: string; timestamp?: string; updated_at?: string; cwd?: string; git_branch?: string;
  title?: string; model?: string; cli_version?: string; provenance?: Provenance; extra?: unknown;
}
export interface Transcript { schema_version: typeof SCHEMA_VERSION; meta: Metadata; messages: Message[]; extra?: unknown }
export interface Warning { path?: string; code: string; message: string }
export interface ParseResult { transcript: Transcript; warnings: Warning[] }
export interface Limits {
  maxInputBytes: number; maxMessages: number; maxBlocks: number; maxTextBytes: number;
  maxInlineMediaBytes: number; maxMetadataBytes: number; maxNestingDepth: number;
}

export const DEFAULT_LIMITS: Readonly<Limits> = Object.freeze({
  maxInputBytes: 32 << 20, maxMessages: 100_000, maxBlocks: 500_000,
  maxTextBytes: 1 << 20, maxInlineMediaBytes: 16 << 20,
  maxMetadataBytes: 1 << 20, maxNestingDepth: 64,
});

export class MoiraiError extends Error {
  constructor(readonly code: string, message: string) { super(message); this.name = "MoiraiError"; }
}

export function newId(): string {
  if (typeof globalThis.crypto?.randomUUID === "function") return globalThis.crypto.randomUUID();
  throw new MoiraiError("crypto_unavailable", "crypto.randomUUID is required");
}

export function validate(transcript: Transcript, limits: Limits = { ...DEFAULT_LIMITS }): void {
  if (!transcript || transcript.schema_version !== SCHEMA_VERSION) throw new MoiraiError("unsupported_version", `schema_version must be ${SCHEMA_VERSION}`);
  if (!transcript.meta?.id?.trim()) throw new MoiraiError("invalid_transcript", "meta.id is required");
  ensureNesting(transcript, limits.maxNestingDepth);
  let totalBytes = enforceStrings("meta", [transcript.meta.id, transcript.meta.timestamp, transcript.meta.updated_at, transcript.meta.cwd, transcript.meta.git_branch, transcript.meta.title, transcript.meta.model, transcript.meta.cli_version], limits.maxMetadataBytes);
  validateTime("meta.timestamp", transcript.meta.timestamp);
  validateTime("meta.updated_at", transcript.meta.updated_at);
  enforceJSONBytes("meta.extra", transcript.meta.extra, limits.maxMetadataBytes);
  enforceJSONBytes("extra", transcript.extra, limits.maxMetadataBytes);
  totalBytes += jsonByteLength(transcript.meta.extra) + jsonByteLength(transcript.extra);
  if (!Array.isArray(transcript.messages) || transcript.messages.length > limits.maxMessages) throw new MoiraiError("limit_exceeded", "message count exceeds the safety limit");
  const calls = new Set<string>();
  let blockCount = 0;
  transcript.messages.forEach((message, mi) => {
    if (message.role !== "user" && message.role !== "assistant") throw new MoiraiError("invalid_transcript", `messages[${mi}].role is invalid`);
    if (!Array.isArray(message.content)) throw new MoiraiError("invalid_transcript", `messages[${mi}].content must be an array`);
    validateTime(`messages[${mi}].timestamp`, message.timestamp);
    enforceJSONBytes(`messages[${mi}].extra`, message.extra, limits.maxMetadataBytes);
    totalBytes += enforceStrings(`messages[${mi}]`, [message.id, message.timestamp, message.model, message.stop_reason], limits.maxMetadataBytes) + jsonByteLength(message.extra);
    if (message.usage) validateUsage(`messages[${mi}].usage`, message.usage);
    blockCount += message.content.length;
    if (blockCount > limits.maxBlocks) throw new MoiraiError("limit_exceeded", "block count exceeds the safety limit");
    message.content.forEach((block, bi) => {
      const path = `messages[${mi}].content[${bi}]`;
      if (byteLength(block.text ?? "") > limits.maxTextBytes) throw new MoiraiError("limit_exceeded", `${path}.text is too large`);
      totalBytes += blockPayloadBytes(block);
      if (totalBytes > limits.maxInputBytes) throw new MoiraiError("limit_exceeded", "aggregate transcript payload exceeds the safety limit");
      switch (block.type) {
        case "text": case "thinking":
          if (!block.text && !block.encrypted) throw new MoiraiError("invalid_transcript", `${path} has no content`);
          break;
        case "tool_use":
          if (!block.id || !block.name) throw new MoiraiError("invalid_transcript", `${path} requires id and name`);
          enforceStrings(path, [block.id, block.name], limits.maxMetadataBytes);
          enforceJSONBytes(`${path}.input`, block.input, limits.maxTextBytes);
          if (calls.has(block.id)) throw new MoiraiError("invalid_transcript", `duplicate tool id ${block.id}`);
          calls.add(block.id); break;
        case "tool_result":
          enforceStrings(path, [block.tool_use_id], limits.maxMetadataBytes);
          enforceJSONBytes(`${path}.content`, block.content, limits.maxTextBytes);
          if (block.tool_use_id && !calls.has(block.tool_use_id)) throw new MoiraiError("invalid_transcript", `${path} references an unknown tool`);
          break;
        case "image":
          if (!block.source?.type) throw new MoiraiError("invalid_transcript", `${path}.source is required`);
          if (block.source.type === "base64" && block.source.data !== undefined) validateBase64(`${path}.source.data`, block.source.data, limits.maxInlineMediaBytes);
          break;
        case "artifact":
          if (!block.artifact?.name) throw new MoiraiError("invalid_transcript", `${path}.artifact.name is required`);
          enforceStrings(`${path}.artifact`, [block.artifact.id, block.artifact.name, block.artifact.description, block.artifact.media_type, block.artifact.sha256], limits.maxMetadataBytes);
          if (block.artifact.sha256 && !/^[0-9a-fA-F]{64}$/u.test(block.artifact.sha256)) throw new MoiraiError("invalid_transcript", `${path}.artifact.sha256 must be a SHA-256 hex digest`);
          if (block.artifact.source) {
            if (!block.artifact.source.type) throw new MoiraiError("invalid_transcript", `${path}.artifact.source.type is required`);
            if (block.artifact.source.type === "base64" && block.artifact.source.data !== undefined) validateBase64(`${path}.artifact.source.data`, block.artifact.source.data, limits.maxInlineMediaBytes);
          }
          break;
        case "unknown":
          if (block.data === undefined) throw new MoiraiError("invalid_transcript", `${path}.data is required`);
          enforceJSONBytes(`${path}.data`, block.data, limits.maxTextBytes);
          break;
        default: throw new MoiraiError("invalid_transcript", `${path}.type is invalid`);
      }
    });
  });
}

function validateTime(path: string, value: string | undefined): void {
  if (value && !isRFC3339(value)) {
    throw new MoiraiError("invalid_transcript", `${path} must be RFC 3339`);
  }
}

function isRFC3339(value: string): boolean {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|([+-])(\d{2}):(\d{2}))$/u.exec(value);
  if (!match) return false;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  const offsetHour = Number(match[9] ?? 0);
  const offsetMinute = Number(match[10] ?? 0);
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return month >= 1 && month <= 12 && day >= 1 && day <= days[month - 1]! && hour <= 23 && minute <= 59 && second <= 59 && offsetHour <= 23 && offsetMinute <= 59;
}

function byteLength(value: string): number { return new TextEncoder().encode(value).length; }

function enforceJSONBytes(path: string, value: unknown, maximum: number): void {
  if (value === undefined) return;
  let encoded: string | undefined;
  try { encoded = JSON.stringify(value); } catch { throw new MoiraiError("invalid_transcript", `${path} must be JSON`); }
  if (encoded === undefined) throw new MoiraiError("invalid_transcript", `${path} must be JSON`);
  if (byteLength(encoded) > maximum) throw new MoiraiError("limit_exceeded", `${path} is too large`);
}

function jsonByteLength(value: unknown): number {
  if (value === undefined) return 0;
  try { const encoded = JSON.stringify(value); return encoded === undefined ? 0 : byteLength(encoded); } catch { return 0; }
}

function enforceStrings(path: string, values: Array<string | undefined>, maximum: number): number {
  let total = 0;
  for (const value of values) {
    if (value === undefined) continue;
    const size = byteLength(value);
    if (size > maximum) throw new MoiraiError("limit_exceeded", `${path} contains oversized metadata`);
    total += size;
  }
  return total;
}

function validateUsage(path: string, usage: Usage): void {
  for (const [name, value] of Object.entries(usage)) {
    if (!Number.isSafeInteger(value) || value < 0) throw new MoiraiError("invalid_transcript", `${path}.${name} must be a nonnegative safe integer`);
  }
  if (!Number.isSafeInteger(usage.input_tokens) || !Number.isSafeInteger(usage.output_tokens)) throw new MoiraiError("invalid_transcript", `${path} requires token counts`);
}

function blockPayloadBytes(block: Block): number {
  let total = enforceStrings("block", [block.text, block.id, block.name, block.tool_use_id, block.signature, block.encrypted], Number.MAX_SAFE_INTEGER);
  total += jsonByteLength(block.input) + jsonByteLength(block.content) + jsonByteLength(block.data);
  if (block.source) total += enforceStrings("source", [block.source.type, block.source.media_type, block.source.data, block.source.path, block.source.url], Number.MAX_SAFE_INTEGER);
  if (block.artifact) {
    total += enforceStrings("artifact", [block.artifact.id, block.artifact.name, block.artifact.description, block.artifact.media_type, block.artifact.sha256], Number.MAX_SAFE_INTEGER);
    if (block.artifact.source) total += enforceStrings("artifact.source", [block.artifact.source.type, block.artifact.source.media_type, block.artifact.source.data, block.artifact.source.path, block.artifact.source.url], Number.MAX_SAFE_INTEGER);
  }
  return total;
}

function ensureNesting(root: unknown, maximum: number): void {
  const stack: Array<{ value: unknown; depth: number }> = [{ value: root, depth: 1 }];
  const seen = new Set<object>();
  while (stack.length) {
    const current = stack.pop()!;
    if (current.value === null || typeof current.value !== "object") continue;
    if (seen.has(current.value)) throw new MoiraiError("invalid_transcript", "transcript contains a cycle");
    seen.add(current.value);
    if (current.depth > maximum) throw new MoiraiError("limit_exceeded", "JSON nesting exceeds the safety limit");
    for (const child of Object.values(current.value)) stack.push({ value: child, depth: current.depth + 1 });
  }
}

function validateBase64(path: string, value: string, maximum: number): void {
  const compact = value.replace(/\s/gu, "");
  if (compact.length % 4 !== 0 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(compact)) {
    throw new MoiraiError("invalid_transcript", `${path} must be valid base64`);
  }
  const padding = compact.endsWith("==") ? 2 : compact.endsWith("=") ? 1 : 0;
  if ((compact.length / 4) * 3 - padding > maximum) throw new MoiraiError("limit_exceeded", `${path} is too large`);
}
