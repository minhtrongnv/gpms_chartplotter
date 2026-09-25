// wheel-zoom.mjs — ECDIS-style discrete viewing-scale zoom.
//
// The semantic state is the physical 1:N viewing scale. MapLibre zoom is only the
// camera coordinate used to render that scale. Wheel/trackpad input therefore
// advances through the canonical ECDIS/S-101 display-scale ladder instead of
// exposing arbitrary fractional zoom states.
//
// Camera motion between two scale stops is animated, but SCAMIN/dataset selection
// is committed by the chart canvas after moveend. This keeps old tiles on screen
// during the transition and avoids symbol/chart flicker at scale boundaries.

import {
  FLOOR_GIVE,
  scaleDenomPhysical,
  zoomForScalePhysical,
  stepDisplayScale,
} from "../lib/util.mjs";

const GESTURE_GAP_MS = 220;
const WHEEL_THRESHOLD_PX = 72;
const STEP_ANIMATION_MS = 150;

export class WheelZoom {
  constructor({ map, getDetent, getFloor, getAnchor, getPxPitch } = {}) {
    this._map = map;
    this._getDetent = getDetent || (() => null); // retained API; chartplotter currently disables it
    this._getFloor = getFloor || (() => map.getMaxZoom() - FLOOR_GIVE);
    this._getAnchor = getAnchor || (() => null);
    this._getPxPitch = getPxPitch || (() => undefined);

    this._targetScale = null;
    this._accum = 0;
    this._lastTs = 0;
    this._lastDir = 0;

    this._canvas = map.getCanvasContainer();
    this._onWheel = (e) => this._wheel(e);
    map.scrollZoom.disable();
    this._canvas.addEventListener("wheel", this._onWheel, { passive: false });
  }

  destroy() {
    if (this._canvas) this._canvas.removeEventListener("wheel", this._onWheel, { passive: false });
    if (this._map && this._map.scrollZoom) this._map.scrollZoom.enable();
  }

  _wheel(e) {
    e.preventDefault();

    let d = e.deltaY;
    if (e.deltaMode === 1) d *= 28;
    else if (e.deltaMode === 2) d *= 400;
    if (!d) return;

    const now = e.timeStamp;
    const dir = d < 0 ? -1 : 1; // -1 zoom in, +1 zoom out
    const newGesture = !this._lastTs || (now - this._lastTs) > GESTURE_GAP_MS;
    this._lastTs = now;

    const map = this._map;
    const center = map.getCenter();
    const pitch = this._getPxPitch();

    if (newGesture || dir !== this._lastDir || this._targetScale == null) {
      // Re-base on the live physical denominator. If it sits between two
      // canonical stops (initial load, pinch/flyTo, restored view), the first wheel
      // action lands on the adjacent stop in the requested direction rather than
      // skipping over it.
      this._targetScale = scaleDenomPhysical(map.getZoom(), center.lat, pitch);
      this._accum = 0;
    }
    this._lastDir = dir;
    this._accum += d;

    if (Math.abs(this._accum) < WHEEL_THRESHOLD_PX) return;

    // Consume one semantic step at a time. Very large mouse-wheel deltas may
    // leave enough residual input for a following event, but never skip several
    // chart scales in one frame.
    this._accum -= Math.sign(this._accum) * WHEEL_THRESHOLD_PX;

    const zoomingIn = dir < 0;
    let nextScale = stepDisplayScale(this._targetScale, zoomingIn);
    let nextZoom = zoomForScalePhysical(nextScale, center.lat, pitch);

    const minZoom = map.getMinZoom();
    const floor = this._getFloor();
    const hardMax = floor + FLOOR_GIVE;
    nextZoom = Math.max(minZoom, Math.min(nextZoom, hardMax));

    // If the physical floor prevents reaching the requested fine step, keep the
    // semantic target synchronized with the scale actually reachable there.
    if (zoomingIn && nextZoom >= floor) {
      nextZoom = floor;
      nextScale = scaleDenomPhysical(floor, center.lat, pitch);
    }

    this._targetScale = nextScale;

    const around = this._getAnchor() || this._cursorLngLat(e);
    map.easeTo({
      zoom: nextZoom,
      around,
      duration: STEP_ANIMATION_MS,
      essential: true,
    });
  }

  _cursorLngLat(e) {
    const r = this._canvas.getBoundingClientRect();
    return this._map.unproject([e.clientX - r.left, e.clientY - r.top]);
  }
}
