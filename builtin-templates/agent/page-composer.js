// page-composer.js — the composer while a page is open (no conversation;
// D212): a page may give the bottom bar a job of its own, or none
// (web-ext.js page(p).composer). Projects: a project's page writes to its
// coordinator there (its card shows what it said) — the bar never starts a
// new chat on a project's page; the list and the forms have no bar.
//
//   pageComposer(app, v)  the open page's: undefined — the composer's own
//                         (a conversation, home, a page that says nothing);
//                         null — none; else {key, placeholder, busy, send(text)
//                         → Promise<boolean: sent>}
//   paintPageComposer(pc, app)  as agent.js paints the composer (before a
//                         hosted conversation's lock): hides it, or shows the
//                         page's job (no class, model or sandbox pickers, no
//                         attachments)
//   sendPage(app, v, text, clear)  Send while a page has a job: true when
//                         the page took it (clear() once sent)
//
// Each job keeps its own draft: what you were writing to a new chat is not
// sent to a coordinator, nor the other way round.
import { ext } from './web-ext.js';

const $ = (id) => document.getElementById(id);
const drafts = new Map(); // a job's key → its text ('' the composer's own)
let shown = '';           // the job shown: '' (its own), 'none', or a page's key

export function pageComposer(app, v) {
  if (v || !app.page || app.page === 'automations') return undefined;
  const pg = ext.page(app.page);
  return pg ? pg.composer : undefined;
}

export function paintPageComposer(pc, app) {
  const bar = document.querySelector('.composer');
  const key = pc === undefined ? '' : pc === null ? 'none' : pc.key;
  const was = shown;
  if (key !== shown) {
    if (shown !== 'none') drafts.set(shown, $('msg').value);
    if (key !== 'none') { $('msg').value = drafts.get(key) || ''; $('msg').dispatchEvent(new Event('input')); } // autosize
    shown = key;
  }
  bar.hidden = key === 'none';
  bar.classList.toggle('pagejob', !!pc);
  if (!pc) {
    if (was !== '' && key === '') { $('send').disabled = !!app.sending; $('attach').hidden = !app.attach.items.length; } // its own again
    if (key === 'none') $('attach').hidden = true;
    return;
  }
  $('attach').hidden = true;
  $('msg').placeholder = pc.placeholder;
  $('msg').disabled = false;
  $('send').disabled = !!pc.busy;
}

export function sendPage(app, v, text, clear) {
  const pc = pageComposer(app, v);
  if (pc === undefined) return false;
  if (pc && !pc.busy) pc.send(text).then((sent) => { if (sent) { drafts.delete(pc.key); clear(); } });
  return true;
}

const style = document.createElement('style');
style.textContent = `
  .composer[hidden] { display: none; }
  .composer.pagejob .cpicks, .composer.pagejob #clip { display: none; }
`;
document.head.append(style);
