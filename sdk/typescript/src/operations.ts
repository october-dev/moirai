import { type Block, MoiraiError, newId, type Role, type Transcript } from "./model.js";

export interface Span { start: number; end: number }
export interface Selector { sessionId: string; span?: Span }

export function parseSelector(value: string): Selector {
  const trimmed = value.trim();
  if (!trimmed) throw new MoiraiError("invalid_transcript", "empty selector");
  const pieces = trimmed.split("#");
  if (pieces.length === 1) return { sessionId: pieces[0]! };
  if (pieces.length !== 2 || !pieces[0] || !pieces[1]) throw new MoiraiError("invalid_transcript", "invalid selector");
  const range = pieces[1].split("-");
  if (range.length > 2 || !range[0]) throw new MoiraiError("invalid_transcript", "invalid range");
  const start = Number(range[0]);
  const end = range.length === 2 ? Number(range[1]) : start;
  if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start < 1 || end < start) {
    throw new MoiraiError("invalid_transcript", "invalid range");
  }
  return { sessionId: pieces[0], span: { start, end } };
}

export function select(transcript: Transcript, span: Span): Transcript {
  if (!Number.isSafeInteger(span.start) || !Number.isSafeInteger(span.end) || span.start < 1 || span.end < span.start || span.end > transcript.messages.length) {
    throw new MoiraiError("invalid_transcript", "range outside transcript");
  }
  const start = span.start - 1;
  const end = span.end;
  const allCalls = new Map<string, number>();
  const allResults = new Map<string, number>();
  transcript.messages.forEach((message, messageIndex) => message.content.forEach((block) => {
    if (block.type === "tool_use" && block.id) allCalls.set(block.id, messageIndex);
    if (block.type === "tool_result" && block.tool_use_id) allResults.set(block.tool_use_id, messageIndex);
  }));
  for (let messageIndex = start; messageIndex < end; messageIndex += 1) {
    for (const block of transcript.messages[messageIndex]!.content) {
      if (block.type === "tool_use" && block.id) assertPairedInside("call", block.id, allResults.get(block.id), start, end);
      if (block.type === "tool_result" && block.tool_use_id) assertPairedInside("result", block.tool_use_id, allCalls.get(block.tool_use_id), start, end);
    }
  }
  const result = structuredClone(transcript);
  result.messages = result.messages.slice(start, end);
  result.meta.id = newId();
  result.meta.provenance = {
    ...(result.meta.provenance ?? {}),
    parent_session_id: transcript.meta.id,
    parent_checkpoint: `messages:${span.start}-${span.end}`,
  };
  return result;
}

function assertPairedInside(kind: string, id: string, pairedAt: number | undefined, start: number, end: number): void {
  if (pairedAt !== undefined && (pairedAt < start || pairedAt >= end)) {
    throw new MoiraiError("invalid_transcript", `range separates tool ${kind} ${JSON.stringify(id)} from its pair`);
  }
}

export interface TextOptions {
  maxBytes?: number;
  includeThinking?: boolean;
  includeTools?: boolean;
  includeMetadata?: boolean;
}

export function toText(transcript: Transcript, options: TextOptions = {}): string {
  const sections: string[] = [];
  if (options.includeMetadata) {
    const facts = [`Session: ${transcript.meta.id}`];
    if (transcript.meta.title) facts.push(`Title: ${transcript.meta.title}`);
    if (transcript.meta.cwd) facts.push(`Working directory: ${transcript.meta.cwd}`);
    if (transcript.meta.git_branch) facts.push(`Git branch: ${transcript.meta.git_branch}`);
    sections.push(facts.join("\n"));
  }
  transcript.messages.forEach((message, messageIndex) => {
    const parts: string[] = [];
    for (const block of message.content) appendBlockText(parts, block, options);
    if (parts.length) sections.push(`${message.role === "assistant" ? "Assistant" : "User"} [${messageIndex + 1}]: ${parts.join("\n")}`);
  });
  return boundedTail(sections.join("\n\n"), options.maxBytes && options.maxBytes > 0 ? options.maxBytes : 64 << 10);
}

