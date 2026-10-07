// native/pick.js — a choice in a screen's bar (the model, the class, the
// sandbox): a menu picker — or, on a phone's narrow bar (nav.compact()), a
// submenu of the screen's ⋯ with the current choice checked, so the bar keeps
// two items and its title (D190).
import { html, repeat, nothing } from '/vendor/xb-native.js';

// pickTpl({label, value, options: [{value, label, icon?}], change(e), fold, icon})
export function pickTpl({ label, value, options, change, fold = false, icon }) {
  if (!fold) return html`<picker label=${label} style="menu" value=${value} options=${options} @change=${change}/>`;
  const cur = options.find((o) => o.value === value);
  return html`<menu icon=${icon || nothing} label=${`${label}: ${cur ? cur.label : '—'}`}>
    ${repeat(options, (o) => String(o.value), (o) => html`<button icon=${o.value === value ? 'check' : o.icon || nothing}
      @tap=${() => { if (o.value !== value) change({ type: 'change', value: o.value }); }}>${o.label}</button>`)}
  </menu>`;
}
