/**
 * Accepts only same-origin relative paths ("/foo?bar=1"). Everything else
 * (absolute URLs, protocol-relative "//host", backslashes, control characters)
 * falls back. Mirrors safeReturnTo in internal/auth/state.go.
 */
export function safeReturnTo(value: string | null | undefined, fallback = '/'): string {
  if (!value || value[0] !== '/') return fallback;
  if (value.length > 1 && (value[1] === '/' || value[1] === '\\')) return fallback;
  for (const ch of value) {
    const c = ch.codePointAt(0) ?? 0;
    if (c < 0x20 || c === 0x7f || ch === '\\') return fallback;
  }
  return value;
}
