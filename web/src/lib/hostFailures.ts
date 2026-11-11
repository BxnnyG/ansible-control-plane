/** Extracts per-host failure messages from already loaded run log lines (client-side only). */

export interface HostFailure {
  /** Truncated, single-line message. */
  message: string;
  /** Index of the first failure line of the host within the given lines. */
  line: number;
}

export const MAX_FAILURE_LENGTH = 240;

const FATAL = /(?:fatal|failed): \[([^\]]+?)(?: -> [^\]]*)?\]: (FAILED|UNREACHABLE)! =>\s*(.*)$/;
const KEYS = ['module_stderr', 'msg', 'stderr', 'module_stdout', 'stdout'];

function tidy(text: string, max: number): string {
  const one = text.replace(/\s+/g, ' ').trim();
  return one.length > max ? `${one.slice(0, max - 3)}...` : one;
}

function fromJson(rest: string): string | null {
  try {
    const obj: unknown = JSON.parse(rest);
    if (obj && typeof obj === 'object') {
      for (const k of KEYS) {
        const v = (obj as Record<string, unknown>)[k];
        if (typeof v === 'string' && v.trim() !== '') return v;
      }
    }
  } catch {
    /* not (complete) JSON: fall through to the raw text */
  }
  return null;
}

function fromYaml(texts: readonly string[], from: number): string | null {
  const found: Record<string, string> = {};
  for (let i = from + 1; i < Math.min(texts.length, from + 12); i++) {
    const m = /^\s+(\w+): ?(.*)$/.exec(texts[i]);
    if (!m) break;
    if (KEYS.includes(m[1]) && m[2].trim() !== '') found[m[1]] = m[2];
  }
  const key = KEYS.find((k) => found[k]);
  return key ? found[key] : null;
}

/**
 * Finds the first `fatal: [host]: FAILED!/UNREACHABLE!` line per host and the best message
 * of it: module_stderr before msg (JSON payload), the indented yaml keys of the yaml
 * callback, or the raw remainder of the line.
 */
export function extractHostFailures(
  texts: readonly string[],
  max = MAX_FAILURE_LENGTH,
): Map<string, HostFailure> {
  const out = new Map<string, HostFailure>();
  texts.forEach((text, i) => {
    const m = FATAL.exec(text);
    if (!m || out.has(m[1])) return;
    const rest = m[3].trim();
    const raw = fromJson(rest) ?? fromYaml(texts, i) ?? rest;
    const label = m[2] === 'UNREACHABLE' ? 'Unreachable' : 'Failed';
    out.set(m[1], { message: tidy(raw || label, max), line: i });
  });
  return out;
}
