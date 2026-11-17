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
 *                        never to Enter. Bind @click=${(e) => { if
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
  hostUpdated() {
    for (const el of this.host.renderRoot.querySelectorAll('[data-armed]')) {
      if (this.seen.has(el)) continue;
      this.seen.set(el, performance.now());
      el.setAttribute('aria-disabled', 'true');
      setTimeout(() => { if (el.isConnected && this.armed(el)) el.setAttribute('aria-disabled', 'false'); }, ARM_MS + 10);
    }
  }
  armed(el) { const t = this.seen.get(el); return t != null && performance.now() - t >= ARM_MS; }
  /** may(e): this click is the person's own, on an armed button nothing covers. */
  may(e) { return !!e && e.isTrusted && this.armed(e.currentTarget) && document.hasFocus() && uncovered(e.currentTarget); }
  key = (e) => { if (e.key === 'Enter') e.preventDefault(); };
}
