// web/signin-scan.js — reading a coding-agent CLI's sign-in as it runs, for
// the Agent tab's guided sign-in (agent-signin.js, D178). The twin of
// sdk/acp/signin.go's Signin.Scan: the same rules, tested against the same
// captures of Claude Code's real output (sdk/acp/testdata/signin, by
// hack/signin-scan.test.mjs under `make js-test`). Pure: no DOM, no fetch.
//
// `spec` is a provider's `signin` from GET /api/xbin/agent/providers
// (docs/protocol.md): {command, url, hosts, code, invalid?, done, fail, …}.

// allowedURL(spec, u): may u be offered as the sign-in page — https, no
// user part, on one of spec.hosts (or a subdomain of one), matching
// spec.url whole.
export function allowedURL(spec, u) {
  let p;
  try { p = new URL(u); } catch { return false; }
  if (p.protocol !== 'https:' || p.username || p.password || !p.hostname) return false;
  const host = p.hostname.toLowerCase();
  if (!(spec.hosts || []).some((h) => { h = String(h).toLowerCase(); return host === h || host.endsWith('.' + h); })) return false;
  try { return new RegExp('^(?:' + spec.url + ')$').test(u); } catch { return false; }
}

const URL_CHAR = /^[A-Za-z0-9\-._~:/?#[\]@!$&'()*+,;=%]$/;
const allURL = (s) => s !== '' && [...s].every((c) => URL_CHAR.test(c));
const width = (s) => [...s].length;

// screenText(s) → {lines, links}: CSI sequences go (a cursor move along the
// line is a space — a TUI spaces its words that way), OSC strings go (an
// OSC 8 link's target is kept), CR LF and a lone CR end a line, other
// controls go. An escape cut off at the end is dropped (the next read has it).
export function screenText(s) {
  const lines = [], links = [];
  let cur = '';
  const n = s.length;
  for (let i = 0; i < n; i++) {
    const c = s[i], k = s.charCodeAt(i);
    if (c === '\x1b' && i + 1 < n) {
      const t = s[i + 1];
      if (t === '[') {
        let j = i + 2;
        while (j < n && (s.charCodeAt(j) < 0x40 || s.charCodeAt(j) > 0x7e)) j++;
        if (j < n && (s[j] === 'G' || s[j] === 'C') && cur) cur += ' ';
        i = j;
      } else if (t === ']' || t === 'P' || t === '_' || t === '^' || t === 'X') {
        let j = i + 2;
        while (j < n && s[j] !== '\x07' && !(s[j] === '\x1b' && s[j + 1] === '\\')) j++;
        if (t === ']' && j < n) {
          const body = s.slice(i + 2, j);
          if (body.startsWith('8;')) {
            const at = body.indexOf(';', 2);
            if (at >= 0 && body.slice(at + 1)) links.push(body.slice(at + 1));
          }
        }
        if (j < n && s[j] === '\x1b') j++;
        i = j;
      } else if ('()*+# %'.includes(t)) i += 2;
      else i++;
    } else if (c === '\x1b') {
      // a lone ESC at the end
    } else if (c === '\r') {
      while (s[i + 1] === '\r') i++;
      if (s[i + 1] === '\n') i++;
      lines.push(cur); cur = '';
    } else if (c === '\n') {
      lines.push(cur); cur = '';
    } else if (c === '\t') {
      cur += ' ';
    } else if (k < 0x20 || k === 0x7f) {
      // other controls
    } else {
      cur += c;
    }
  }
  if (cur) lines.push(cur);
  return { lines, links };
}

// the newest acceptable URL in the text: one running to the end of its line
// continues on the next lines while they are URL characters alone and the
// line before was as wide as the first (a hard wrap breaks every row at
// one width; the last row is shorter)
function textURL(spec, lines) {
  let re;
  try { re = new RegExp(spec.url); } catch { return ''; }
  let found = '';
  lines.forEach((l, i) => {
    const at = l.indexOf('https://');
    if (at < 0) return;
    let cand = l.slice(at);
    const end = [...cand].findIndex((c) => !URL_CHAR.test(c));
    if (end >= 0) cand = [...cand].slice(0, end).join('');
    else {
      const w = width(l);
      for (let j = i + 1; j < lines.length; j++) {
        const next = lines[j].trim();
        if (!allURL(next)) break;
        cand += next;
        if (width(lines[j].replace(/ +$/, '')) < w) break;
      }
    }
    const m = cand.match(re);
    if (m && allowedURL(spec, m[0])) found = m[0];
  });
  return found;
}

// scanSignin(spec, text) → {url, link, code, invalid, done, failed, last}: what a
// sign-in has printed so far (all of it, as text) — the sign-in page (an
// OSC 8 link's target first — link says it was one, whole as printed;
// text may still be arriving), whether it asks for the code, how many codes
// it refused as malformed, whether it signed in, the line that said it
// failed, and the last line of text.
export function scanSignin(spec, text) {
  const { lines, links } = screenText(text);
  const st = { url: '', link: false, code: false, invalid: 0, done: false, failed: '', last: '' };
  for (let i = links.length - 1; i >= 0 && !st.url; i--) if (allowedURL(spec, links[i])) { st.url = links[i]; st.link = true; }
  if (!st.url) st.url = textURL(spec, lines);
  for (const l of lines) {
    if (spec.code && l.includes(spec.code)) st.code = true;
    if (spec.invalid) st.invalid += l.split(spec.invalid).length - 1;
    if (spec.done && l.includes(spec.done)) st.done = true;
    if (spec.fail && !st.failed) { const at = l.indexOf(spec.fail); if (at >= 0) st.failed = l.slice(at).trim(); }
    if (l.trim()) st.last = l.trim();
  }
  return st;
}

// The most a reader keeps: a full-screen sign-in redraws a spinner for as
// long as it waits; the URL, once found, is remembered.
const KEEP = 256 * 1024;

// SigninReader: feed it a terminal's bytes as they come (push), read
// what the sign-in said (state). reset() starts over (a reattach replays
// the scrollback from the start).
export class SigninReader {
  constructor(spec) { this.spec = spec; this.reset(); }
  reset() { this._dec = new TextDecoder(); this._text = ''; this._url = ''; this._link = false; this.state = scanSignin(this.spec, ''); }
  push(chunk) {
    this._text += typeof chunk === 'string' ? chunk : this._dec.decode(chunk, { stream: true });
    if (this._text.length > KEEP) this._text = this._text.slice(-KEEP / 2);
    const st = scanSignin(this.spec, this._text);
    if (st.url) { this._url = st.url; this._link = st.link; } else { st.url = this._url; st.link = this._link; }
    this.state = st;
    return st;
  }
}

// typedLine(spec): what the guided sign-in types into its terminal — the
// command, then exit, so the session ends (and says so) when the CLI does,
// even when the CLI is missing. The leading space keeps it out of a shell
// history that ignores such lines (bash's ignorespace).
export const typedLine = (spec) => ` ${spec.command}; exit\r`;

// cleanCode(s): a pasted code as the CLI reads it — one line, nothing but
// what a code is made of (a paste may carry spaces, a newline, or a
// trailing control character).
export const cleanCode = (s) => String(s || '').replace(/[\s\x00-\x1f\x7f]/g, '');
