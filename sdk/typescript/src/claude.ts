import {
  DEFAULT_LIMITS, type Block, type Limits, type MediaSource, type Message, MoiraiError, newId,
  type ParseResult, type RenderResult, SCHEMA_VERSION, type Transcript, type Usage, validate, type Warning,
} from "./model.js";
import type { Codec, ParseOptions } from "./simple.js";

const KNOWN_BLOCKS = new Set(["text", "thinking", "redacted_thinking", "tool_use", "tool_result", "image"]);
const BOOKKEEPING_FIELDS = ["isMeta", "userType", "requestId", "agentId", "toolUseResult"];

export class ClaudeCodeCodec implements Codec {
  readonly format = "claude_code";

  parse(data: string | Uint8Array, options: ParseOptions = {}): ParseResult {
    const text = typeof data === "string" ? data : new TextDecoder().decode(data);
    const limits = options.limits ?? { ...DEFAULT_LIMITS };
    if (new TextEncoder().encode(text).length > limits.maxInputBytes) throw new MoiraiError("limit_exceeded", "input exceeds the safety limit");
    const warnings: Warning[] = [];
    const records: Array<Record<string, unknown>> = [];
    let lineNumber = 0;
    for (const line of text.split("\n")) {
      lineNumber += 1;
      if (!line.trim()) continue;
      let record: unknown;
      try {
        checkJSONDepth(line, limits.maxNestingDepth);
        record = JSON.parse(line);
      } catch (error) {
        if (error instanceof MoiraiError) throw error;
        warnings.push({ path: `line ${lineNumber}`, code: "invalid_json", message: "record omitted" });
        continue;
      }
      if (!isRecord(record)) { warnings.push({ path: `line ${lineNumber}`, code: "invalid_json", message: "record omitted" }); continue; }
      records.push(record);
    }
    const meta: { id: string; timestamp?: string; cwd?: string; git_branch?: string; title?: string; model?: string; cli_version?: string } = { id: "" };
    const messages: Message[] = [];
    const pending: string[] = [];
    const omittedKinds = new Set<string>();
    let omittedFields = false;
    for (const record of records) {
      const kind = stringValue(record.type);
      if (kind === "summary") {
        const summary = stringValue(record.summary);
        if (summary && !meta.title) meta.title = summary;
        continue;
      }
      if (kind !== "user" && kind !== "assistant") {
        if (kind) omittedKinds.add(kind);
        continue;
      }
      if (record.isSidechain === true) {
        omittedKinds.add("sidechain");
        continue;
      }
      if (BOOKKEEPING_FIELDS.some((field) => record[field] !== undefined && record[field] !== null)) omittedFields = true;
      const sessionID = stringValue(record.sessionId); if (sessionID && !meta.id) meta.id = sessionID;
      const cwd = stringValue(record.cwd); if (cwd && !meta.cwd) meta.cwd = cwd;
      const branch = stringValue(record.gitBranch); if (branch && !meta.git_branch) meta.git_branch = branch;
      const version = stringValue(record.version); if (version && !meta.cli_version) meta.cli_version = version;
      const stamp = timestampValue(record.timestamp);
      if (stamp && !meta.timestamp) meta.timestamp = stamp;
      const payload = isRecord(record.message) ? record.message : {};
      const blocks = parseAnthropicContent(payload.content, messages.length, pending, warnings);
      if (!blocks.length) continue;
      const payloadRole = stringValue(payload.role);
      const message: Message = { role: payloadRole === "user" || payloadRole === "assistant" ? payloadRole : kind, content: blocks };
      const uuid = stringValue(record.uuid); if (uuid) message.id = uuid;
      if (stamp) message.timestamp = stamp;
      const model = stringValue(payload.model); if (model) message.model = model;
      const stopReason = stringValue(payload.stop_reason); if (stopReason) message.stop_reason = stopReason;
      const usage = usageFromMap(payload.usage); if (usage) message.usage = usage;
      messages.push(message);
      if (message.model && !meta.model) meta.model = message.model;
    }
    for (const kind of omittedKinds) warnings.push({ code: "native_record_omitted", message: `Claude Code ${kind} record omitted from portable context` });
    if (omittedFields) warnings.push({ code: "native_fields_omitted", message: "Claude Code bookkeeping fields omitted from portable context" });
    if (!messages.length) throw new MoiraiError("invalid_transcript", "no conversational records");
    if (!meta.id) meta.id = options.sourceId || newId();
    if (!meta.timestamp) meta.timestamp = options.now?.() || new Date().toISOString();
    const transcript: Transcript = { schema_version: SCHEMA_VERSION, meta, messages };
    let last = meta.timestamp;
    for (const message of transcript.messages) {
      if (!message.timestamp && last) message.timestamp = last;
      else last = message.timestamp ?? "";
    }
    validate(transcript, limits);
    return { transcript, warnings };
  }

