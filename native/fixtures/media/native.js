// media — a site-inspection tile: tile-relative photos at every height token,
// fill and fit, tap to switch the hero photo, Quick Look previews, a small
// data: image (the inspector's signature), and status icons in every tone.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

let ins = null;
let hero = null;

const findingIcon = { ok: 'check', minor: 'info', major: 'warning', blocker: 'error', na: 'minus' };
const findingTone = { ok: 'ok', minor: 'accent', major: 'warn', blocker: 'danger', na: 'muted' };

async function load() {
  const r = await xbin.fetch(`/api/${xbin.self}/inspections/218`);
  ins = await r.json();
  hero = ins.photos[0];
  paint();
}

const paint = () => render(!ins ? nothing : html`
  <screen title=${ins.address} subtitle=${`Inspection #${ins.id} · ${ins.date}`} style="scroll">
    <stack gap="m">
      <image src=${hero.src} alt=${hero.caption} aspect="fill" height="l" preview/>
      <text style="footnote" tone="muted">${hero.caption}</text>
      <stack axis="h" gap="s">
        ${repeat(ins.photos, (p) => p.src, (p) => html`
          <image src=${p.thumb} alt=${p.caption} aspect="fill" height="xs" @tap=${() => { hero = p; paint(); }}/>`)}
      </stack>
      <divider/>
      <text style="headline">Findings</text>
      ${repeat(ins.findings, (f) => f.id, (f) => html`
        <stack axis="h" gap="s" align="center">
          <icon name=${findingIcon[f.level]} tone=${findingTone[f.level]}/>
          <text>${f.text}</text>
        </stack>`)}
      <divider/>
      <text style="headline">Roof, north side</text>
      <image src="photos/218/roof-north-detail.jpg" alt="Cracked ridge tiles, north side" aspect="fill" height="m" preview/>
      <text style="headline">Ground floor plan</text>
      <image src=${ins.plan} alt="Ground floor plan with the measured rooms" aspect="fit" height="xl" preview/>
      <stack axis="h" gap="m" align="end">
        <stack gap="xs">
          <text style="caption" tone="muted">Signed by</text>
          <text>${ins.inspector}</text>
        </stack>
        <spacer/>
        <image src=${ins.signature} alt=${`Signature of ${ins.inspector}`} aspect="fit" height="s"/>
      </stack>
    </stack>
  </screen>`);

load();
