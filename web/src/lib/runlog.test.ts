import { describe, expect, it } from 'vitest';
import { LogModel, parseJsonEvent, parseLogEvent, searchLines } from './runlog';

describe('parseLogEvent', () => {
  it('decodes JSON strings and keeps control characters', () => {
    expect(parseLogEvent('"a\\r\\nb\\u001b[0m"')).toBe('a\r\nb\u001b[0m');
  });
  it('ignores malformed or non-string payloads', () => {
    expect(parseLogEvent('not json')).toBe('');
    expect(parseLogEvent('{"a":1}')).toBe('');
  });
});

describe('parseJsonEvent', () => {
  it('returns objects only', () => {
    expect(parseJsonEvent<{ a: number }>('{"a":1}')).toEqual({ a: 1 });
    expect(parseJsonEvent('"x"')).toBeNull();
    expect(parseJsonEvent('{')).toBeNull();
  });
});

describe('LogModel', () => {
  it('splits lines and exposes the unfinished one', () => {
    const m = new LogModel();
    m.append('one\ntw');
    expect(m.lines.map((l) => l.text)).toEqual(['one']);
    expect(m.all().map((l) => l.text)).toEqual(['one', 'tw']);
    m.append('o\n');
    expect(m.lines.map((l) => l.text)).toEqual(['one', 'two']);
    expect(m.partial).toBeNull();
  });

  it('treats CRLF as one newline, also split across chunks', () => {
    const m = new LogModel();
    m.append('a\r');
    m.append('\nb\r\n');
    expect(m.lines.map((l) => l.text)).toEqual(['a', 'b']);
  });

  it('overwrites the line on a bare CR', () => {
    const m = new LogModel();
    m.append('10%\r50%\r100%\n');
    expect(m.lines.map((l) => l.text)).toEqual(['100%']);
  });

  it('carries ANSI style over lines', () => {
    const m = new LogModel();
    m.append('\u001b[31mred\nstill\u001b[0m\nplain\n');
    expect(m.lines[1].segments[0].fg).toBe(m.lines[0].segments[0].fg);
    expect(m.lines[2].segments[0].fg).toBeUndefined();
  });

  it('caps the number of lines', () => {
    const m = new LogModel(3);
    m.append('1\n2\n3\n4\n5\n');
    expect(m.lines.map((l) => l.text)).toEqual(['3', '4', '5']);
    expect(m.dropped).toBe(2);
  });
});

describe('searchLines', () => {
  it('finds case-insensitive matches', () => {
    const m = new LogModel();
    m.append('TASK [Install]\nok\nFAILED install\n');
    expect(searchLines(m.lines, 'install')).toEqual([0, 2]);
    expect(searchLines(m.lines, '')).toEqual([]);
  });
});
