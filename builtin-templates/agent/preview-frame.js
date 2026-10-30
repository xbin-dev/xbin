// preview-frame.js — the render pane's frame beyond what agent.js draws (it
// is at its size budget): the zoom of the header's −, % and + (#49), and the
// watch that keeps a report's file links working (#46) — both D207.
import { ZOOMS, clampZoom, stepZoom, zoomLabel, loadZoom, saveZoom } from './model/preview-zoom.js';

// makeZoom wires the zoom to the static frame (#prevframe). The frame is
// scaled from outside: laid out at 1/z of the pane and scaled by z, so the
// report reflows as a browser's zoom would — its srcdoc never reassigned
// (no reload, no lost scroll) and its sandbox untouched. The zoom is kept per
// person and tile (model/preview-zoom.js).
export function makeZoom($) {
  let z = 1;
  let touched = false;
  const apply = () => {
    const s = $('prevframe').style;
    if (z === 1) s.width = s.height = s.transform = s.transformOrigin = '';
    else Object.assign(s, { width: `${100 / z}%`, height: `${100 / z}%`, transform: `scale(${z})`, transformOrigin: '0 0' });
    $('prev-zreset').textContent = zoomLabel(z);
    $('prev-zreset').title = z === 1 ? 'The report at its own size' : 'Back to 100%';
    $('prev-zout').disabled = z <= ZOOMS[0];
    $('prev-zin').disabled = z >= ZOOMS[ZOOMS.length - 1];
  };
  const set = (nz) => { touched = true; z = clampZoom(nz); apply(); saveZoom(z); };
  $('prev-zout').onclick = () => set(stepZoom(z, -1));
  $('prev-zin').onclick = () => set(stepZoom(z, +1));
  $('prev-zreset').onclick = () => set(1);
  apply();
  loadZoom().then((v) => { if (!touched) { z = v; apply(); } });
  return { get zoom() { return z; }, set };
}

// watchFrame calls away() when the frame loads a document it wasn't given:
// a file link answered with a page (an expired link, a file deleted since)
// rather than a download — a download loads nothing. expect() goes before
// each srcdoc assignment (two in a row still load once).
export function watchFrame(frame, away) {
  let expected = 0;
  frame.addEventListener('load', () => { if (expected > 0) expected = 0; else away(); });
  return { expect: () => { expected = 1; } };
}
