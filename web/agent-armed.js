/**
 * /vendor/agent-armed.js — how xbin's own chrome holds a control that
 * widens what something may do until it is the person's deliberate click:
 * the approval sheet's Allow (bx-agent-approve.js), a preference that
 * widens (WidenGuard there), "Trust this tile" (agent-signin.js).
 *
 *   uncovered(el)        the person can see el and nothing is drawn over it
 *   ArmedButtons(host)   a lit controller: every button the host marks
 *                        data-armed answers only ARM_MS after it appears,
 *                        to the person's own click (isTrusted) while the
 *                        window has their attention and nothing covers it —
 *                        never to Enter — and again after the button's
 *                        data-armed value changes (what it stands for
 *                        changed). Bind @click=${(e) => { if
 *                        (!armed.may(e)) return; … }} @keydown=${armed.key};
 *                        the button carries aria-disabled until it is armed.
 */

/** ARM_MS: how long after it appears a widening control answers nothing (the chrome's CHOICES.armMs). */
export const ARM_MS = 600;

/** uncovered(el): the person can see el and nothing is drawn over its middle — through every shadow root on the way. */
export function uncovered(el) {
  if (!el || !el.isConnected || document.visibilityState !== 'visible') return false;
  const r = el.getBoundingClientRect();
  if (r.width < 4 || r.height < 4) return false;
  const x = r.left + r.width / 2, y = r.top + r.height / 2;
  let at = document.elementFromPoint(x, y);
  for (let i = 0; at && at.shadowRoot && i < 16; i++) {
    const inner = at.shadowRoot.elementFromPoint(x, y);
    if (!inner || inner === at) break;
    at = inner;
  }
  return !!at && (at === el || el.contains(at));
}

export class ArmedButtons {
  constructor(host) { this.host = host; this.seen = new WeakMap(); host.addController(this); }
  // a button is armed for what it stood for when it appeared: its data-armed value is a key of that (a card's basis, what it
  // lists). lit reuses the element when the card redraws, so a NEW key — the question changed under the person, more names
  // were shown — makes the same button wait again: what they click is never something that slid in under their pointer
  hostUpdated() {
    for (const el of this.host.renderRoot.querySelectorAll('[data-armed]')) {
      const key = el.getAttribute('data-armed') || '', was = this.seen.get(el);
      if (was && was.key === key) continue;
      this.seen.set(el, { at: performance.now(), key });
      el.setAttribute('aria-disabled', 'true');
      setTimeout(() => { if (el.isConnected && this.armed(el)) el.setAttribute('aria-disabled', 'false'); }, ARM_MS + 10);
    }
  }
  armed(el) { const s = this.seen.get(el); return !!s && s.key === (el.getAttribute('data-armed') || '') && performance.now() - s.at >= ARM_MS; }
  /** may(e): this click is the person's own, on an armed button nothing covers. */
  may(e) { return !!e && e.isTrusted && this.armed(e.currentTarget) && document.hasFocus() && uncovered(e.currentTarget); }
  key = (e) => { if (e.key === 'Enter') e.preventDefault(); };
}
