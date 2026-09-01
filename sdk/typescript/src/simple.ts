import {
  DEFAULT_LIMITS, type Block, type Limits, type Message, MoiraiError, newId,
  type ParseResult, SCHEMA_VERSION, type Transcript, type Warning, validate,
} from "./model.js";

export interface ParseOptions { limits?: Limits; sourceId?: string; now?: () => string }
export interface Codec {
  readonly format: string;
  parse(data: string | Uint8Array, options?: ParseOptions): ParseResult;
  render(transcript: Transcript, limits?: Limits): string;
}

export class SimpleCodec implements Codec {
  readonly format = "simple";
  parse(data: string | Uint8Array, options: ParseOptions = {}): ParseResult {
    const text = typeof data === "string" ? data : new TextDecoder().decode(data);
    const limits = options.limits ?? { ...DEFAULT_LIMITS };
    if (new TextEncoder().encode(text).length > limits.maxInputBytes) throw new MoiraiError("limit_exceeded", "input exceeds the safety limit");
    checkJSONDepth(text, limits.maxNestingDepth);
    let decoded: unknown;
    try { decoded = JSON.parse(text); }
    catch (error) { throw new MoiraiError("invalid_transcript", `invalid JSON: ${String(error)}`); }
    if (!isRecord(decoded)) throw new MoiraiError("invalid_transcript", "document must be an object");
    const raw = decoded;
    if (raw.schema_version !== undefined && raw.schema_version !== SCHEMA_VERSION) throw new MoiraiError("unsupported_version", `schema_version must be ${SCHEMA_VERSION}`);
    if (!Array.isArray(raw.messages)) throw new MoiraiError("invalid_transcript", "messages array is required");
    const warnings: Warning[] = [];
    const pending: string[] = [];
    const messages: Message[] = [];
    raw.messages.forEach((value, mi) => {
      const item = asRecord(value);
      const role = typeof item.role === "string" ? item.role.toLowerCase() : "";
      if (role !== "user" && role !== "assistant") {
        warnings.push({ path: `messages[${mi}]`, code: "unknown_role", message: "message omitted" }); return;
      }
      const blocks = parseContent(item.content, mi, pending, warnings);
      if (!blocks.length) return;
      const message: Message = { role, content: blocks };
      copyString(item, message, "id"); copyString(item, message, "timestamp");
      copyString(item, message, "model");
      const stopReason = parseStopReason(item.stop_reason); if (stopReason) message.stop_reason = stopReason;
      if (isRecord(item.usage)) message.usage = item.usage as unknown as NonNullable<Message["usage"]>;
      if (item.extra !== undefined) message.extra = item.extra;
      messages.push(message);
    });
    const nestedMeta = asRecord(raw.meta);
    const meta = { id: stringValue(raw.id) || stringValue(nestedMeta.id) || options.sourceId || newId(), timestamp: stringValue(raw.timestamp) || stringValue(nestedMeta.timestamp) || options.now?.() || new Date().toISOString() } as Transcript["meta"];
    for (const key of ["updated_at", "cwd", "git_branch", "title", "model", "cli_version"] as const) {
      copyString(nestedMeta, meta, key);
      copyString(raw, meta, key);
    }
    if (isRecord(nestedMeta.provenance)) meta.provenance = nestedMeta.provenance as NonNullable<Transcript["meta"]["provenance"]>;
    if (isRecord(raw.provenance)) meta.provenance = raw.provenance as NonNullable<Transcript["meta"]["provenance"]>;
    if (nestedMeta.extra !== undefined) meta.extra = nestedMeta.extra;
    const transcript: Transcript = { schema_version: SCHEMA_VERSION, meta, messages };
    if (raw.extra !== undefined) transcript.extra = raw.extra;
    let last = meta.timestamp;
    for (const message of transcript.messages) {
      const timestamp = message.timestamp;
      if (!timestamp && last) message.timestamp = last;
      else last = timestamp;
    }
    validate(transcript, limits);
    return { transcript, warnings };
  }
  render(transcript: Transcript, limits: Limits = { ...DEFAULT_LIMITS }): string {
    validate(transcript, limits);
    return `${JSON.stringify(transcript, null, 2)}\n`;
  }
}

