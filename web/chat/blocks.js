/**
 * chat/blocks.js — the block model of the chat kit (/vendor/chat/blocks.js).
 *
 * A transcript is an ordered list of BLOCKS. Two reducers make them from
 * different inputs — xbind's Agent tab folds a session's event log
 * (/vendor/agent-fold.js), a tile with its own chat folds its own rows (the
 * agent template's model/fold.js) — and the kit's views draw blocks and
 * nothing else. This module is that contract: the shape of every kind, the
 * one record of a tool call, and the few helpers a reducer needs (keys,
 * versions, nesting). It is pure — no lit, no DOM, no imports — so a native
 * view and node tests load it too.
 *
 * Every block has
 *   k     its key: a string, unique among all the blocks of one transcript
 *         (nested ones too) and the same every time the block is folded
 *         again — a row's identity, what open/closed state and memos hang on;
 *   v     its version: a number that changes whenever anything a view of the
 *         block reads changed, a nested block's change included. Same (k, v):
 *         the same picture;
 *   kind  one of KINDS.
 * A field whose name starts with `$` is a reducer's or a view's own (a parent
 * pointer, a memo): never part of the contract, never serialised (plain()).
 * An optional field that is absent means its zero value ('' / 0 / false /
 * null / []): reducers may leave such fields out and views read them with a
 * default.
 *
 * What a reducer hands a host is a Transcript: the blocks in order, and —
 * beside them, because hosts place these differently — the blocks that
 * wait on a person now (`tail`), the plan in force (`plan`) and the usage.
 * The Agent tab keeps a permission request where it was asked and its
 * answer in history; the agent template draws the request under the
 * transcript while it waits and nothing afterwards. Both are right: a
 * perm, ask or signin block may sit in `blocks` or in `tail`.
 *
 * The shapes are frozen as they grow: a field is never removed or given
 * another meaning, a kind is never re-shaped (/vendor/chat/rev.js counts
 * the additions).
 */

// ---- kinds ---------------------------------------------------------------------------

/** The kinds a view of the kit draws. A reducer may emit others of its own (a host draws those: ChatCtx.other). */
export const KINDS = Object.freeze(['user', 'assistant', 'draft', 'thought', 'tool', 'agent', 'changes', 'plan', 'perm', 'ask', 'signin', 'notice', 'turn', 'gap']);

/** ACP's tool kinds (a ToolRecord's kind); anything else reads as 'other'. */
export const TOOL_KINDS = Object.freeze(['read', 'edit', 'delete', 'move', 'search', 'execute', 'think', 'fetch', 'switch_mode', 'other']);

/** ACP's tool call statuses (a ToolRecord's status). */
export const TOOL_STATUS = Object.freeze(['pending', 'in_progress', 'completed', 'failed', 'cancelled']);

/**
 * A tool block's state — what its card says. ACP's status, plus what only a
 * reducer knows: `writing` (the model is still writing the call's
 * arguments), `approval` (it waits for a person's permission), `waiting`
 * (it waits for something else: a subagent, a job).
 */
export const TOOL_STATES = Object.freeze(['writing', 'pending', 'running', 'approval', 'waiting', 'done', 'error', 'stopped']);

/**
 * @typedef {object} BlockBase
 * @property {string} k        the key (see above)
 * @property {number} v        the version (see above)
 * @property {string} kind
 * @property {string} [parent] the id of the subagent's tool call the block ran under ('' at the top);
 *                             set also on a block that is nested under that call's block
 * @property {number} [at]     when it began, in ms since the epoch (0: not known)
 */

/**
 * A file that came with a message.
 * @typedef {object} ChatFile
 * @property {string} name        its name (a path's last part)
 * @property {string} [path]      where the host keeps it ('' when it only knows the name)
 * @property {string} [mime]
 * @property {number|null} [size] bytes, when known exactly
 * @property {string} [sizeText]  the size as the host words it ("12 KB"), when it knows no bytes
 */

/**
 * user — what a person (or something speaking for one) sent.
 * @typedef {BlockBase & {
 *   kind: 'user', text: string, files?: ChatFile[],
 *   sender?: string,     // who sent it, when the host knows ('' the reader themself or unknown)
 *   steered?: boolean,   // taken into the running turn rather than starting one
 *   ref?: string|number, // the host's handle of the message (to resolve its files)
 * }} UserBlock
 */

