// live-status.js — the live pane's status strip (D135 diagnostics). Check
// fetches the pane's own URL — /api/~<ticket>/… — as the frame does (a plain
// fetch: the ticket rides in the URL; no credentials) and says what came
// back: the HTTP status and type, a refusal and what to do about it
// (model/live.js probeWords). The frame is an opaque origin this page can't
// read, so the strip checks as the frame loads, and a failed answer is said
// here with the frame hidden — never a blank frame.
import { probeWords } from './model/live.js';

let cur = null; // the mounted strip: {strip, frame, src}

// checkLive: what src answers now — {ok, status, contentType, refusal, error, ms}.
export async function checkLive(src, timeoutMs = 20000) {
  const t0 = performance.now();
  const ms = () => Math.round(performance.now() - t0);
  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(), timeoutMs);
  try {
    const r = await fetch(src, { cache: 'no-store', credentials: 'omit', signal: ac.signal });
    const p = { ok: r.ok, status: r.status, contentType: r.headers.get('content-type') || '', ms: ms() };
    if (r.ok) { try { await r.body?.cancel(); } catch { /* read enough */ } return p; }
    const text = (await r.text().catch(() => '')).slice(0, 4000);
    try { const j = JSON.parse(text); p.error = j.error || ''; p.refusal = j.refusal || ''; } catch { p.error = text.trim().slice(0, 300); }
    return p;
  } catch (e) {
    return { ok: false, status: 0, ms: ms(), error: ac.signal.aborted ? `no answer within ${timeoutMs / 1000} s` : (e.message || String(e)) };
  } finally { clearTimeout(timer); }
}

// mountLive puts the strip on the pane over a live frame (agent.js openLive)
// and checks at once; unmountLive takes it away (dropLive).
export function mountLive(pane, frame, src) {
  unmountLive();
  const strip = Object.assign(document.createElement('div'), { className: 'lstrip', id: 'live-strip' });
  const out = Object.assign(document.createElement('span'), { className: 'lsout', textContent: 'checking…' });
  const btn = Object.assign(document.createElement('button'), { className: 'btn ghost btnsm', id: 'live-check', textContent: 'Check',
    title: 'Ask the page\'s URL what it answers now (status, type, refusal)' });
  const msg = Object.assign(document.createElement('div'), { className: 'lsmsg', hidden: true });
  strip.append(out, btn);
  pane.querySelector('.phd').after(strip);
  frame.after(msg);
  const me = cur = { strip, msg };
  const run = async () => {
    btn.disabled = true;
    out.textContent = 'checking…';
    const p = await checkLive(src);
    if (cur !== me) return;
    btn.disabled = false;
    const w = probeWords(p);
    strip.dataset.tone = w.tone;
    out.textContent = out.title = w.text;
    msg.textContent = w.hint ? `This page didn't load: ${w.hint}.` : '';
    msg.hidden = p.ok;
    if (frame.hidden && p.ok) frame.src = src; // it came back: load it again
    frame.hidden = !p.ok;
  };
  btn.onclick = run;
  run();
}

export function unmountLive() {
  if (!cur) return;
  cur.strip.remove();
  cur.msg.remove();
  cur = null;
}
