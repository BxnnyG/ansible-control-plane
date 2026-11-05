/**
 * Minimal ANSI SGR converter for Ansible output. Text is never turned into HTML:
 * the result is a list of plain-text segments with style attributes, which the
 * view renders with normal (escaping) Svelte interpolation.
 */

export interface Segment {
  text: string;
  fg?: string;
  bg?: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
}

interface Style {
  fg?: string;
  bg?: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
}

const BASIC = [
  '#1f2937',
  '#dc2626',
  '#16a34a',
  '#ca8a04',
  '#2563eb',
  '#9333ea',
  '#0891b2',
  '#d1d5db',
];
const BRIGHT = [
  '#6b7280',
  '#f87171',
  '#4ade80',
  '#facc15',
  '#60a5fa',
  '#c084fc',
  '#22d3ee',
  '#f9fafb',
];

/** Color of an xterm 256-color index. */
export function color256(n: number): string | undefined {
  if (n < 0 || n > 255 || !Number.isInteger(n)) return undefined;
  if (n < 8) return BASIC[n];
  if (n < 16) return BRIGHT[n - 8];
  if (n >= 232) {
    const v = 8 + (n - 232) * 10;
    return `rgb(${v}, ${v}, ${v})`;
  }
  const i = n - 16;
  const level = (x: number) => (x === 0 ? 0 : 55 + x * 40);
  return `rgb(${level(Math.floor(i / 36))}, ${level(Math.floor(i / 6) % 6)}, ${level(i % 6)})`;
}

function rgb(r: number, g: number, b: number): string | undefined {
  for (const v of [r, g, b]) if (!Number.isInteger(v) || v < 0 || v > 255) return undefined;
  return `rgb(${r}, ${g}, ${b})`;
}

function applySgr(style: Style, params: number[]): Style {
  let s = { ...style };
  if (params.length === 0) params = [0];
  for (let i = 0; i < params.length; i++) {
    const p = params[i];
    if (p === 0) s = {};
    else if (p === 1) s.bold = true;
    else if (p === 2) s.dim = true;
    else if (p === 3) s.italic = true;
    else if (p === 4) s.underline = true;
    else if (p === 22) {
      s.bold = false;
      s.dim = false;
    } else if (p === 23) s.italic = false;
    else if (p === 24) s.underline = false;
    else if (p >= 30 && p <= 37) s.fg = BASIC[p - 30];
    else if (p >= 90 && p <= 97) s.fg = BRIGHT[p - 90];
    else if (p >= 40 && p <= 47) s.bg = BASIC[p - 40];
    else if (p >= 100 && p <= 107) s.bg = BRIGHT[p - 100];
    else if (p === 39) s.fg = undefined;
    else if (p === 49) s.bg = undefined;
    else if (p === 38 || p === 48) {
      let c: string | undefined;
      if (params[i + 1] === 5) {
        c = color256(params[i + 2]);
        i += 2;
      } else if (params[i + 1] === 2) {
        c = rgb(params[i + 2], params[i + 3], params[i + 4]);
        i += 4;
      } else {
        break;
      }
      if (p === 38) s.fg = c;
      else s.bg = c;
    }
    // Unknown codes (blink, reverse, ...) are ignored.
  }
  return s;
}

/** Stateful parser: style and incomplete escape sequences carry over between chunks. */
export class AnsiParser {
  private style: Style = {};
  private pending = '';

  parse(input: string): Segment[] {
    const text = this.pending + input;
    this.pending = '';
    const out: Segment[] = [];
    let buf = '';
    const flush = () => {
      if (buf) out.push({ text: buf, ...this.style });
      buf = '';
    };
    let i = 0;
    while (i < text.length) {
      const ch = text[i];
      if (ch !== '\u001b') {
        buf += ch;
        i++;
        continue;
      }
      if (i + 1 >= text.length) {
        this.pending = text.slice(i);
        break;
      }
      if (text[i + 1] !== '[') {
        i += 2; // two-character escape: drop
        continue;
      }
      // CSI: parameters 0x30-0x3F, intermediates 0x20-0x2F, final 0x40-0x7E.
      let j = i + 2;
      while (j < text.length && text.charCodeAt(j) >= 0x20 && text.charCodeAt(j) <= 0x3f) j++;
      if (j >= text.length) {
        // Cap the held-back tail so a stray ESC cannot swallow the rest of the log.
        if (text.length - i <= 32) this.pending = text.slice(i);
        break;
      }
      if (text[j] === 'm') {
        const raw = text.slice(i + 2, j);
        const params = raw === '' ? [] : raw.split(/[;:]/).map((x) => (x === '' ? 0 : Number(x)));
        if (params.every((x) => Number.isFinite(x))) {
          flush();
          this.style = applySgr(this.style, params);
        }
      }
      i = j + 1;
    }
    flush();
    return out;
  }
}

/** Convenience for one-shot conversion (tests, small strings). */
export function ansiToSegments(input: string): Segment[] {
  return new AnsiParser().parse(input);
}

/** Inline CSS for a segment; values come from fixed tables or validated integers. */
export function segmentStyle(s: Segment): string {
  const parts: string[] = [];
  if (s.fg) parts.push(`color:${s.fg}`);
  if (s.bg) parts.push(`background-color:${s.bg}`);
  if (s.bold) parts.push('font-weight:600');
  if (s.dim) parts.push('opacity:0.7');
  if (s.italic) parts.push('font-style:italic');
  if (s.underline) parts.push('text-decoration:underline');
  return parts.join(';');
}