/**
 * assistant — a message of the agent's, markdown. A message still streaming
 * is an assistant block that grows (same k, v moving) when the reducer knows
 * its identity from the first chunk (the Agent tab), a draft when it doesn't.
 * @typedef {BlockBase & {
 *   kind: 'assistant', text: string,
 *   mid?: string,    // the agent's message id
 *   model?: string, usage?: object,
 * }} AssistantBlock
 */

/**
 * draft — the answer being written, before the reducer has the row it
 * becomes: drawn as an assistant message with a cursor, replaced (another k)
 * by the assistant block once it is whole.
 * @typedef {BlockBase & { kind: 'draft', text: string }} DraftBlock
 */

/**
 * thought — the agent thinking.
 * @typedef {BlockBase & {
 *   kind: 'thought', text: string,
 *   live: boolean,   // still being thought (a view also asks ChatCtx.running)
 *   ms: number,      // how long it took (0: not known)
 * }} ThoughtBlock
 */

/**
 * tool — one tool call: its record, what its card says, and — a subagent's
 * call — everything that ran under it.
 * @typedef {BlockBase & {
 *   kind: 'tool', rec: ToolRecord,
 *   state: string,              // one of TOOL_STATES (toolState)
 *   children: ChatBlock[]|null, // a subagent call's blocks, in order; null: not a subagent call
 *   result?: string,            // what the call answered as its host words it, when that is more than the record
 *                               // (a call of the host's own tools; a call settled some other way: "(cancelled)")
 * }} ToolBlock
 */

/**
 * agent — a sub-conversation: another agent's run started from this one,
 * drawn as a card that unfolds to that run's own blocks.
 * @typedef {BlockBase & {
 *   kind: 'agent',
 *   title: string,              // what the card is called
 *   ref: string|number,         // the host's handle of the sub-conversation (ChatCtx.openAgent / loadAgent)
 *   state: string,              // running | approval | done | error | stopped
 *   phase?: string,             // a word for what it is doing now
 *   steps?: number,             // how much it did (the host's count), 0 unknown
 *   task?: string,              // what it was asked
 *   answer?: string,            // what it came to, markdown ('' while it runs)
 *   blocks: ChatBlock[]|null,   // its transcript; null: not loaded (the view asks ChatCtx.loadAgent once opened)
 *   tail?: ChatBlock[],         // what it waits on a person for (a perm, an ask), drawn on its card
 *   provider?: string,          // a coding agent's name, when one drives it
 * }} AgentBlock
 */

/**
 * One file of a change set.
 * @typedef {object} FileChange
 * @property {string} path
 * @property {string} [oldPath]  a rename's
 * @property {string} status     added | modified | deleted | renamed | typechange
 * @property {number} add
 * @property {number} del
 * @property {boolean} [binary]
 */

/**
 * What changed on disk, as the host observed it (not as the agent said).
 * @typedef {object} ChangeSet
 * @property {FileChange[]} files
 * @property {{text: string, truncated: boolean}|null} patch  one unified patch of all the files, possibly capped;
 *                                null: none inline (ChatCtx.fileDiff fetches a file's)
 */

/**
 * changes — what a whole turn changed on disk.
 * @typedef {BlockBase & { kind: 'changes', turn: number, set: ChangeSet }} ChangesBlock
 */

/**
 * plan — the agent's plan as it stands.
 * @typedef {BlockBase & {
 *   kind: 'plan',
 *   entries: {content: string, status: string, priority?: string}[],  // status: pending | in_progress | completed
 * }} PlanBlock
 */

/**
 * One way to answer a permission request.
 * @typedef {object} PermOption
 * @property {string} optionId
 * @property {string} name
 * @property {string} kind         allow_once | allow_always | reject_once | reject_always
 * @property {boolean} [explicit]  choosing it raises what the agent may do without asking: confirm it,
 *                                 and offer it only to who may (ChatCtx.can.raise)
 */

/**
 * perm — the agent asks permission for a call; a plan approval when `plan`
 * is set (the options are then modes to go on in, never "remember this").
 * @typedef {BlockBase & {
 *   kind: 'perm', pid: string,
 *   callId: string,            // the tool call it is about ('' none)
 *   tool: {title: string, kind: string, name?: string, label?: string, command?: string, rawInput?: any, content?: any[]},
 *                              // the call as the request describes it: ACP's kind and content list, the harness's label,
 *                              // the shell command when it is one
 *   options: PermOption[],
 *   rule: {kind: string, title: string}|null,  // what an "always" answer will also allow; null: it can't be remembered
 *   title?: string,            // the agent's own heading for the request
 *   description?: string,
 *   defaultToNo?: boolean,     // the rejections come first
 *   plan: {text: string, path?: string}|null,
 *   calls?: string[],          // a host that asks once for several calls of its own: their names
 *   by: string|null,           // who answered: 'user:<id>', 'auto' (a rule), 'cancel'; null: it waits
 *   optionId: string|null,     // the option chosen
 *   park?: string,             // the host's token for answering it
 * }} PermBlock
 */