  render(transcript: Transcript, limits: Limits = { ...DEFAULT_LIMITS }): RenderResult {
    validate(transcript, limits);
    if (transcript.messages.some(m => m.role === "system")) {
      const warnings: Warning[] = [];
      const messages = transcript.messages.map((m, i): Message => {
        if (m.role !== "system") return m;
        warnings.push({ path: `messages[${i}].role`, code: "system_role_flattened", message: "System message preserved as labelled user context; destination instruction semantics are not equivalent" });
        return { ...m, role: "user", content: [{ type: "text", text: "[System message from source chat]" }, ...m.content] };
      });
      const result = this.render({ ...transcript, messages }, limits);
      return { data: result.data, warnings: [...warnings, ...result.warnings] };
    }
    const sessionID = transcript.meta.id;
    const lines: string[] = [];
    let parent = "";
    let last = "";
    transcript.messages.forEach((message, index) => {
      const id = message.id || seededUUID(sessionID, String(index), message.role);
      const payload: Record<string, unknown> = { role: message.role, content: renderContent(message.content) };
      if (message.role === "assistant") {
        payload.id = `msg_${seededHash(sessionID, String(index))}`;
        payload.type = "message";
        payload.model = firstNonEmpty(message.model, transcript.meta.model, "unknown");
        let stopReason = message.stop_reason ?? "";
        if (!stopReason && message.content.some((block) => block.type === "tool_use")) stopReason = "tool_use";
        payload.stop_reason = firstNonEmpty(stopReason, "end_turn");
        if (transcript.schema_version === "1.1") {
          payload.model = firstNonEmpty(message.model, transcript.meta.model);
          payload.stop_reason = stopReason;
        }
        if (message.usage) payload.usage = message.usage;
      }
      lines.push(JSON.stringify({
        type: message.role,
        uuid: id,
        parentUuid: parent || null,
        sessionId: sessionID,
        timestamp: firstNonEmpty(message.timestamp, transcript.meta.timestamp, ""),
        cwd: transcript.meta.cwd ?? "",
        gitBranch: transcript.meta.git_branch ?? "",
        version: transcript.meta.cli_version ?? "",
        message: payload,
      }));
      parent = id;
      last = id;
    });
    if (transcript.meta.title) lines.push(JSON.stringify({ type: "summary", summary: transcript.meta.title, leafUuid: last }));
    return { data: `${lines.join("\n")}\n`, warnings: renderLossWarnings(transcript, "claude_code") };
  }
}

// Mirrors Go renderLossWarnings (codec_helpers.go) for the claude_code format.
function renderLossWarnings(transcript: Transcript, format: string): Warning[] {
  const warnings: Warning[] = [];
  if (hasExtra(transcript.extra) || hasExtra(transcript.meta.extra)) {
    warnings.push({ code: "extension_omitted", message: `${format} cannot represent canonical extension data; extension omitted` });
  }
  transcript.messages.forEach((message, messageIndex) => {
    if (hasExtra(message.extra)) {
      warnings.push({ path: `messages[${messageIndex}].extra`, code: "extension_omitted", message: `${format} cannot represent message extension data; extension omitted` });
    }
    message.content.forEach((block, blockIndex) => {
      if (renderSupportsBlock(message.role, block)) return;
      warnings.push({
        path: `messages[${messageIndex}].content[${blockIndex}]`,
        code: "unsupported_block",
        message: `${format} cannot represent ${block.type} content; block omitted`,
      });
    });
  });
  return warnings;
}

