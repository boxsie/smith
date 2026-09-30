import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('../static/theme.js', import.meta.url), 'utf8');
function fixture(saved, dark = false, blocked = false) {
  const events = {}, attributes = {}, element = {dataset: {}, style: {}};
  let ready = false;
  const button = {setAttribute: (key, value) => attributes[key] = value,
    addEventListener: (key, handler) => events[key] = handler};
  const media = {matches: dark, addEventListener: (key, handler) => events.media = handler};
  const storage = {getItem() { if (blocked) throw Error('denied'); return saved; },
    setItem(key, value) { if (blocked) throw Error('denied'); saved = value; }};
  vm.runInNewContext(source, {
    localStorage: storage,
    window: {matchMedia: () => media, addEventListener: (key, handler) => events[key] = handler},
    document: {documentElement: element, getElementById: () => ready ? button : null,
      addEventListener: (key, handler) => events[key] = handler},
  });
  return {element, attributes, media, events, saved: () => saved,
    ready() { ready = true; events.DOMContentLoaded(); }};
}
test('system preference is applied in the head before the button exists', () => {
  const f = fixture(null, true);
  assert.equal(f.element.dataset.theme, 'dark');
  assert.equal(f.element.style.colorScheme, 'dark');
  f.ready(); assert.equal(f.attributes['aria-pressed'], 'true');
  f.media.matches = false; f.events.media();
  assert.equal(f.element.dataset.theme, 'light');
});
test('explicit choice persists and wins over system changes and reload', () => {
  const f = fixture('light', true); f.ready();
  assert.equal(f.element.dataset.theme, 'light');
  f.events.click(); assert.equal(f.saved(), 'dark');
  f.media.matches = false; f.events.media();
  assert.equal(f.element.dataset.theme, 'dark');
  assert.equal(fixture(f.saved()).element.dataset.theme, 'dark');
  f.events.click(); assert.equal(f.saved(), 'light');
});
test('invalid or inaccessible storage cannot break startup or toggle', () => {
  assert.equal(fixture('nonsense', true).element.dataset.theme, 'dark');
  const f = fixture(null, false, true); f.ready(); f.events.click();
  assert.equal(f.element.dataset.theme, 'dark');
  assert.equal(f.attributes['aria-pressed'], 'true');
});
test('other tabs synchronize choices; clearing storage resumes system preference', () => {
  const f = fixture('light', true); f.ready();
  f.events.storage({key: 'unrelated', newValue: 'dark'});
  assert.equal(f.element.dataset.theme, 'light');
  f.events.storage({key: 'smith.theme', newValue: 'dark'});
  assert.equal(f.attributes['aria-pressed'], 'true');
  f.media.matches = false;
  f.events.storage({key: null, newValue: null});
  assert.equal(f.element.dataset.theme, 'light');
});
test('dark palette text pairs clear the 4.5:1 body-text contrast floor', () => {
  const css = readFileSync(new URL('../static/theme.css', import.meta.url), 'utf8');
  const dark = css.match(/:root\[data-theme="dark"\]\s*\{([^}]+)\}/)[1];
  const tokens = Object.fromEntries([...dark.matchAll(/(--[\w-]+):\s*(#[\da-f]{6})/g)].map(m => [m[1], m[2]]));
  const luminance = hex => hex.slice(1).match(/../g).map(v => parseInt(v, 16) / 255)
    .map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4)
    .reduce((sum, v, i) => sum + v * [.2126, .7152, .0722][i], 0);
  for (const [fg, bg] of [['--ink','--face'], ['--muted','--face'], ['--muted','--face-low'],
    ['--action-ink','--action-face'], ['--action-ink','--action-hover'], ['--ink','--selected-face'],
    ['--signal-ink','--face'], ['--danger-ink','--face'], ['--on-signal','--signal'], ['--on-fault','--fault']]) {
    const a = luminance(tokens[fg]), b = luminance(tokens[bg]);
    assert((Math.max(a,b) + .05) / (Math.min(a,b) + .05) >= 4.5, `${fg} on ${bg}`);
  }
});