/**
 * ask — a question the agent asks: a form from `schema` (an elicitation),
 * a page to open (mode 'url'), or only its message (mode 'text': the person
 * answers with their next message).
 * @typedef {BlockBase & {
 *   kind: 'ask', eid: string,
 *   callId: string,            // the tool call it belongs to ('' none)
 *   message: string,           // markdown
 *   mode: 'form'|'url'|'text',
 *   schema: object|null,       // form: a flat JSON Schema object of primitives
 *   url?: string,              // url: the page
 *   action: string|null,       // accept | decline | cancel | complete; null: it waits
 *   by: string|null,           // who answered ('agent': it withdrew or completed it)
 *   content: object|null,      // an accept's answers
 *   park?: string,
 * }} AskBlock
 */

/**
 * signin — the agent is signed out and a turn waits for that.
 * @typedef {BlockBase & {
 *   kind: 'signin',
 *   provider: string,          // the agent's name
 *   command?: string,          // what signs it in from a terminal
 *   methods?: {id: string, name: string, kind: string}[],        // kind: terminal | api-key | device-code
 *   device?: {url: string, code?: string}|null,                 // a device code under way
 *   park?: string,
 * }} SigninBlock
 */

/**
 * notice — a line that is neither the person's nor the agent's: the host
 * speaking (a session moved, an automation delivered this). Its first line
 * is its heading.
 * @typedef {BlockBase & { kind: 'notice', text: string }} NoticeBlock
 */

/**
 * turn — a turn's end.
 * @typedef {BlockBase & {
 *   kind: 'turn', turn: number, stopReason: string,
 *   usage?: {used: number, size: number, cost?: {amount: number, currency: string}}|null,
 *   error?: string,
 * }} TurnBlock
 */

/**
 * gap — part of the transcript is not here (dropped by the log's limit).
 * @typedef {BlockBase & { kind: 'gap' }} GapBlock
 */

/**
 * @typedef {UserBlock|AssistantBlock|DraftBlock|ThoughtBlock|ToolBlock|AgentBlock|ChangesBlock|PlanBlock|PermBlock|AskBlock|SigninBlock|NoticeBlock|TurnBlock|GapBlock} ChatBlock
 */

/**
 * What a reducer hands its host.
 * @typedef {object} Transcript
 * @property {ChatBlock[]} blocks
 * @property {ChatBlock[]} [tail]      blocks that wait on a person now and are not in `blocks` (perm, ask, signin)
 * @property {PlanBlock|null} [plan]   the plan in force (also in `blocks` when the reducer places it there)
 * @property {object|null} [usage]     the context in use: {used, size, cost?}
 */

// ---- the tool record -----------------------------------------------------------------

/**
 * One file of a call's edit, as the agent described it: a patch, or the
 * whole old and new text (a view draws either; a reducer that has both
 * texts may leave the patch out).
 * @typedef {object} ToolDiff
 * @property {string} path
 * @property {string} status         added | modified | deleted
 * @property {number} add            lines added
 * @property {number} del            lines removed
 * @property {string} [patch]        a unified diff of this file
 * @property {boolean} [truncated]   the patch was cut (at a hunk)
 * @property {string|null} [oldText] the text before (null: the file is new)
 * @property {string} [newText]      the text after
 */