// Mirrors Go renderSupportsBlock for the claude_code format: artifact and
// unknown blocks are never representable; image always is; thinking is
// assistant-only; tool_use is assistant-only; tool_result is user-only.
function renderSupportsBlock(role: Message["role"], block: Block): boolean {
  switch (block.type) {
    case "artifact": case "unknown":
      return false;
    case "image":
      return Boolean(block.source);
    case "thinking":
      return role === "assistant";
    case "tool_use":
      return role === "assistant";
    case "tool_result":
      return role === "user";
    case "text":
      return true;
    default:
      return false;
  }
}

// Matches Go: Extra is json.RawMessage — present-but-empty ({}, null) still warns.
function hasExtra(value: unknown): boolean {
  return value !== undefined;
}

function parseAnthropicContent(value: unknown, messageIndex: number, pending: string[], warnings: Warning[]): Block[] {
  if (typeof value === "string") return value ? [{ type: "text", text: value }] : [];
  if (!Array.isArray(value)) return [];
  const blocks: Block[] = [];
  value.forEach((entry, bi) => {
    const raw = isRecord(entry) ? entry : {};
    const kind = stringValue(raw.type);
    const known = KNOWN_BLOCKS.has(kind);
    const block = known ? parseBlock(raw, messageIndex, bi, pending) : undefined;
    if (!block) {
      warnings.push({ path: `messages[${messageIndex}].content[${bi}]`, code: known ? "invalid_block" : "unknown_block", message: "block omitted" });
      return;
    }
    blocks.push(block);
  });
  return blocks;
}

function parseBlock(raw: Record<string, unknown>, messageIndex: number, blockIndex: number, pending: string[]): Block | undefined {
  switch (stringValue(raw.type)) {
    case "text": {
      const text = firstNonEmpty(stringValue(raw.text), stringValue(raw.content));
      return text ? { type: "text", text } : undefined;
    }
    case "thinking": {
      const text = firstNonEmpty(stringValue(raw.thinking), stringValue(raw.text));
      const signature = stringValue(raw.signature);
      const encrypted = firstNonEmpty(stringValue(raw.encrypted), stringValue(raw.encrypted_content));
      if (!text && !encrypted) return undefined;
      const block: Block = { type: "thinking" };
      if (text) block.text = text;
      if (signature) block.signature = signature;
      if (encrypted) block.encrypted = encrypted;
      return block;
    }
    case "redacted_thinking": {
      const encrypted = firstNonEmpty(stringValue(raw.data), stringValue(raw.encrypted));
      return encrypted ? { type: "thinking", encrypted } : undefined;
    }
    case "tool_use": {
      const name = stringValue(raw.name);
      if (!name) return undefined;
      const id = stringValue(raw.id) || `tool-${messageIndex + 1}-${blockIndex + 1}`;
      let input: unknown = raw.input !== undefined ? raw.input : raw.arguments;
      if (typeof input === "string") {
        try { input = JSON.parse(input); } catch { /* keep the raw string */ }
      }
      pending.push(id);
      return { type: "tool_use", id, name, input: input === undefined ? null : input };
    }
    case "tool_result": {
      let id = stringValue(raw.tool_use_id);
      if (!id && pending.length) {
        const paired = pending.shift();
        if (paired) id = paired;
      }
      const block: Block = { type: "tool_result", content: raw.content === undefined ? null : raw.content };
      if (id) block.tool_use_id = id;
      if (raw.is_error === true) block.is_error = true;
      return block;
    }
    case "image": {
      const source = isRecord(raw.source) ? raw.source : {};
      const media: MediaSource = { type: firstNonEmpty(stringValue(source.type), "base64") };
      const mediaType = firstNonEmpty(stringValue(source.media_type), stringValue(source.mediaType));
      if (mediaType) media.media_type = mediaType;
      const data = stringValue(source.data);
      if (data) media.data = data;
      const url = firstNonEmpty(stringValue(source.url), stringValue(raw.image_url));
      if (url) media.url = url;
      return { type: "image", source: media };
    }
    default:
      return undefined;
  }
}