function parseContent(value: unknown, mi: number, pending: string[], warnings: Warning[]): Block[] {
  if (typeof value === "string") return value ? [{ type: "text", text: value }] : [];
  if (!Array.isArray(value)) { warnings.push({ path: `messages[${mi}].content`, code: "invalid_content", message: "content omitted" }); return []; }
  const blocks: Block[] = [];
  value.forEach((entry, bi) => {
    const raw = asRecord(entry);
    if (typeof raw.type !== "string") { warnings.push({ path: `messages[${mi}].content[${bi}]`, code: "invalid_block", message: "block omitted" }); return; }
    const block = parseBlock(raw, `messages[${mi}].content[${bi}]`);
    if (!block) { warnings.push({ path: `messages[${mi}].content[${bi}]`, code: "invalid_block", message: "block omitted" }); return; }
    if (block.type === "tool_use") { block.id ||= `tool-${mi + 1}-${bi + 1}`; pending.push(block.id); }
    else if (block.type === "tool_result" && !block.tool_use_id && pending.length) {
      const paired = pending.shift();
      if (paired) block.tool_use_id = paired;
    }
    blocks.push(block);
  });
  return blocks;
}
function parseStopReason(value: unknown): string {
  if (typeof value === "string") return value;
  return isRecord(value) && typeof value.other === "string" ? value.other : "";
}
function parseBlock(raw: Record<string, unknown>, path: string): Block | undefined {
  switch (raw.type) {
    case "text":
      return typeof raw.text === "string" && raw.text ? { type: "text", text: raw.text } : undefined;
    case "thinking": {
      if (raw.text !== undefined && typeof raw.text !== "string" || raw.encrypted !== undefined && typeof raw.encrypted !== "string" || raw.signature !== undefined && typeof raw.signature !== "string") return undefined;
      return raw.text || raw.encrypted ? { type: "thinking", ...(raw.text ? { text: raw.text } : {}), ...(raw.signature ? { signature: raw.signature } : {}), ...(raw.encrypted ? { encrypted: raw.encrypted } : {}) } : undefined;
    }
    case "tool_use":
      if (raw.id !== undefined && typeof raw.id !== "string" || typeof raw.name !== "string" || !raw.name || raw.input === undefined) return undefined;
      return { type: "tool_use", ...(typeof raw.id === "string" && raw.id ? { id: raw.id } : {}), name: raw.name, input: raw.input };
    case "tool_result":
      if (raw.tool_use_id !== undefined && typeof raw.tool_use_id !== "string" || raw.content === null || raw.is_error !== undefined && typeof raw.is_error !== "boolean") return undefined;
      return { type: "tool_result", ...(typeof raw.tool_use_id === "string" && raw.tool_use_id ? { tool_use_id: raw.tool_use_id } : {}), ...(raw.content !== undefined ? { content: raw.content } : {}), ...(raw.is_error === true ? { is_error: true } : {}) };
    case "image":
      return isRecord(raw.source) ? { type: "image", source: raw.source as unknown as NonNullable<Block["source"]> } : undefined;
    case "artifact":
      return isRecord(raw.artifact) ? { type: "artifact", artifact: raw.artifact as unknown as NonNullable<Block["artifact"]> } : undefined;
    case "unknown":
      return raw.data !== undefined ? { type: "unknown", data: raw.data } : undefined;
    default:
      void path;
      return undefined;
  }
}
function isRecord(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }
function asRecord(value: unknown): Record<string, unknown> { return isRecord(value) ? value : {}; }
function stringValue(value: unknown): string { return typeof value === "string" ? value.trim() : ""; }
function copyString(source: Record<string, unknown>, target: object, key: string): void { const value = stringValue(source[key]); if (value) Object.assign(target, { [key]: value }); }

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

export class Registry {
  readonly #codecs = new Map<string, Codec>();
  constructor(codecs: Codec[] = [new SimpleCodec()]) { codecs.forEach((codec) => this.register(codec)); }
  register(codec: Codec): void {
    if (!codec?.format || this.#codecs.has(codec.format)) throw new MoiraiError("duplicate_codec", `codec already registered: ${codec?.format ?? ""}`);
    this.#codecs.set(codec.format, codec);
  }
  codec(format: string): Codec { const codec = this.#codecs.get(format); if (!codec) throw new MoiraiError("unknown_format", `unknown format: ${format}`); return codec; }
  formats(): string[] { return [...this.#codecs.keys()]; }
  convert(data: string | Uint8Array, from: string, to: string, options?: ParseOptions): { data: string; warnings: Warning[] } {
    const parsed = this.codec(from).parse(data, options);
    if (from !== to) { const original = parsed.transcript.meta.id; parsed.transcript.meta.id = newId(); parsed.transcript.meta.provenance = { source_format: from as never, source_session_id: original, imported_at: new Date().toISOString() }; }
    return { data: this.codec(to).render(parsed.transcript, options?.limits), warnings: parsed.warnings };
  }
}
export const defaultRegistry = new Registry();
