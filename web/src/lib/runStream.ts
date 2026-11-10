/** Minimal EventSource surface used here, so tests can inject a fake. */
export interface StreamSource {
  readyState: number;
  onopen: ((e: Event) => void) | null;
  onerror: ((e: Event) => void) | null;
  addEventListener(type: string, listener: (e: Event) => void): void;
  close(): void;
}

export type StreamFactory = (url: string) => StreamSource;

export interface RunLogHandlers {
  onOpen(): void;
  /** `closed` is true when the browser gave up (no automatic reconnect will follow). */
  onError(closed: boolean): void;
  onLog(data: string): void;
  onStatus(data: string): void;
  onEnd(): void;
}

const CLOSED = 2; // EventSource.CLOSED

const browserFactory: StreamFactory = (url) =>
  new EventSource(url, { withCredentials: true }) as unknown as StreamSource;

/**
 * Opens one SSE connection for a run log. The stream is closed on `end` and when the browser
 * reports a closed connection, so a finished run is never fetched again. Returns a closer.
 */
export function openRunLog(
  url: string,
  h: RunLogHandlers,
  factory: StreamFactory = browserFactory,
): () => void {
  const es = factory(url);
  const data = (e: Event) => (e as MessageEvent<string>).data;
  es.onopen = () => h.onOpen();
  es.onerror = () => {
    const closed = es.readyState === CLOSED;
    if (closed) es.close();
    h.onError(closed);
  };
  es.addEventListener('log', (e) => h.onLog(data(e)));
  es.addEventListener('status', (e) => h.onStatus(data(e)));
  es.addEventListener('end', () => {
    es.close(); // stops the browser's automatic reconnect
    h.onEnd();
  });
  return () => es.close();
}
