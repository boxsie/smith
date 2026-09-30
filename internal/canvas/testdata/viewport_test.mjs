import assert from 'node:assert/strict';
import test from 'node:test';

import {
  MAX_VIEW_SCALE,
  MIN_VIEW_SCALE,
  clampViewScale,
  clientToWorldPoint,
  worldPointerDelta,
  zoomViewAt,
} from '../static/viewport.mjs';

const rect = {left: 100, top: 50};

test('client coordinates invert pan and zoom', () => {
  assert.deepEqual(
    clientToWorldPoint(500, 350, rect, {x: -200, y: 100, scale: 2}),
    {x: 300, y: 100},
  );
});

test('zoom keeps the patch point beneath the cursor', () => {
  const before = {x: -320, y: 75, scale: 0.8};
  const client = {x: 610, y: 420};
  const worldBefore = clientToWorldPoint(client.x, client.y, rect, before);
  const after = zoomViewAt(before, client.x, client.y, rect, 1.6);
  const worldAfter = clientToWorldPoint(client.x, client.y, rect, after);

  assert.deepEqual(worldAfter, worldBefore);
});

test('scale is bounded and pointer deltas remain world-correct', () => {
  assert.equal(clampViewScale(0.01), MIN_VIEW_SCALE);
  assert.equal(clampViewScale(10), MAX_VIEW_SCALE);
  assert.equal(worldPointerDelta(80, 2), 40);
  assert.equal(worldPointerDelta(-20, 0.5), -40);
});
