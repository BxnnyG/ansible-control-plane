import { describe, expect, it } from 'vitest';
import { AnsiParser, ansiToSegments, color256, segmentStyle } from './ansi';

describe('ansiToSegments', () => {
  it('returns plain text unchanged', () => {
    expect(ansiToSegments('hello')).toEqual([{ text: 'hello' }]);
  });

  it('applies colors and resets', () => {
    const s = ansiToSegments('a\u001b[31mred\u001b[0mb');
    expect(s.map((x) => x.text)).toEqual(['a', 'red', 'b']);
    expect(s[1].fg).toBeTruthy();
    expect(s[2].fg).toBeUndefined();
  });

  it('handles bold, underline and bright colors', () => {
    const [s] = ansiToSegments('\u001b[1;4;92mx');
    expect(s.bold).toBe(true);
    expect(s.underline).toBe(true);
    expect(s.fg).toBeTruthy();
  });

  it('supports 256 and truecolor', () => {
    expect(ansiToSegments('\u001b[38;5;196mx')[0].fg).toBe(color256(196));
    expect(ansiToSegments('\u001b[38;2;1;2;3mx')[0].fg).toBe('rgb(1, 2, 3)');
    expect(ansiToSegments('\u001b[48;2;300;2;3mx')[0].bg).toBeUndefined();
  });

  it('drops non-SGR sequences', () => {
    expect(
      ansiToSegments('a\u001b[2Kb\u001b[1Ac')
        .map((x) => x.text)
        .join(''),
    ).toBe('abc');
  });

  it('keeps style and incomplete sequences across chunks', () => {
    const p = new AnsiParser();
    expect(p.parse('\u001b[3')).toEqual([]);
    const s = p.parse('2mgreen');
    expect(s[0].text).toBe('green');
    expect(s[0].fg).toBeTruthy();
    expect(p.parse('more')[0].fg).toBe(s[0].fg);
  });

  it('never emits markup: HTML stays literal text', () => {
    const s = ansiToSegments('<img src=x onerror=alert(1)>');
    expect(s).toEqual([{ text: '<img src=x onerror=alert(1)>' }]);
  });

  it('produces CSS only from fixed values', () => {
    const [s] = ansiToSegments('\u001b[1;31mx');
    expect(segmentStyle(s)).toMatch(/^color:#[0-9a-f]{6};font-weight:600$/);
  });
});
