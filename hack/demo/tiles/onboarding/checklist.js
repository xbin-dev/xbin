// checklist.js — one customer's steps: tick to complete, late ones in red,
// finished ones folded behind "n done".
import { html, nothing } from 'lit';

const day = (t) => new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });

export function checklist(c, { open, onToggle, onShowDone }) {
  const now = Date.now();
  const done = c.steps.filter((s) => s.done), todo = c.steps.filter((s) => !s.done);
  const row = (s) => {
    const late = !s.done && s.due && s.due < now;
    return html`<label class="step ${s.done ? 'done' : ''} ${late ? 'late' : ''}">
      <input type="checkbox" .checked=${!!s.done} @change=${(e) => onToggle(c, s, e.target.checked)}>
      <span class="st">${s.title}</span>
      <span class="due">${s.done ? `done ${day(s.doneAt)}` : s.due ? (late ? html`<bx-icon name="error"></bx-icon>late · was due ${day(s.due)}` : `due ${day(s.due)}`) : ''}</span>
    </label>`;
  };
  return html`<div class="steps">
    ${done.length ? html`<button class="fold" @click=${onShowDone}><bx-icon name=${open ? 'caret-down' : 'caret-right'}></bx-icon>${done.length} done</button>` : nothing}
    ${open ? done.map(row) : nothing}
    ${todo.map(row)}
  </div>`;
}