/**
 * The one record of a tool call: its tool.call and tool.update events
 * merged. Both reducers convert what they hold to it, and everything the
 * kit says about a call — its headline, its state, its outcome — is read
 * from it. In JSON a field at its zero value may be left out (id, kind,
 * title and status never are).
 * @typedef {object} ToolRecord
 * @property {string} id                the agent's id for the call. Unique only among calls that are not over:
 *                                      an agent may use an id again, for another call — a tool.call for an id
 *                                      whose call is over, or was made in an earlier turn, starts a new record
 * @property {string} kind              one of TOOL_KINDS
 * @property {string} title             the agent's title for it
 * @property {string} label             the harness's own description, when it gives one (shown instead of the title)
 * @property {string} name              the programmatic tool name (Bash, Task, ExitPlanMode)
 * @property {string} status            one of TOOL_STATUS
 * @property {string} parent            the id of the subagent call it runs under
 * @property {boolean} subagent         the call starts a subagent: blocks run under it
 * @property {boolean} planReview       the call asks for a plan's approval
 * @property {any} rawInput             the tool's arguments as the agent sent them (null: none)
 * @property {{path: string, line?: number}[]} locations  the places it touches
 * @property {string[]} files           the paths of its locations and its diffs, each once, in that order
 * @property {string} text              its text content, the items joined by a blank line
 * @property {boolean} terminal         it has a terminal (its output streams in `output`)
 * @property {string} output            terminal output, escape codes kept; its newest part when the reducer caps it
 * @property {number} outputTruncated   bytes of output dropped before `output` (0: all of it is here)
 * @property {number|null} exitCode     a finished command's exit status
 * @property {ToolDiff[]} diffs         what it edits, as the agent said
 * @property {ChangeSet|null} changed   what it changed on disk, as the host observed
 */

/** A fresh record for the call `id`: every field present, at its zero value. */
export function newToolRecord(id) {
  return {
    id: String(id ?? ''), kind: 'other', title: '', label: '', name: '', status: 'pending', parent: '', subagent: false, planReview: false,
    rawInput: null, locations: [], files: [], text: '', terminal: false, output: '', outputTruncated: 0, exitCode: null, diffs: [], changed: null,
  };
}

/** A call's status is final. */
export const toolDone = (status) => status === 'completed' || status === 'failed' || status === 'cancelled';

/**
 * toolState(rec, hint): a tool block's state from its record — and what the
 * reducer knows beside it (hint: 'writing' | 'approval' | 'waiting', which
 * hold only while the call is not over).
 */
export function toolState(rec, hint = '') {
  switch (rec.status) {
    case 'failed': return 'error';
    case 'cancelled': return 'stopped';
    case 'completed': return 'done';
  }
  if (hint === 'writing' || hint === 'approval' || hint === 'waiting') return hint;
  return rec.status === 'in_progress' ? 'running' : 'pending';
}

// ---- keys, versions, nesting ---------------------------------------------------------

/** block(kind, k, fields): a new block, version 0. */
export function block(kind, k, fields) {
  return { k: String(k), v: 0, kind, ...fields };
}

/** up(child, parent): remember the block a nested block sits in (not enumerable: plain data stays plain). */
export function up(child, parent) {
  Object.defineProperty(child, '$up', { value: parent || null, writable: true, configurable: true, enumerable: false });
  return child;
}

/** bump(b): b changed — its version moves, and that of every block it is nested in (they draw it). */
export function bump(b) {
  for (let x = b; x; x = x.$up) x.v++;
  return b;
}

/** adopt(parent, child): put child at the end of parent's children (a subagent call's blocks). */
export function adopt(parent, child) {
  if (!parent.children) parent.children = [];
  parent.children.push(up(child, parent));
  if (!child.parent && parent.rec) child.parent = parent.rec.id;
  bump(parent);
  return child;
}

/**
 * nest(flat, idOf): a flat list whose blocks name the subagent call they
 * ran under (`parent`) → the list with those blocks moved into that call's
 * `children`, in order. idOf(toolBlock) is the id `parent` is matched with
 * (default: its record's id). A block whose parent isn't in the list (paged
 * out) stays where it is, `parent` still set — an orphan; a loop of parents
 * leaves its blocks flat. For a reducer that holds rows rather than folding
 * events in order.
 */
export function nest(flat, idOf = (b) => b.rec.id) {
  if (!flat.some((b) => b.parent)) return flat;
  const calls = new Map();
  for (const b of flat) if (b.kind === 'tool') calls.set(idOf(b), b);
  const top = [], kids = new Map();
  for (const b of flat) {
    const p = b.parent ? calls.get(b.parent) : null;
    if (p && p !== b) { if (!kids.has(p)) kids.set(p, []); kids.get(p).push(b); } else top.push(b);
  }
  const placed = new Set();
  const place = (b) => {
    placed.add(b);
    const ks = (kids.get(b) || []).filter((c) => !placed.has(c));
    if (ks.length) { b.children = b.children || []; for (const c of ks) b.children.push(up(place(c), b)); }
    return b;
  };
  const out = top.map(place);
  for (const b of flat) if (!placed.has(b)) out.push(b);
  return out;
}

/** inner(b): the blocks nested in b — a subagent call's children, a sub-conversation's blocks. */
export const inner = (b) => (b.kind === 'tool' ? b.children : b.kind === 'agent' ? b.blocks : null) || [];

