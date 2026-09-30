export const MIN_VIEW_SCALE = 0.25;
export const MAX_VIEW_SCALE = 2.5;

export function clampViewScale(scale) {
  return Math.min(MAX_VIEW_SCALE, Math.max(MIN_VIEW_SCALE, scale));
}

export function clientToWorldPoint(clientX, clientY, rect, view) {
  return {
    x: (clientX - rect.left - view.x) / view.scale,
    y: (clientY - rect.top - view.y) / view.scale,
  };
}

export function zoomViewAt(view, clientX, clientY, rect, requestedScale) {
  const scale = clampViewScale(requestedScale);
  const cursorX = clientX - rect.left;
  const cursorY = clientY - rect.top;
  const world = clientToWorldPoint(clientX, clientY, rect, view);
  return {
    x: cursorX - world.x * scale,
    y: cursorY - world.y * scale,
    scale,
  };
}

export function worldPointerDelta(delta, scale) {
  return delta / scale;
}
