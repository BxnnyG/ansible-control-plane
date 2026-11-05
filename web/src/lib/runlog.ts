import { AnsiParser, type Segment } from './ansi';

export interface LogLine {
  /** Monotonic id, stable across trimming (keyed rendering). */
  id: number;
  segments: Segment[];
  /** Plain text of the line, used for search. */
  text: string;
}

/** Extracts the chunk of a `log` SSE event; the payload is a JSON-encoded string. */
export function parseLogEvent(data: string): string {
  try {
    const v: unknown = JSON.parse(data);
    return typeof v === 'string' ? v : '';
  } catch {
    return '';
  }
}

/** Parses a JSON SSE payload; null when it is not a JSON object. */
export function parseJsonEvent<T>(data: string): T | null {
  try {
    const v: unknown = JSON.parse(data);
    return v !== null && typeof v === 'object' ? (v as T) : null;
  } catch {
    return null;
  }
}

/**
 * Accumulates log chunks into lines. Handles LF, CRLF (also split across chunks)
 * and bare CR (progress output: the current line is overwritten).
 */
export class LogModel {
  lines: LogLine[] = [];
  /** Number of lines dropped from the front because of the size cap. */
  dropped = 0;
  private cur: Segment[] = [];
  private nextId = 0;
  private pendingCR = false;
  private ansi = new AnsiParser();

  constructor(private readonly maxLines = 20000) {}

  append(chunk: string): void {
    for (const seg of this.ansi.parse(chunk)) this.addSegment(seg);
    this.commitPartial();
  }

  private addSegment(seg: Segment): void {
    const parts = seg.text.split(/(\r\n|\n|\r)/);
    for (const part of parts) {
      if (part === '') continue;
      if (part === '\r\n' || part === '\n') {
        this.pendingCR = false;
        this.newLine();
      } else if (part === '\r') {
        if (this.pendingCR) this.cur = [];
        this.pendingCR = true;
      } else {
        if (this.pendingCR) {
          this.cur = [];
          this.pendingCR = false;
        }
        this.cur.push({ ...seg, text: part });
      }
    }
  }

  private newLine(): void {
    this.lines.push(this.make(this.cur));
    this.cur = [];
    if (this.lines.length > this.maxLines) {
      const extra = this.lines.length - this.maxLines;
      this.lines.splice(0, extra);
      this.dropped += extra;
    }
  }

  private make(segs: Segment[]): LogLine {
    return { id: this.nextId++, segments: segs, text: segs.map((s) => s.text).join('') };
  }

  /** The unfinished last line (no newline yet), if any. */
  private commitPartial(): void {
    this.partial = this.cur.length > 0 ? this.make(this.cur) : null;
  }

  partial: LogLine | null = null;

  /** All lines including the unfinished one. */
  all(): LogLine[] {
    return this.partial ? [...this.lines, this.partial] : [...this.lines];
  }
}

/** Indices of lines containing the query (case-insensitive); empty query matches nothing. */
export function searchLines(lines: readonly LogLine[], query: string): number[] {
  const q = query.toLowerCase();
  if (!q) return [];
  const hits: number[] = [];
  lines.forEach((l, i) => {
    if (l.text.toLowerCase().includes(q)) hits.push(i);
  });
  return hits;
}
