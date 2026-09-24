/**
 * agent-tools.js — the Agent tab's model of a tool call (D77), pure and
 * dependency-free (node-tested: hack/agent-tools.test.mjs). The daemon lifts
 * the adapters' extras into plain event fields (internal/agent/acp/
 * toolmeta.go: name, label, parent, subagent, planReview, output/outputDelta,
 * exitCode); this folds a call's events into one record and derives what
 * the cards show: a human headline (the harness's own description, else a
 * deterministic reading of the command), whether it is a plan approval, the
 * plan text. The cards themselves are agent-cards.js.
 */

export function newTool(id) {
  return {
    kind: 'tool', id, title: '', tk: 'other', status: 'pending', content: null, rawInput: null, rawOutput: null,
    name: '', label: '', parent: '', subagent: false, planReview: false, output: '', exitCode: null,
    files: null, children: null, t0: 0,
  };
}

// foldTool merges one tool.call/tool.update event into the record: fields
// replace, outputDelta appends, output replaces the accumulated output.
export function foldTool(t, d) {
  if (d.title != null) t.title = d.title;
  if (d.kind != null) t.tk = d.kind;
  if (d.status != null) t.status = d.status;
  if (d.content != null) t.content = d.content;
  if (d.rawInput != null) t.rawInput = d.rawInput;
  if (d.rawOutput != null) t.rawOutput = d.rawOutput;
  if (d.name) t.name = d.name;
  if (d.label) t.label = d.label;
  if (d.parent) t.parent = d.parent;
  if (d.subagent) t.subagent = true;
  if (d.planReview) t.planReview = true;
  if (typeof d.outputDelta === 'string') t.output += d.outputDelta;
  if (typeof d.output === 'string') t.output = d.output;
  if (d.exitCode != null) t.exitCode = d.exitCode;
  return t;
}

export const firstLine = (s) => { const t = String(s ?? '').trim(); const i = t.indexOf('\n'); return i < 0 ? t : t.slice(0, i) + ' …'; };

// the command of a shell call: rawInput.command (a string, or argv), else
// .cmd/.script; a claude/opencode/codex execute call's title is the command.
export function commandOf(t) {
  const r = t && t.rawInput;
  if (r && typeof r === 'object') {
    const c = r.command ?? r.cmd ?? r.script;
    if (typeof c === 'string') return c + (Array.isArray(r.args) ? ' ' + r.args.join(' ') : '');
    if (Array.isArray(c)) return c.join(' ');
  }
  return t && t.tk === 'execute' ? String(t.title || '') : '';
}

// the first text content item (a tool's prose: gemini's description, a plan)
function contentText(t) {
  for (const it of Array.isArray(t && t.content) ? t.content : []) {
    const s = it && (it.content?.text ?? (it.type === 'text' ? it.text : undefined));
    if (typeof s === 'string' && s.trim()) return s;
  }
  return '';
}

// headline: what a card is called. The harness's own description first
// (claude's _meta.claudeCode.title / rawInput.description → label; gemini's
// description is the pending call's text content), else a reading of the
// command, else the title's first line.
export function headline(t) {
  if (!t) return '';
  if (t.label) return t.label;
  const desc = t.rawInput && typeof t.rawInput.description === 'string' ? t.rawInput.description : '';
  if (desc) return desc;
  const cmd = commandOf(t);
  if (cmd) {
    if (t.tk === 'execute' && (t.status === 'pending' || t.status === 'in_progress')) {
      const c = contentText(t); // gemini: "[in dir] (description)" until the output replaces it
      if (c && !c.includes('\n') && c.length < 160) return c;
    }
    return describeCommand(cmd);
  }
  return firstLine(t.title) || t.name || t.id || 'a tool call';
}

