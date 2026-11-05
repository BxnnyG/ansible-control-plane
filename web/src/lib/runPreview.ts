import type { RunPreview } from './api';

export type PreviewState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'ok'; preview: RunPreview }
  | { kind: 'error'; message: string };

export interface PreviewScheduler {
  /** Schedule a preview; replaces any pending call and discards stale results. */
  request(): void;
  cancel(): void;
}

/**
 * Debounced preview loader. Only the result of the latest request is
 * delivered; earlier in-flight responses are dropped.
 */
export function createPreviewScheduler(
  load: () => Promise<RunPreview>,
  onState: (s: PreviewState) => void,
  describe: (e: unknown) => string,
  delayMs = 400,
): PreviewScheduler {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;
  return {
    request() {
      clearTimeout(timer);
      const mine = ++seq;
      onState({ kind: 'loading' });
      timer = setTimeout(async () => {
        try {
          const preview = await load();
          if (mine === seq) onState({ kind: 'ok', preview });
        } catch (e) {
          if (mine === seq) onState({ kind: 'error', message: describe(e) });
        }
      }, delayMs);
    },
    cancel() {
      clearTimeout(timer);
      seq++;
    },
  };
}

/** Summary line of a preview, e.g. "3 hosts" or "120 hosts (first 50 shown)". */
export function previewSummary(p: RunPreview): string {
  const n = `${p.host_count} host${p.host_count === 1 ? '' : 's'}`;
  return p.truncated ? `${n} (first ${p.hosts.length} shown)` : n;
}
