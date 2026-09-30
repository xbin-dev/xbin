// native/hosted.js — a non-secure (hosted) conversation in the native view
// (model/hosted.js; API.md "Non-secure conversations"), as the web's
// hosted-ui.js draws it in the app's vocabulary: the warning — whose private
// resources it uses, who can read it — heads its transcript; the composer
// stays locked until "Start anyway" (a button in the composer) in this app
// session, while a wider audience waits for its host (who confirms or
// declines there), and once hosting ended (continue it without the host).
// Nothing for any other conversation.
import { html, nothing } from '/vendor/xb-native.js';
import { jbody } from '/vendor/bx-kit.js';
import { homeApi } from '../model/home-api.js';
import { hostingOf, lockOf, readers, exposed } from '../model/hosted.js';
import { ctx, guard } from './ui.js';

const started = new Set(); // hosted conversations started in this app session

const me = () => (ctx.app.me && ctx.app.me.user) || '';

/** hostedNoticeTpl: the warning at the head of a hosted conversation's transcript. */
export function hostedNoticeTpl(v) {
  const h = hostingOf(v);
  if (!h) return nothing;
  const lk = lockOf(v, me(), started);
  const who = readers(v.acl).join('; ');
  return html`<notice tone="warn" title="⚠ Not private" text=${`This conversation uses ${exposed(h)}. Who can read it: ${who}.${lk.locked ? ' ' + lk.why + '.' : ''}`}/>`;
}

/** hostedComposer: the composer's state (model/rules.js composer) for a hosted conversation. */
export function hostedComposer(c, v) {
  const lk = lockOf(v, me(), started);
  return lk && lk.locked ? { ...c, disabled: true, placeholder: lk.why } : c;
}

/** hostedButtonsTpl: the composer's buttons for a locked hosted conversation. */
export function hostedButtonsTpl(v) {
  const lk = lockOf(v, me(), started);
  if (!lk || !lk.locked) return nothing;
  const app = ctx.app;
  const h = hostingOf(v);
  const call = (home, path, body) => homeApi(home, path, jbody(body, 'POST'));
  switch (lk.kind) {
    case 'start':
      return html`<button icon="shield" role="primary" @tap=${() => { started.add(lk.root); ctx.paint(); }}>Start anyway</button>`;
    case 'paused':
      return lk.isHost ? html`<button icon="people" role="primary" @tap=${guard(() => call('', `/hosting/${lk.root}/confirm`, { seen: h.pendingKey || '' }))}>Confirm ${lk.pending.join(', ')}</button>
        <button icon="xmark" @tap=${guard(() => call('', `/hosting/${lk.root}/decline`, {}))}>Decline</button>` : nothing;
    case 'ended':
      return v.access === 'viewer' ? nothing : html`<button icon="refresh" @tap=${guard(async () => {
        const r = await call('global', `/hosted/${lk.root}/continue`, {});
        if (r && r.conversation) app.select(r.conversation);
      })}>Continue without ${h.host}</button>`;
  }
  return nothing;
}
