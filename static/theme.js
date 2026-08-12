const storageKey = 'leapview-color-mode';
const root = document.documentElement;
const media = window.matchMedia?.('(prefers-color-scheme: dark)');
const nextModes = { system: 'light', light: 'dark', dark: 'system' };
const themes = {
  system: { colorMode: 'auto', lightTheme: 'light', darkTheme: 'dark' },
  light: { colorMode: 'light', lightTheme: 'light', darkTheme: 'dark' },
  dark: { colorMode: 'dark', lightTheme: 'light', darkTheme: 'dark' },
  dark_dimmed: { colorMode: 'dark', lightTheme: 'light', darkTheme: 'dark_dimmed' },
  light_colorblind: { colorMode: 'light', lightTheme: 'light_colorblind', darkTheme: 'dark' },
  dark_colorblind: { colorMode: 'dark', lightTheme: 'light', darkTheme: 'dark_colorblind' },
  light_tritanopia: { colorMode: 'light', lightTheme: 'light_tritanopia', darkTheme: 'dark' },
  dark_tritanopia: { colorMode: 'dark', lightTheme: 'light', darkTheme: 'dark_tritanopia' },
};
const modeLabels = {
  system: 'System theme',
  light: 'Light default',
  dark: 'Dark default',
  dark_dimmed: 'Soft dark',
  light_colorblind: 'Light protanopia and deuteranopia',
  dark_colorblind: 'Dark protanopia and deuteranopia',
  light_tritanopia: 'Light tritanopia',
  dark_tritanopia: 'Dark tritanopia',
};
let lastAppliedEvent = 0;

window.addEventListener('unhandledrejection', (event) => {
  const reason = event.reason;
  const message = typeof reason?.message === 'string' ? reason.message : '';
  if (reason?.name === 'AbortError' && message.includes('Transition was skipped')) {
    event.preventDefault();
    return;
  }
  if (reason?.name === 'InvalidStateError' && message.includes('Transition was aborted')) {
    event.preventDefault();
  }
});

function storedMode() {
  const preference = root.dataset.themePreference;
  if (Object.hasOwn(themes, preference)) return preference;
  const saved = localStorage.getItem(storageKey);
  if (Object.hasOwn(themes, saved)) return saved;
  return 'system';
}

function setMode(mode, options = {}) {
  const next = Object.hasOwn(themes, mode) ? mode : 'system';
  const theme = themes[next];
  const resolved = theme.colorMode === 'auto' ? (media?.matches ? 'dark' : 'light') : theme.colorMode;
  root.dataset.colorMode = theme.colorMode;
  root.dataset.themePreference = next;
  root.dataset.lightTheme = theme.lightTheme;
  root.dataset.darkTheme = theme.darkTheme;
  root.style.colorScheme = resolved;
  localStorage.setItem(storageKey, next);
  for (const button of document.querySelectorAll('[data-theme-value]')) {
    button.setAttribute('aria-pressed', String(button.dataset.themeValue === next));
  }
  for (const toggle of document.querySelectorAll('[data-theme-toggle]')) {
    const nextMode = nextModes[next] || 'system';
    const label = `${modeLabels[next] || 'System theme'}. Switch to ${modeLabels[nextMode] || 'system theme'}.`;
    toggle.dataset.themeMode = next;
    toggle.setAttribute('aria-label', label);
    toggle.setAttribute('title', label);
    for (const icon of toggle.querySelectorAll('[data-theme-icon]')) {
      const active = icon.dataset.themeIcon === next;
      icon.hidden = !active;
      icon.classList.toggle('hidden', !active);
    }
  }

  if (options.notify !== false) {
    const eventID = ++lastAppliedEvent;
    requestAnimationFrame(() => {
      if (eventID !== lastAppliedEvent) return;
      document.dispatchEvent(new CustomEvent('leapview-theme-applied', { detail: { mode: next, resolvedMode: resolved } }));
    });
  }
}

document.addEventListener('click', (event) => {
  const button = event.target.closest?.('[data-theme-value]');
  if (button) {
    setMode(button.dataset.themeValue);
    return;
  }

  const toggle = event.target.closest?.('[data-theme-toggle]');
  if (toggle) setMode(nextModes[storedMode()] || 'system');
});

document.addEventListener('leapview-theme-change', (event) => {
  setMode(event.detail?.mode);
});

media?.addEventListener?.('change', () => {
  if (storedMode() === 'system') setMode('system');
});

setMode(storedMode(), { notify: false });