// describeCommand reads a shell command deterministically: a heredoc script
// becomes "Python script (N lines) → main.go", sed -i/perl -pi/cat >/tee/>
// name the file they write, rm/mkdir/mv say so; anything else is its first line.
export function describeCommand(cmd) {
  let c = String(cmd ?? '').trim();
  const cd = c.match(/^cd\s+(\S+)\s*&&\s*/);
  if (cd) c = c.slice(cd[0].length);
  const here = c.match(/^([\w./-]*?(python3?|node|deno|bun|ruby|perl|php|bash|sh|zsh))\b[^\n]*?<<-?\s*(['"]?)(\w+)\3[^\n]*\n([\s\S]*?)\n\4\s*$/);
  if (here) {
    const lang = { python: 'Python', python3: 'Python', node: 'Node', deno: 'Deno', bun: 'Bun', ruby: 'Ruby', perl: 'Perl', php: 'PHP', bash: 'Shell', sh: 'Shell', zsh: 'Shell' }[here[2]] || here[2];
    const body = here[5];
    const n = body.split('\n').length;
    return `${lang} script (${n} line${n === 1 ? '' : 's'})${targets(scriptTargets(body))}`;
  }
  const catW = c.match(/^cat\s+>>?\s*(['"]?)([^\s'"<>|;&]+)\1\s*<</);
  if (catW) return `Write ${catW[2]}`;
  const inPlace = c.match(/^(sed|perl)\b[^\n|;&]*\s-[a-zA-Z]*[ip][a-zA-Z]*\b[^\n|;&]*?\s(['"]?)([^\s'"|;&]+)\2\s*$/);
  if (inPlace && /\s-[a-zA-Z]*i/.test(c)) return `Edit ${inPlace[3]} (${inPlace[1]})`;
  const tee = c.match(/\|\s*tee\s+(?:-a\s+)?(['"]?)([^\s'"|;&]+)\1\s*$/);
  if (tee) return `Write ${tee[2]}`;
  const redir = c.match(/^[^\n]*?[^2&0-9]>>?\s*(['"]?)([^\s'"|;&<>]+)\1\s*$/);
  if (redir && !c.includes('\n') && redir[2] !== '/dev/null') return `Write ${redir[2]}`;
  const rm = c.match(/^rm\s+(?:-\w+\s+)*(.+)$/);
  if (rm && !c.includes('\n')) return `Remove ${rm[1]}`;
  const mk = c.match(/^mkdir\s+(?:-\w+\s+)*(.+)$/);
  if (mk && !c.includes('\n')) return `Create ${mk[1]}`;
  const mv = c.match(/^mv\s+(?:-\w+\s+)*(\S+)\s+(\S+)$/);
  if (mv) return `Move ${mv[1]} → ${mv[2]}`;
  return firstLine(c);
}

// the files a script body names (open('x'), Path('x'), p='x', read/writeFileSync)
function scriptTargets(body) {
  const out = [];
  const re = /(?:open|Path|readFileSync|writeFileSync|readFile|writeFile|read_text|write_text)\(\s*(['"])([^'"\n]+)\1|(?:^|[\s;(])(?:p|path|fn|file|filename|target)\s*=\s*(['"])([^'"\n]+)\3/gm;
  for (const m of body.matchAll(re)) {
    const f = m[2] || m[4];
    if (f && /[./]/.test(f) && !out.includes(f)) out.push(f);
  }
  return out;
}

const targets = (fs) => (!fs.length ? '' : ' → ' + fs.slice(0, 2).join(', ') + (fs.length > 2 ? ` +${fs.length - 2}` : ''));

// isPlanApproval: a mode switch (Claude's ExitPlanMode "Ready to code?",
// Codex's "Implement this plan?"), or any request carrying a plan to approve.
export function isPlanApproval(t) {
  if (!t) return false;
  return t.kind === 'switch_mode' || t.tk === 'switch_mode' || !!t.planReview ||
    !!(t.rawInput && typeof t.rawInput === 'object' && typeof t.rawInput.plan === 'string');
}

// planText: the plan's markdown — claude sends it as text content, codex
// only in rawInput.plan.
export function planText(t) {
  return contentText(t) || (t && t.rawInput && typeof t.rawInput.plan === 'string' ? t.rawInput.plan : '');
}

// stripAnsi drops terminal escapes (colour, cursor) from command output.
// eslint-disable-next-line no-control-regex
export const stripAnsi = (s) => String(s ?? '').replace(/\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g, '');

// rawText renders a tool's rawInput for display: a shell command verbatim
// (the common {command}/{cmd} shapes), else compact JSON.
export function rawText(raw) {
  if (raw == null) return '';
  if (typeof raw === 'string') return raw;
  if (typeof raw === 'object') {
    const cmd = raw.command ?? raw.cmd ?? raw.script;
    if (typeof cmd === 'string') return cmd + (Array.isArray(raw.args) ? ' ' + raw.args.join(' ') : '');
    try { return JSON.stringify(raw, null, 1); } catch { return ''; }
  }
  return String(raw);
}

// unifiedDiff(path, old, new): a git-style unified diff from an ACP diff
// block's whole old/new text, via a line LCS — so bx-code's diffHTML gives
// the same +/- view the code panel uses. Bounded: very large inputs fall back
// to replace-all instead of an O(nm) table.
export function unifiedDiff(path, oldText, newText) {
  const a = String(oldText ?? '').split('\n'), b = String(newText ?? '').split('\n');
  const p = path || 'file';
  if ((oldText ?? '') === (newText ?? '')) return `diff --git a/${p} b/${p}\n`;
  const n = a.length, m = b.length;
  let body;
  if (n * m > 400000) {
    body = a.map((l) => '-' + l).concat(b.map((l) => '+' + l));
  } else {
    const dp = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    body = []; let i = 0, j = 0;
    while (i < n && j < m) {
      if (a[i] === b[j]) { body.push(' ' + a[i]); i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) { body.push('-' + a[i]); i++; }
      else { body.push('+' + b[j]); j++; }
    }
    while (i < n) body.push('-' + a[i++]);
    while (j < m) body.push('+' + b[j++]);
  }
  return `diff --git a/${p} b/${p}\n--- a/${p}\n+++ b/${p}\n@@ -1,${n} +1,${m} @@\n` + body.join('\n') + '\n';
}