function appendBlockText(parts: string[], block: Block, options: TextOptions): void {
  if (block.type === "text" && block.text) parts.push(block.text);
  else if (block.type === "thinking" && options.includeThinking && block.text) parts.push(`Thinking: ${block.text}`);
  else if (block.type === "tool_use" && options.includeTools) parts.push(`Tool call ${block.name ?? ""}${jsonSuffix(block.input)}`);
  else if (block.type === "tool_result" && options.includeTools) parts.push(`Tool result${block.is_error ? " (error)" : ""}${jsonSuffix(block.content)}`);
  else if (block.type === "image") parts.push("[image omitted]");
  else if (block.type === "artifact" && block.artifact) parts.push(`[artifact: ${block.artifact.name}]`);
}

function jsonSuffix(value: unknown): string {
  if (value === undefined) return "";
  try { return `: ${JSON.stringify(value)}`; } catch { return ""; }
}

export function boundedTail(value: string, maxBytes: number): string {
  const bytes = new TextEncoder().encode(value);
  if (maxBytes <= 0 || bytes.length <= maxBytes) return value;
  const marker = "[earlier context omitted]\n";
  const markerBytes = new TextEncoder().encode(marker);
  if (maxBytes <= markerBytes.length) return validUtf8Tail(bytes, maxBytes);
  let tail = validUtf8Tail(bytes, maxBytes - markerBytes.length);
  const newline = tail.indexOf("\n");
  if (newline >= 0) tail = tail.slice(newline + 1);
  return marker + tail;
}

function validUtf8Tail(value: Uint8Array, maxBytes: number): string {
  const start = Math.max(0, value.length - maxBytes);
  for (let offset = start; offset < Math.min(value.length, start + 4); offset += 1) {
    try { return new TextDecoder("utf-8", { fatal: true }).decode(value.slice(offset)); } catch { /* start was inside a code point */ }
  }
  return "";
}

export interface SearchHit {
  messageIndex: number;
  blockIndex: number;
  role: Role;
  kind: string;
  text: string;
  score: number;
}

export function search(transcript: Transcript, rawQuery: string, limit = 50): SearchHit[] {
  const query = normalizeSearch(rawQuery);
  if (!query) return [];
  const hits: SearchHit[] = [];
  transcript.messages.forEach((message, messageIndex) => message.content.forEach((block, blockIndex) => {
    let value = "";
    if (block.type === "text" || block.type === "thinking") value = block.text ?? "";
    else if (block.type === "tool_use") value = `${block.name ?? ""} ${safeJSON(block.input)}`;
    else if (block.type === "tool_result") value = safeJSON(block.content);
    else if (block.type === "artifact" && block.artifact) value = `${block.artifact.name} ${block.artifact.description ?? ""}`;
    const score = matchScore(normalizeSearch(value), query);
    if (score > 0) hits.push({ messageIndex: messageIndex + 1, blockIndex: blockIndex + 1, role: message.role, kind: block.type, text: boundedTail(value, 2_000), score });
  }));
  hits.sort((left, right) => right.score - left.score || right.messageIndex - left.messageIndex || left.blockIndex - right.blockIndex);
  return hits.slice(0, limit > 0 ? limit : 50);
}

function safeJSON(value: unknown): string { try { return value === undefined ? "" : JSON.stringify(value); } catch { return ""; } }
function normalizeSearch(value: string): string { return value.trim().toLocaleLowerCase().replace(/\s/gu, " "); }
function matchScore(value: string, query: string): number {
  const index = value.indexOf(query);
  if (index >= 0) return 10_000 - Math.min(index, 5_000) + [...query].length;
  const target = [...value];
  const needle = [...query];
  let needleIndex = 0;
  let gaps = 0;
  let last = -1;
  for (let valueIndex = 0; valueIndex < target.length && needleIndex < needle.length; valueIndex += 1) {
    if (target[valueIndex] === needle[needleIndex]) {
      if (last >= 0) gaps += valueIndex - last - 1;
      last = valueIndex;
      needleIndex += 1;
    }
  }
  return needleIndex === needle.length ? Math.max(1, 1_000 - gaps) : 0;
}