/** walk(blocks, fn): fn(block, depth) for every block, nested ones after the block they sit in. */
export function walk(blocks, fn, depth = 0) {
  for (const b of blocks || []) {
    fn(b, depth);
    walk(inner(b), fn, depth + 1);
    if (b.kind === 'agent' && b.tail) walk(b.tail, fn, depth + 1);
  }
}

/** find(blocks, k): the block with key k, at any depth (null: none). */
export function find(blocks, k) {
  let hit = null;
  walk(blocks, (b) => { if (!hit && b.k === String(k)) hit = b; });
  return hit;
}

/**
 * Versions gives blocks their `v` in a reducer that makes new objects
 * instead of changing held ones: of(k, token) is the version of block k,
 * moved by one each time `token` — anything that is the same exactly while
 * the block is unchanged: the object it was made from — is another.
 * sweep(seen) forgets the keys not in the set (blocks that are gone).
 */
export class Versions {
  constructor() { this.held = new Map(); }

  of(k, token) {
    const e = this.held.get(k);
    if (!e) { this.held.set(k, { token, v: 0 }); return 0; }
    if (e.token !== token) { e.token = token; e.v++; }
    return e.v;
  }

  sweep(seen) { for (const k of this.held.keys()) if (!seen.has(k)) this.held.delete(k); }
}

/**
 * plain(x): x as plain data — `$` fields left out, at every depth — what a
 * fixture, a native view or a test compares.
 */
export function plain(x) {
  if (Array.isArray(x)) return x.map(plain);
  if (!x || typeof x !== 'object') return x;
  const out = {};
  for (const [key, val] of Object.entries(x)) if (key[0] !== '$' && val !== undefined) out[key] = plain(val);
  return out;
}

// ---- what views receive --------------------------------------------------------------

/**
 * A view of the kit is a function of (block, ctx). ctx is everything it
 * may ask of its host — the Agent tab's element, a tile's page — and the
 * only way it reaches one:
 *
 * @typedef {object} ChatCtx
 * @property {boolean} running                    a turn is running (a live thought, a working subagent stay open)
 * @property {boolean} ended                      the session is over: nothing can be answered
 * @property {string} agentName                   how the agent is called in a card's words
 * @property {string} me                          the reader's id, to tell their messages from others' ('' unknown)
 * @property {{answer: boolean, raise: boolean}} can  the reader may answer requests; may pick an `explicit` option
 * @property {(block: ChatBlock, slot: string, dflt?: boolean) => boolean} isOpen
 *           is this fold of the block open (slot: 'body', 'files', 'f:<path>', 'diff0:all', 'task', 'raw', 'full', …);
 *           dflt is what it is before the reader touched it
 * @property {(block: ChatBlock, slot: string, open: boolean, dflt?: boolean) => void} setOpen
 *           the reader opened or closed it (the host remembers by k and slot, keeps the reader's view, repaints the row)
 * @property {(block: PermBlock, answer: {optionId?: string, decision?: string, feedback?: string}) => (Promise<boolean>|void)} permit
 *           answer a permission request: an option, or a decision (an option kind) when the agent offered none; feedback is
 *           a word that goes with a rejection — the host sends it the way its agent takes it
 * @property {(block: AskBlock, answer: {action: string, content?: object}) => (Promise<boolean>|void)} answer
 *           answer a question: accept (content: the form's values), decline, cancel
 * @property {(block: ChatBlock, set: ChangeSet, file: FileChange) => {state: string, text?: string, cut?: boolean, note?: string}} fileDiff
 *           one file's diff of a change set: state 'ok' (text: its unified diff; cut: not all of it), 'loading', or 'none' (note says why)
 * @property {(block: ChatBlock, file: ChatFile) => {linked: boolean, thumb?: string}} [fileState]  is a message's file still there; a thumbnail
 * @property {(block: ChatBlock, file: ChatFile) => void} [openFile]
 * @property {(block: AgentBlock) => void} [openAgent]  show the sub-conversation on its own
 * @property {(block: AgentBlock) => void} [loadAgent]  its blocks are wanted (the card was opened)
 * @property {(block: AgentBlock) => string} [agentError]  why they could not be read ('' no error)
 * @property {(block: ChatBlock, depth: number) => any} [other]  draw a block the kit has no view for — or one the host
 *           draws its own way: asked first, null leaves it to the kit
 * @property {() => void} repaint                 something a view keeps outside the blocks changed (a form's value)
 */