function renderContent(blocks: Block[]): unknown[] {
  const result: unknown[] = [];
  for (const block of blocks) {
    switch (block.type) {
      case "text":
        result.push({ type: "text", text: block.text ?? "" });
        break;
      case "thinking":
        if (block.signature) result.push({ type: "thinking", thinking: block.text ?? "", signature: block.signature });
        else if (block.encrypted) result.push({ type: "redacted_thinking", data: block.encrypted });
        else if (block.text) result.push({ type: "text", text: `[Reasoning]\n${block.text}` });
        break;
      case "tool_use":
        result.push({ type: "tool_use", id: block.id, name: block.name, input: block.input ?? null });
        break;
      case "tool_result":
        result.push({ type: "tool_result", tool_use_id: block.tool_use_id, content: toolResultContent(block.content), is_error: block.is_error === true });
        break;
      case "image":
        if (block.source) result.push({ type: "image", source: block.source });
        break;
      default:
        break; // artifact and unknown blocks have no Claude Code representation
    }
  }
  return result;
}

function toolResultContent(content: unknown): unknown {
  if (content === undefined) return "";
  if (content === null) return "null";
  if (typeof content === "string") return content;
  if (Array.isArray(content) && content.every((entry) => isRecord(entry) && (entry.type === "text" || entry.type === "image"))) return content;
  return JSON.stringify(content);
}

function usageFromMap(value: unknown): Usage | undefined {
  if (!isRecord(value) || !Object.keys(value).length) return undefined;
  const usage: Usage = { input_tokens: integerValue(value.input_tokens), output_tokens: integerValue(value.output_tokens) };
  const cacheRead = integerValue(value.cache_read_input_tokens);
  if (cacheRead) usage.cache_read_input_tokens = cacheRead;
  const cacheCreation = integerValue(value.cache_creation_input_tokens);
  if (cacheCreation) usage.cache_creation_input_tokens = cacheCreation;
  return usage;
}

function integerValue(value: unknown): number {
  const parsed = typeof value === "number" ? value : typeof value === "string" && value.trim() ? Number(value) : 0;
  return Number.isSafeInteger(parsed) ? parsed : 0;
}

const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u;

function timestampValue(value: unknown): string {
  if (typeof value === "number" && Number.isFinite(value)) return unixTimestamp(value);
  if (typeof value !== "string" || !value) return "";
  if (RFC3339.test(value)) {
    // Valid RFC3339 is returned unchanged: re-formatting through Date would
    // truncate sub-millisecond fractions (2026-01-01T00:00:00.123456789Z).
    if (!Number.isNaN(new Date(value).getTime())) return value;
  }
  if (/^[+-]?\d+$/u.test(value.trim())) return unixTimestamp(Number(value));
  return "";
}

function unixTimestamp(seconds: number): string {
  const date = seconds > 1e11 ? new Date(seconds) : new Date(seconds * 1000);
  try { return date.toISOString(); } catch { return ""; }
}

function seededHash(...values: string[]): string {
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  for (const value of values) {
    for (let index = 0; index < value.length; index += 1) {
      const code = value.charCodeAt(index);
      h1 = Math.imul(h1 ^ code, 0x01000193);
      h2 = Math.imul(h2 + code, 0x85ebca6b);
    }
    h1 = Math.imul(h1 ^ (h1 >>> 15), 0x2545f491);
    h2 = Math.imul(h2 ^ (h2 >>> 13), 0x9e3779b1);
  }
  return (h1 >>> 0).toString(16).padStart(8, "0") + (h2 >>> 0).toString(16).padStart(8, "0");
}

function seededUUID(...values: string[]): string {
  const raw = seededHash(...values) + seededHash(...values, "uuid");
  return `${raw.slice(0, 8)}-${raw.slice(8, 12)}-4${raw.slice(13, 16)}-a${raw.slice(17, 20)}-${raw.slice(20, 32)}`;
}

function firstNonEmpty(...values: Array<string | undefined>): string {
  for (const value of values) if (value && value.trim()) return value;
  return "";
}

function stringValue(value: unknown): string { return typeof value === "string" ? value : ""; }
function isRecord(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }

function checkJSONDepth(value: string, maximum: number): void {
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (const character of value) {
    if (inString) {
      if (escaped) escaped = false;
      else if (character === "\\") escaped = true;
      else if (character === "\"") inString = false;
    } else if (character === "\"") inString = true;
    else if (character === "{" || character === "[") {
      depth += 1;
      if (depth > maximum) throw new MoiraiError("limit_exceeded", "JSON nesting exceeds the safety limit");
    } else if (character === "}" || character === "]") depth = Math.max(0, depth - 1);
  }
}
