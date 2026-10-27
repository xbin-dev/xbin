/**
 * pair-kit.js — the words, links and arithmetic of adding a phone to the
 * xbin app (D204, /docs/app-tunnel.md; the link and the confirmation code are
 * native/spec/tunnel.md §2 and §5): the code as people compare it, the
 * pairing's states, the enrollment link's address rewrite, the QR code's
 * size, the device limit's words. For <bx-pair> (/vendor/bx-pair.js) and
 * every surface that hosts it: the shell's devices panel and xbind's
 * /xbin/devices page.
 *
 * The server decides everything — what a pairing's state is, whether a
 * phone may replace another; this file only says it.
 *
 * Pure, no imports: hack/pair-kit.test.mjs runs it under node.
 */

// formatSas(v) → "012 345": the six-digit confirmation code (a number or
// a digit string from GET /devices/pairing/{id}), zero-padded and grouped
// by three as the phone shows it; null for anything that isn't one.
export function formatSas(v) {
  if (v == null || typeof v === 'boolean') return null;
  const s = String(v).replace(/\s+/g, '');
  if (!/^\d{1,6}$/.test(s)) return null;
  const p = s.padStart(6, '0');
  return `${p.slice(0, 3)} ${p.slice(3)}`;
}

// The pairing's states as xbind names them, and what the page shows for
// each: a code nobody scanned yet and a phone mid-handshake both wait for
// the phone; a state a newer xbind adds waits too (the poll goes on).
const STATES = Object.freeze({
  minted: 'waiting', waiting: 'waiting', handshaken: 'connecting', connecting: 'connecting',
  confirm: 'confirm', enrolled: 'enrolled', done: 'enrolled',
  rejected: 'rejected', abandoned: 'abandoned', expired: 'expired',
});
export const pairState = (s) => STATES[s] ?? 'waiting';
// the states a pairing never leaves: the poll stops, the host may add again
export const isFinal = (st) => st === 'enrolled' || st === 'rejected' || st === 'abandoned' || st === 'expired';

// phoneOrigin(v): v as an http(s) origin (scheme://host[:port], no path,
// query or credentials; https:// assumed when no scheme is typed) — or null.
// The shell's devices panel has its own copy (it must load on an xbind
// without this file).
export function phoneOrigin(v) {
  const t = String(v ?? '').trim();
  let u;
  try { u = new URL(t.includes('://') ? t : `https://${t}`); } catch { return null; }
  if (!/^https?:$/.test(u.protocol) || !u.hostname || u.username || u.password || u.search || u.hash) return null;
  if (u.pathname !== '/' || /^[a-z]+:\/\/[^/]*\/./i.test(t)) return null;
  return u.origin;
}

// withOrigin(link, origin): the enrollment link with its u= set to origin
// and every other parameter — the code, and a tunnel link's tv, t, k, p, r,
// rq — kept byte for byte, in order. (The address the phone uses; the
// device still signs the origin its enrollment answer names.)
export function withOrigin(link, origin) {
  const s = String(link ?? '');
  const q = s.indexOf('?');
  if (q < 0 || !origin) return s;
  const u = `u=${encodeURIComponent(origin)}`;
  let seen = false;
  const parts = s.slice(q + 1).split('&').map((p) => {
    if (!p.startsWith('u=')) return p;
    seen = true;
    return u;
  });
  if (!seen) parts.unshift(u);
  return `${s.slice(0, q + 1)}${parts.join('&')}`;
}

// linkParams(link) → {u, c, tv, t, k, p, r, rq} as the link carries them
// (decoded); isTunnelLink: a link the pre-D204 app can't use.
export function linkParams(link) {
  const s = String(link ?? '');
  const q = s.indexOf('?');
  const out = {};
  if (q < 0) return out;
  for (const part of s.slice(q + 1).split('&')) {
    const i = part.indexOf('=');
    const k = i < 0 ? part : part.slice(0, i);
    try { out[k] = decodeURIComponent(i < 0 ? '' : part.slice(i + 1)); } catch { out[k] = part.slice(i + 1); }
  }
  return out;
}
export const isTunnelLink = (link) => 'tv' in linkParams(link);

// The QR code's geometry (native/spec/tunnel.md §2: level M, at least 288 px
// wide): a 4-module quiet zone, whole pixels per module and never under 4,
// so a phone camera resolves the longer tunnel link from a laptop screen.
export const QR_MIN_PX = 288;
export const QR_MIN_MODULE = 4;
export const QR_QUIET = 4;
export function qrGeometry(n) {
  const span = n + 2 * QR_QUIET;
  const ppm = Math.max(QR_MIN_MODULE, Math.ceil(QR_MIN_PX / span));
  return { n, span, ppm, px: ppm * span };
}

