/**
 * agent-testapi.js — <bx-agent>'s test hook (the UI harness drives it; the
 * frame-testapi.js pattern): the transcript as plain records, the window it
 * renders (D124) over the pages it holds (D130), and the actions.
 */
import { headline, isPlanApproval, rawText, formFields } from '/vendor/agent-tools.js';

export function agentTestApi(a) {
  return {
    get status() { return a._status(); },
    get sessionId() { return a.session || null; },
    get provider() { return a._provider; },
    get blocks() {
      return a._blocks().map((b) => ({ kind: b.kind, role: b.role, text: b.text, status: b.status, pid: b.pid, by: b.by, optionId: b.optionId, stopReason: b.stopReason,
        ...(b.kind === 'tool' ? { id: b.id, name: b.name, tk: b.tk, headline: headline(b), output: b.output, exitCode: b.exitCode, files: b.files ? b.files.changes.map((c) => c.path) : null,
          children: b.children ? b.children.map((c) => ({ kind: c.kind, text: c.text, id: c.id, done: c.done })) : null } : {}),
        ...(b.kind === 'changes' ? { turn: b.turn, files: b.changes.map((c) => c.path) } : {}),
        ...(b.kind === 'msg' && b.files ? { files: b.files } : {}),
        ...(b.kind === 'ask' ? { eid: b.eid, action: b.action, fields: formFields(b.schema).map((f) => f.key), content: b.content } : {}),
        ...(b.kind === 'perm' ? { plan: isPlanApproval(b.tool) } : {}),
        ...(b.kind === 'thought' ? { done: b.done, ms: (b.t1 || 0) - (b.t0 || 0) } : {}) }));
    },
    get pending() {
      return a._blocks().filter((b) => b.kind === 'perm' && !b.by).map((b) => ({ pid: b.pid, cmd: rawText(b.tool?.rawInput), options: (b.options || []).map((o) => o.optionId),
        scoped: b.rule ? b.rule.scoped : true, plan: isPlanApproval(b.tool) }));
    },
    get followUp() { return a._followUp ? a._followUp.text : null; },
    // the rendered window (D124): total blocks, the first rendered index, rows in the DOM, the scroller
    // (D130: total counts the LOADED blocks; events the loaded events)
    get window() {
      const sc = a._sc(), tx = a._tx;
      return { total: a._blocks().length, from: a._start, to: a._end, rendered: sc ? sc.querySelectorAll(':scope > .blk').length : 0, atBottom: a._sw.atBottom,
        scrollTop: sc ? sc.scrollTop : 0, scrollHeight: sc ? sc.scrollHeight : 0, clientHeight: sc ? sc.clientHeight : 0,
        segments: tx.segs.length, events: tx.segs.reduce((n, g) => n + g.events.length, 0), hasOlder: tx.hasOlder, hasNewer: tx.hasNewer,
        detached: tx.detached, fresh: tx.fresh, paged: tx.paged, all: a._all, lastSeq: tx.lastSeq, firstKey: a._fromKey,
        seqs: tx.segs.length ? [tx.segs[0].first, tx.segs[tx.segs.length - 1].events.at(-1)?.seq ?? 0] : null };
    },
    get fetches() { return a._fetches.slice(); }, // the queries of the events route, in order
    get pill() { return a.renderRoot?.querySelector('.pill')?.textContent.trim() || null; },
    jumpLatest() { return a._jumpLatest(); },
    // a late event (D130): take event `seq` out of its loaded page, as if it
    // had not arrived, and deliver it as a catch-up would — only its page
    // refolds, and nothing on screen moves
    late(seq) {
      const tx = a._tx, seg = tx.segs.find((g) => g.events.some((e) => e.seq === seq));
      if (!seg) return false;
      const e = seg.events.find((x) => x.seq === seq);
      seg.events = seg.events.filter((x) => x !== e);
      tx.seen.delete(seq);
      const ok = tx.apply([e]);
      a.requestUpdate();
      return ok;
    },
    get hidden() { return !!a._stale; }, // a hidden tab that skipped a render
    scrollTo(y) { const sc = a._sc(); if (sc) sc.scrollTop = y; },
    firstVisible() { const f = a._sw.firstVisible(); return f ? Number(f.el.dataset.k) : null; },
    topOf(key) { const sc = a._sc(); const el = sc && sc.querySelector(`:scope > .blk[data-k="${key}"]`); return el ? el.getBoundingClientRect().top - sc.getBoundingClientRect().top : null; },
    loadAll() { return a._loadAll(); },
    get commands() { return a._commands().map((c) => c.name); },
    get questions() { return a._blocks().filter((b) => b.kind === 'ask' && !b.action).map((b) => ({ eid: b.eid, message: b.message, fields: formFields(b.schema).map((f) => ({ key: f.key, kind: f.kind, other: f.other, options: f.options.map((o) => o.value) })) })); },
    answer(eid, action, content) { a._answer(eid, action, content); },
    get slashMenu() { return a._slashItems().map((c) => c.name); },
    get draft() { return a._draft; },
    setProvider(id) { a._provider = id; const p = (a._providers || []).find((x) => x.id === id); a._mode = p?.defaultMode || ''; },
    get options() { return a._options().map((o) => ({ id: o.id, current: o.currentValue, values: (o.options || []).map((v) => v.value) })); },
    get modes() { return a._modes().map((m) => ({ id: m.id, name: m.name })); },
    get login() { return a._login(); },
    get history() { return a._historyMeta || null; }, // the past session shown read-only (history mode)
    resumeHistory() { a._doResume(); },
    signIn() { const lg = a._login(); if (lg) a._doSignIn(lg); },
    setOption(id, value) { a._setOption(id, value); },
    start() { return a._create(); },
    send(text) { a._draft = text; return a._submit(); },
    permit(pid, decision, optionId) { a._permit(pid, decision, optionId); },
    rejectPlan(pid, optionId, feedback) { if (feedback) a._planFeedback[pid] = feedback; a._rejectPlan(pid, optionId); },
    cancel() { a._cancel(); },
  };
}
