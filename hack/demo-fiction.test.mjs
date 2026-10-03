// hack/demo-fiction.test.mjs — the demo film set (hack/demo) is fiction
// throughout: its company, people, customers and vendors are invented, so
// the promo film names no real company. A real vendor's name in a fixture,
// a tile, a shot's caption or a commit the seed makes (seed.sh writes the
// tiles' git history: its messages are in that file) fails here — as
// "Samsara and Geotab" once did, in a telematics commit message.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = fileURLToPath(new URL('.', import.meta.url));
const DEMO = join(HERE, 'demo');

// Real companies and products in the set's world: fleet telematics, route
// planning and dispatch, maps, weather — and workplace suites with their own
// AI assistant. Brand words that are also English words match only as
// written (capitalized); the rest match in any case.
const ANY_CASE = ['Samsara', 'Geotab', 'Verizon Connect', 'KeepTruckin', 'Omnitracs', 'Teletrac', 'Navman', 'Fleetio', 'Lytx',
  'Trimble', 'Zonar', 'GPS Insight', 'Azuga', 'Onfleet', 'Routific', 'OptimoRoute', 'Route4Me', 'Bringg', 'Descartes', 'WorkWave',
  'Mapbox', 'TomTom', 'Google Maps', 'OpenStreetMap', 'weather.gov', 'National Weather Service', 'AccuWeather', 'Tomorrow.io',
  'Larksuite', 'ByteDance', 'Feishu'];
const AS_WRITTEN = ['Motive', 'HERE Technologies', 'Spoke Dispatch', 'Circuit Route'];
// the set's agent is no workplace suite's assistant (company.json agent)
const PERSONA = /\bLark\b(?!span)/;

function* files(dir) {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n);
    if (n === 'node_modules' || n === 'llmreplay' || n === 'measure' || n === 'framecheck') continue; // tools, not the set
    if (statSync(p).isDirectory()) yield* files(p);
    else if (/\.(json|js|mjs|html|md|sh|svg)$/.test(n) && n !== 'measurements.md') yield p;
  }
}

test('the demo film set names no real vendor and no real assistant', () => {
  const hits = [];
  const anyCase = new RegExp(`\\b(${ANY_CASE.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})\\b`, 'i');
  const asWritten = new RegExp(`\\b(${AS_WRITTEN.join('|')})\\b`);
  for (const f of files(DEMO)) {
    const lines = readFileSync(f, 'utf8').split('\n');
    lines.forEach((l, i) => {
      for (const rx of [anyCase, asWritten, PERSONA]) {
        const m = rx.exec(l);
        if (m) hits.push(`${relative(HERE, f)}:${i + 1}: ${m[0]}`);
      }
    });
  }
  assert.deepEqual(hits, [], `real names on the film set:\n${hits.join('\n')}`);
});

test('the checks see the set: its seed, fixtures, tiles and shots', () => {
  const seen = [...files(DEMO)].map((f) => relative(DEMO, f));
  for (const want of ['seed.sh', 'company.json', 'data/model-script.json', 'data/telematics.json', 'tiles/telematics/feeds.js', 'cam/shots/site-agent.js']) {
    assert.ok(seen.includes(want), `${want} is checked`);
  }
  // and a planted name is caught
  assert.ok(new RegExp('\\bSamsara\\b', 'i').test('Telematics feeds: Samsara and Geotab van positions'));
  assert.ok(PERSONA.test('Brief for Lark: an onboarding tracker') && !PERSONA.test('Larkspan'));
});
