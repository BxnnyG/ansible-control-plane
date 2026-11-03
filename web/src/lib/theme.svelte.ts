export type Theme = 'light' | 'dark';

const KEY = 'acp-theme';

function systemTheme(): Theme {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function storedTheme(): Theme | null {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'light' || v === 'dark' ? v : null;
  } catch {
    return null;
  }
}

class ThemeState {
  current = $state<Theme>('light');

  init() {
    this.current = storedTheme() ?? systemTheme();
  }

  toggle() {
    this.current = this.current === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = this.current;
    try {
      localStorage.setItem(KEY, this.current);
    } catch {
      /* storage unavailable: choice applies for this session only */
    }
  }
}

export const theme = new ThemeState();
