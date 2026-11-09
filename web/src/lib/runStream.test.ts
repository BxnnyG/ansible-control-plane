import { describe, expect, it } from 'vitest';
import { openRunLog, type RunLogHandlers, type StreamSource } from './runStream';

class FakeSource implements StreamSource {
  static all: FakeSource[] = [];
  readyState = 1;
  onopen: ((e: Event) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  closed = 0;
  private listeners = new Map<string, ((e: Event) => void)[]>();
  constructor(readonly url: string) {
    FakeSource.all.push(this);
  }
  addEventListener(type: string, l: (e: Event) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), l]);
  }
  close() {
    this.closed++;
    this.readyState = 2;
  }
  emit(type: string, data = '{}') {
    for (const l of this.listeners.get(type) ?? []) l({ data } as unknown as Event);
  }
}

function setup() {
  FakeSource.all = [];
  const events: string[] = [];
  const h: RunLogHandlers = {
    onOpen: () => events.push('open'),
    onError: (c) => events.push(c ? 'error-closed' : 'error-retry'),
    onLog: () => events.push('log'),
    onStatus: () => events.push('status'),
    onEnd: () => events.push('end'),
  };
  const close = openRunLog('/runs/1/log', h, (u) => new FakeSource(u));
  return { events, close, es: () => FakeSource.all[0] };
}

describe('openRunLog', () => {
  it('uses one connection for a whole run lifecycle', () => {
    const { events, es } = setup();
    es().onopen?.(new Event('open'));
    for (let i = 0; i < 20; i++) {
      es().emit('log');
      es().emit('status');
    }
    es().emit('end');
    expect(FakeSource.all).toHaveLength(1);
    expect(events.filter((e) => e === 'status')).toHaveLength(20);
    expect(events.at(-1)).toBe('end');
    expect(es().readyState).toBe(2);
  });

  it('closes on end so the browser cannot reconnect', () => {
    const { es } = setup();
    es().emit('end');
    expect(es().closed).toBe(1);
  });

  it('keeps the connection on transient errors but reports closed ones', () => {
    const { events, es } = setup();
    es().readyState = 0; // CONNECTING: browser retries by itself
    es().onerror?.(new Event('error'));
    expect(es().closed).toBe(0);
    es().readyState = 2;
    es().onerror?.(new Event('error'));
    expect(events.filter((e) => e.startsWith('error'))).toEqual(['error-retry', 'error-closed']);
  });
});