// qrModel(qrcode, text): the QR code of text at level M as one SVG path
// (a run of dark modules per subpath) plus its geometry. qrcode is the
// vendored qrcode-generator factory (/vendor/qrcode.mjs), passed in so this
// file stays pure.
export function qrModel(qrcode, text) {
  const qr = qrcode(0, 'M');
  qr.addData(text);
  qr.make();
  const n = qr.getModuleCount();
  let d = '';
  for (let r = 0; r < n; r++) {
    for (let c = 0; c < n;) {
      if (!qr.isDark(r, c)) { c++; continue; }
      let run = 1;
      while (c + run < n && qr.isDark(r, c + run)) run++;
      d += `M${c + QR_QUIET} ${r + QR_QUIET}h${run}v1h-${run}z`;
      c += run;
    }
  }
  return { d, ...qrGeometry(n) };
}

// expiresAt(v): a pairing's or a code's expiry as unix seconds — xbind's
// enroll-code answers a number, a pairing may answer RFC 3339; null when
// neither.
export function expiresAt(v) {
  if (typeof v === 'number' && Number.isFinite(v)) return v;
  if (typeof v === 'string' && v) {
    const t = Date.parse(v);
    if (Number.isFinite(t)) return t / 1000;
  }
  return null;
}
// clock(secs) → "4:59"
export function clock(secs) {
  const s = Math.max(0, Math.round(Number(secs) || 0));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

// limitInfo(r): the person's device limit from GET /devices ({devices,
// limit, count}) — {limit, count, at, words: "1 of 3 devices"} — or null
// when this xbind says none (before D204: no limit to show).
export function limitInfo(r) {
  const limit = Number.isInteger(r?.limit) ? r.limit : null;
  if (limit == null) return null;
  const count = Number.isInteger(r?.count) ? r.count : (Array.isArray(r?.devices) ? r.devices.length : 0);
  return { limit, count, at: count >= limit, words: `${count} of ${limit} device${limit === 1 ? '' : 's'}` };
}

// confirmBody(match, replace): POST /devices/pairing/{id}/confirm's body —
// {match}; a match at the limit names the device it replaces.
export function confirmBody(match, replace) {
  return match && replace ? { match: true, replace } : { match: !!match };
}

// endWords(state, reason, error): what a pairing that added nothing says —
// {title, body, tone: 'danger'|'warn'}; null for any other state.
export function endWords(state, reason, error) {
  if (state === 'rejected') {
    switch (reason) {
      case 'mismatch': return { tone: 'danger', title: 'Not added: the codes didn\'t match',
        body: 'Nothing was added, and this QR code no longer works. If your phone showed a different code, someone else may have scanned it: make a new code, and show it to no one else.' };
      case 'expired': return { tone: 'warn', title: 'Not added: the code expired before you confirmed', body: 'Nothing was added. Make a new code and confirm within two minutes of scanning.' };
      case 'limit': return { tone: 'warn', title: 'Not added: you are at your device limit',
        body: 'Remove a device (or pick one for the new phone to replace), then make a new code. An admin can raise your limit.' };
      default: return { tone: 'danger', title: 'Not added', body: error || 'The workspace refused this phone.' };
    }
  }
  if (state === 'abandoned') {
    return { tone: 'warn', title: 'The phone stopped before you confirmed',
      body: 'Nothing was added. Open the xbin app and scan a new code to try again.' };
  }
  if (state === 'expired') return { tone: 'warn', title: 'This code expired', body: 'Nothing was added. Make a new code to try again.' };
  return null;
}

// platformName(p): a device's platform as people write it ("ios" → "iOS").
const PLATFORMS = Object.freeze({ ios: 'iOS', ipados: 'iPadOS', android: 'Android', macos: 'macOS' });
export const platformName = (p) => PLATFORMS[String(p ?? '').toLowerCase()] ?? String(p ?? '');

// ago(t): a unix time in words, coarse ("3 d ago").
export function ago(t, now = Date.now() / 1000) {
  if (!t) return 'never';
  const s = Math.max(0, now - t);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 30 * 86400) return `${Math.round(s / 86400)} d ago`;
  return new Date(t * 1000).toLocaleDateString();
}
