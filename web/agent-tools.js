/**
 * agent-tools.js — the Agent tab's model of a tool call (D77), at the URL it
 * always had. The model itself is the chat kit's now (D224):
 * /vendor/chat/tools.js reads a call's record, /vendor/chat/ask.js a
 * question's form. This module keeps every export it had, saying what it
 * said — the Swift port and old pages are held to it (hack/agent-tools.test.mjs,
 * native/tools/agent-parity.mjs) — as re-exports, and as the adapter from
 * the Agent tab's own record (its tool kind is `tk`, its content the last
 * list an event carried) to the kit's.
 *
 * The record newTool makes carries the kit's too (`rec`, a ToolRecord kept
 * by the kit's merge rules: locations, files, a diff that outlives a later
 * content list): the cards move onto that one, and this shape stays for who
 * reads it.
 */
import { newToolRecord } from './chat/blocks.js';
import * as kit from './chat/tools.js';

export { firstLine, describeCommand, filesStat, stripAnsi, rawText, unifiedDiff } from './chat/tools.js';
export { formFields, formContent, missingRequired } from './chat/ask.js';

export function newTool(id) {
  return {
    kind: 'tool', id, title: '', tk: 'other', status: 'pending', content: null, rawInput: null, rawOutput: null,
    name: '', label: '', parent: '', subagent: false, planReview: false, output: '', exitCode: null,
    files: null, children: null, t0: 0, rec: newToolRecord(id),
  };
}

// foldTool merges one tool.call/tool.update event into the record: fields
// replace, outputDelta appends, output replaces the accumulated output — and
// into its ToolRecord by the kit's rules.
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
  if (d.subagent) { t.subagent = true; if (!t.children) t.children = []; }
  if (d.planReview) t.planReview = true;
  if (typeof d.outputDelta === 'string') t.output += d.outputDelta;
  if (typeof d.output === 'string') t.output = d.output;
  if (d.exitCode != null) t.exitCode = d.exitCode;
  if (t.rec) {
    kit.mergeTool(t.rec, d);
    // the two outputs are one text (they differ only after an event that carried a whole output and a delta at once): held once
    if (t.rec.output.length === t.output.length) t.output = t.rec.output;
  }
  return t;
}

// the Agent tab's record — or a permission request's tool with `tk` set —
// as the kit reads a call: its text is the content list's first that says
// something
const call = (t) => (t ? { id: t.id, title: t.title, label: t.label, name: t.name, status: t.status, rawInput: t.rawInput, planReview: t.planReview, kind: t.tk, text: kit.firstText(t.content) } : t);

// the command of a shell call: rawInput.command (a string, or argv), else
// .cmd/.script; a claude/opencode/codex execute call's title is the command.
export const commandOf = (t) => kit.commandOf(call(t));

// headline: what a card is called — the harness's own description, else a
// reading of the command, else the title's first line (the kit's headline
// without its reading by kind: this export says what it always said).
export const headline = (t) => kit.headline(call(t), { reading: false });

// isPlanApproval: a mode switch (Claude's ExitPlanMode "Ready to code?",
// Codex's "Implement this plan?"), or any request carrying a plan to approve.
export const isPlanApproval = (t) => !!t && (t.kind === 'switch_mode' || kit.isPlanApproval(call(t)));

// planText: the plan's markdown — claude sends it as text content, codex
// only in rawInput.plan.
export const planText = (t) => kit.planText(call(t));
