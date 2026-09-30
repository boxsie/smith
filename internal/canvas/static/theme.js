// Blocking head script: set the palette before styles/first paint, independently
// of service startup. Only an explicit light/dark choice is persisted.
(() => {
  const key = 'smith.theme';
  const media = window.matchMedia('(prefers-color-scheme: dark)');
  const valid = value => value === 'light' || value === 'dark' ? value : null;
  let preference = null;
  try { preference = valid(localStorage.getItem(key)); } catch {}
  function apply() {
    const theme = preference || (media.matches ? 'dark' : 'light');
    document.documentElement.dataset.theme = theme;
    document.documentElement.style.colorScheme = theme;
    const button = document.getElementById('theme-toggle');
    if (button) {
      button.setAttribute('aria-pressed', String(theme === 'dark'));
      button.title = `switch to ${theme === 'dark' ? 'light' : 'dark'} mode`;
    }
  }
  apply();
  document.addEventListener('DOMContentLoaded', () => {
    document.getElementById('theme-toggle').addEventListener('click', () => {
      preference = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
      try { localStorage.setItem(key, preference); } catch {}
      apply();
    });
    apply();
  });
  media.addEventListener('change', () => { if (!preference) apply(); });
  window.addEventListener('storage', event => {
    if (event.key === key || event.key === null) {
      preference = valid(event.newValue);
      apply();
    }
  });
})();
