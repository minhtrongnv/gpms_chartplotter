// calibration.mjs — OpenCPN-style physical display calibration for the web.
//
// OpenCPN derives pixels/mm from the operating system's reported physical monitor
// width and offers a manual width override. A normal browser does not expose EDID or
// trustworthy physical millimetres, so we use the CSS reference pixel as the
// automatic fallback and offer the same manual physical-screen-width concept.
//
// The resulting CSS-pixel pitch drives BOTH:
//   * true on-screen chart scale (HUD / go-to-scale / SCAMIN / overscale), and
//   * physical S-52 feature sizes (symbols / lines / text / patterns).
//
// It is deliberately local to this browser/screen and is never server-shared.

import { DEFAULT_PX_PITCH_MM, clampPxPitch } from "../lib/util.mjs";

const REF_MM = 5;

const screenCssWidth = () => {
  const n = Number(globalThis.screen && globalThis.screen.width);
  if (n > 0) return n;
  return Math.max(1, document.documentElement.clientWidth || window.innerWidth || 1);
};

const boxPx = (pitch) => Math.max(1, Math.round(REF_MM / pitch));

export function calibrationContribution(app) {
  return {
    id: "calibration",
    tab: "general",
    order: 0.5,
    group: "Physical screen",
    render: (host) => renderCalibration(host, app),
  };
}

function renderCalibration(host, app) {
  const calibrated = typeof app._pxPitch === "number" && app._pxPitch > 0;
  const pitch = clampPxPitch(calibrated ? app._pxPitch : undefined);
  const cssWidth = screenCssWidth();
  const physicalWidth = pitch * cssWidth;
  const defaultWidth = DEFAULT_PX_PITCH_MM * cssWidth;
  const ppm = 1 / pitch;
  const px = boxPx(pitch);

  host.innerHTML = `
    <style>
      .cal { max-width: 34rem; }
      .cal__intro { font-size:.9rem; line-height:1.45; margin:0 0 .9rem; }
      .cal__mode { display:flex; gap:1rem; flex-wrap:wrap; margin:.25rem 0 .8rem; }
      .cal__row { display:flex; gap:.6rem; align-items:center; flex-wrap:wrap; margin:.45rem 0; }
      .cal__row input[type="number"] { width:7rem; }
      .cal__btns { display:flex; gap:.5rem; margin:.7rem 0; }
      .cal__meta { font-size:.8rem; line-height:1.45; opacity:.78; margin:.25rem 0; }
      .cal__verify { display:flex; align-items:center; gap:.65rem; margin-top:.8rem; }
      .cal__box { background:#1b1b1b; outline:1px solid #888; flex:0 0 auto; }
      .cal__warn { font-size:.78rem; line-height:1.4; margin-top:.75rem; opacity:.78; }
    </style>
    <div class="cal">
      <p class="cal__intro">
        OpenCPN computes true chart scale from the monitor's physical width. Browsers
        cannot reliably read the monitor EDID/size, so <b>Browser estimate</b> uses the
        standard CSS reference pixel. For OpenCPN-like 1:N scale, choose
        <b>Manual width</b> and enter the actual visible screen width in millimetres.
      </p>

      <div class="cal__mode">
        <label><input type="radio" name="cal-mode" value="auto" ${calibrated ? "" : "checked"}> Browser estimate</label>
        <label><input type="radio" name="cal-mode" value="manual" ${calibrated ? "checked" : ""}> Manual width</label>
      </div>

      <div class="cal__row">
        <label for="cal-screen-mm">Physical screen width</label>
        <input id="cal-screen-mm" type="number" min="50" max="3000" step="1"
               value="${Math.round(physicalWidth)}" ${calibrated ? "" : "disabled"}>
        <span>mm</span>
      </div>

      <div class="cal__btns">
        <button id="cal-apply" type="button">Apply</button>
        <button id="cal-reset" type="button">Use browser estimate</button>
      </div>

      <p class="cal__meta">
        Browser screen width: <b>${cssWidth.toFixed(0)} CSS px</b><br>
        Effective physical width: <b>${physicalWidth.toFixed(0)} mm</b>
        ${calibrated ? "(manual)" : `(estimated ${defaultWidth.toFixed(0)} mm)`}<br>
        Pixel pitch: <b>${pitch.toFixed(4)} mm/CSS px</b> ·
        <b>${ppm.toFixed(2)} CSS px/mm</b>
      </p>

      <div class="cal__verify">
        <div class="cal__box" style="width:${px}px;height:${px}px"></div>
        <span class="cal__meta">Verification square: this should measure <b>5 mm</b> with a ruler.</span>
      </div>

      <p class="cal__warn">
        This setting is per browser/screen. Moving the window to a monitor with a
        different physical pixel density requires a different calibration. Page zoom
        should normally be 100% when comparing against OpenCPN.
      </p>
    </div>`;

  const widthInput = host.querySelector("#cal-screen-mm");
  const radios = [...host.querySelectorAll('input[name="cal-mode"]')];
  const syncMode = () => {
    const manual = radios.find((r) => r.checked)?.value === "manual";
    widthInput.disabled = !manual;
  };
  radios.forEach((r) => r.addEventListener("change", syncMode));

  host.querySelector("#cal-apply").addEventListener("click", () => {
    const mode = radios.find((r) => r.checked)?.value || "auto";
    if (mode === "auto") {
      app.setPxPitch(undefined);
    } else {
      const mm = parseFloat(widthInput.value);
      if (!(mm >= 50 && mm <= 3000)) return;
      app.setPxPitch(mm / cssWidth);
    }
    renderCalibration(host, app);
  });

  host.querySelector("#cal-reset").addEventListener("click", () => {
    app.setPxPitch(undefined);
    renderCalibration(host, app);
  });
}

export { DEFAULT_PX_PITCH_MM };
