/**
 * xb/vocab.js — the native vocabulary (v0), the one canonical schema.
 *
 * Every primitive a tile's native.js may render, with its props (type, enum
 * or token set), events (payload + which prop an event reports for
 * controlled state), child rules and a revision. The runtime
 * (/vendor/xb-native.js) validates every rendered node against it; the app
 * advertises what it supports as `caps` (prims: {name: rev}) and a tile
 * whose tree needs more falls back to its web page.
 *
 * native/spec/vocab.json is the checked-in JSON export of VOCAB (a node test
 * keeps them equal); the Swift renderer and XbinCore read that file. The
 * wire format itself is native/spec/tree.md.
 *
 * ADDITIVE ONLY once shipped (docs/compat.md): a new prop bumps its
 * primitive's `rev` and carries `since: <rev>`; a new primitive starts at
 * rev 1; a new enum value that needs app support names a `features` flag.
 * Removing or re-meaning anything needs a new major `v`.
 *
 * Schema (all plain JSON):
 *   prop:     {type: 'string'|'number'|'bool'|'json'|'array'|'object' | [types…],
 *              enum?: [...], token?: <tokens key>, of?: <prop>, shape?: {field: <prop>},
 *              since?: <rev>, features?: {<enum value>: <feature flag>},
 *              runtime?: true (set by the runtime, never by a tile), doc?}
 *   event:    {payload?: {field: type}, reports?: {prop, from?, value?}}
 *             `reports`: the event carries the app-side value of a controlled
 *             prop — `from` names the payload field (default: the prop's own
 *             name), `value` is a constant instead of a payload field.
 *   children: {none: true} | {any: true} | {only: [prims], min?, max?}
 *             (+ lazy: 'selected' for tabs — only the selected tab's content
 *             is materialized)
 *   text:     the prop a text-content element's content becomes
 */

const S = { type: 'string' };
const B = { type: 'bool' };
const N = { type: 'number' };
const J = { type: 'json' };
const en = (...values) => ({ type: 'string', enum: values });
const tok = (token) => ({ type: 'string', token });
const arr = (of) => ({ type: 'array', of });
const obj = (shape) => ({ type: 'object', shape });
const scalar = { type: ['string', 'number', 'bool'] };
const reports = (prop, from) => (from ? { reports: { prop, from } } : { reports: { prop } });
const NONE = { none: true };
const ANY = { any: true };
const only = (...prims) => ({ only: prims });

// Icon names → SF Symbols (the reference renderer ships an SVG per name).
// An unknown name is a diagnostic; renderers draw a neutral placeholder.
export const ICONS = {
  plus: 'plus', minus: 'minus', check: 'checkmark', xmark: 'xmark',
  copy: 'doc.on.doc', share: 'square.and.arrow.up', refresh: 'arrow.clockwise', gear: 'gearshape',
  terminal: 'apple.terminal', box: 'shippingbox', key: 'key', lock: 'lock',
  unlock: 'lock.open', globe: 'globe', shield: 'checkmark.shield', bolt: 'bolt',
  clock: 'clock', chart: 'chart.xyaxis.line', link: 'link', paperclip: 'paperclip',
  send: 'arrow.up.circle.fill', stop: 'stop.circle.fill', play: 'play.fill', pause: 'pause.fill',
  sparkles: 'sparkles', wrench: 'wrench.and.screwdriver', warning: 'exclamationmark.triangle', trash: 'trash',
  list: 'list.bullet', ellipsis: 'ellipsis', search: 'magnifyingglass', filter: 'line.3.horizontal.decrease',
  pencil: 'pencil', folder: 'folder', file: 'doc', doc: 'doc.text',
  photo: 'photo', bell: 'bell', person: 'person', people: 'person.2',
  star: 'star', heart: 'heart', pin: 'pin', archive: 'archivebox',
  tag: 'tag', calendar: 'calendar', mail: 'envelope', chat: 'bubble.left',
  info: 'info.circle', question: 'questionmark.circle', error: 'xmark.octagon', home: 'house',
  download: 'arrow.down.circle', upload: 'arrow.up.circle', cloud: 'cloud', server: 'server.rack',
  database: 'cylinder', cpu: 'cpu', network: 'network', code: 'chevron.left.forwardslash.chevron.right',
  branch: 'arrow.triangle.branch', eye: 'eye', 'eye-slash': 'eye.slash', external: 'arrow.up.right.square',
  back: 'chevron.left', forward: 'chevron.right', expand: 'chevron.down', collapse: 'chevron.up',
  power: 'power', sun: 'sun.max', moon: 'moon', agent: 'person.crop.circle.badge.checkmark',
};

export const TOKENS = {
  tone: ['muted', 'accent', 'ok', 'warn', 'danger'],
  noticeTone: ['info', 'muted', 'accent', 'ok', 'warn', 'danger'],
  type: ['largeTitle', 'title', 'title2', 'title3', 'headline', 'body', 'callout',
    'subheadline', 'footnote', 'caption', 'caption2', 'mono'],
  gap: ['none', 'xs', 's', 'm', 'l', 'xl', 'xxl'],
  // image/chart/canvas heights; renderers map them (reference: 48/96/160/240/360 pt)
  height: ['xs', 's', 'm', 'l', 'xl'],
  icon: Object.keys(ICONS),
};

// Feature flags an app may lack even when it knows the primitive.
export const FEATURES = ['chart.area', 'markdown.tables'];

const P = {
  // ── 8.1 structure and navigation ──────────────────────────────────────────
  fragment: { rev: 1, group: 'structure', runtime: true,
    doc: 'several top-level elements; no visual of its own (a nav with a sheet laid over it)',
    props: {}, events: {}, children: ANY },
  nav: { rev: 1, group: 'structure', doc: 'a navigation stack; the first screen is the root',
    props: {}, events: { pop: { payload: { depth: 'number' } } }, children: { only: ['screen'], min: 1 } },
  screen: { rev: 1, group: 'structure', doc: 'one screen: a list, a form or a scroll view with a title',
    props: { title: S, subtitle: S, style: en('list', 'form', 'scroll'), large: B, refreshable: B,
      search: { ...S, doc: 'the search query; present (even "") shows the search field' } },
    events: { refresh: {}, search: { payload: { value: 'string' }, ...reports('search', 'value') }, appear: {} },
    children: ANY },
  toolbar: { rev: 1, group: 'structure', doc: 'actions in the screen/sheet bar',
    props: {}, events: {}, children: only('button', 'menu', 'picker', 'badge') },
  section: { rev: 1, group: 'structure', doc: 'a titled group of rows, controls or content',
    props: { title: S, badge: S, footer: S, collapsible: B, collapsed: B },
    events: { toggle: { payload: { collapsed: 'bool' }, ...reports('collapsed') } }, children: ANY },
  stack: { rev: 1, group: 'structure', doc: 'a vertical or horizontal stack',
    props: { axis: en('v', 'h'), gap: tok('gap'), align: en('start', 'center', 'end'), wrap: B },
    events: {}, children: ANY },
  list: { rev: 1, group: 'structure', doc: 'a lazy list',
    props: { style: en('plain', 'inset', 'grouped') }, events: { more: {} },
    children: only('row', 'section', 'empty', 'progress', 'notice') },
  row: { rev: 1, group: 'structure', doc: 'a list row; optional swipe/context actions and content',
    props: { title: S, subtitle: S, detail: S, icon: tok('icon'), badge: S, tone: tok('tone'),
      mono: en('title', 'subtitle', 'detail', 'all'), nav: B, selected: B, disabled: B },
    events: { tap: {} }, children: ANY },
  actions: { rev: 1, group: 'structure', doc: "a row's swipe actions and context menu",
    props: {}, events: {}, children: only('button') },
  disclosure: { rev: 1, group: 'structure', doc: 'a collapsible group',
    props: { title: S, open: B }, events: { toggle: { payload: { open: 'bool' }, ...reports('open') } },
    children: ANY },
  tabs: { rev: 1, group: 'structure', doc: 'segmented tabs; only the selected tab is materialized',
    props: { selected: S, style: en('segmented', 'bar') },
    events: { change: { payload: { key: 'string' }, ...reports('selected', 'key') } },
    children: { only: ['tab'], lazy: 'selected' } },
  tab: { rev: 1, group: 'structure', doc: 'one tab of tabs; `key` is a prop here (it does not key the node)',
    props: { key: S, title: S, icon: tok('icon'), badge: S }, events: {}, children: ANY },
  sheet: { rev: 1, group: 'structure', doc: 'a modal sheet',
    props: { open: B, title: S, detents: { type: ['string', 'array'], enum: ['medium', 'large'], of: en('medium', 'large') } },
    events: { dismiss: { reports: { prop: 'open', value: false } } }, children: ANY },
  split: { rev: 1, group: 'structure', doc: 'list/detail: exactly two children; stacked when compact',
    props: { prefer: en('auto', 'single') }, events: {}, children: { any: true, min: 2, max: 2 } },
  spacer: { rev: 1, group: 'structure', props: {}, events: {}, children: NONE },
  divider: { rev: 1, group: 'structure', props: {}, events: {}, children: NONE },

  // ── 8.2 content ───────────────────────────────────────────────────────────
  text: { rev: 1, group: 'content', doc: 'verbatim text (never parsed as markup)', text: 'text',
    props: { text: S, style: tok('type'), tone: tok('tone'), mono: B, selectable: B, lines: N },
    events: {}, children: NONE },
  markdown: { rev: 1, group: 'content', doc: 'markdown; the runtime lexes `source` into `tokens`',
    props: { source: { ...S, doc: 'lexed by the runtime; the wire carries tokens instead' },
      streaming: B, tokens: { type: 'array', runtime: true, doc: 'native/spec/tree.md "Markdown tokens"' } },
    events: { link: { payload: { href: 'string' } } }, children: NONE },
  image: { rev: 1, group: 'content', doc: 'a tile-relative or data: (≤ 256 KiB) image, loaded by the app',
    props: { src: S, alt: S, aspect: en('fit', 'fill'), height: tok('height'), preview: B },
    events: { tap: {} }, children: NONE },
  icon: { rev: 1, group: 'content', props: { name: tok('icon'), tone: tok('tone') }, events: {}, children: NONE },
  badge: { rev: 1, group: 'content', text: 'text',
    props: { text: S, tone: tok('tone'), pulse: B }, events: {}, children: NONE },
  notice: { rev: 1, group: 'content', doc: 'an inset banner',
    props: { tone: tok('noticeTone'), title: S, text: S }, events: {}, children: NONE },
  progress: { rev: 1, group: 'content', doc: 'value 0…1, or absent for indeterminate',
    props: { value: N, label: S }, events: {}, children: NONE },
  chart: { rev: 1, group: 'content',
    props: { kind: { ...en('line', 'bar', 'area', 'spark'), features: { area: 'chart.area' } },
      series: arr(obj({ name: S, points: arr({ type: 'array', of: { type: ['number', 'string'] } }) })),
      x: en('time', 'number', 'category'), y: en('number', 'bytes', 'percent'), height: tok('height') },
    events: {}, children: NONE },
  code: { rev: 1, group: 'content', doc: 'monospaced text with horizontal scroll', text: 'text',
    props: { text: S, copy: B, wrap: B }, events: {}, children: NONE },
  empty: { rev: 1, group: 'content', doc: 'an empty state',
    props: { icon: tok('icon'), title: S, text: S }, events: {}, children: NONE },

  // ── 8.3 controls ──────────────────────────────────────────────────────────
  button: { rev: 1, group: 'control', text: 'label',
    props: { label: S, icon: tok('icon'), role: en('primary', 'secondary', 'destructive', 'plain'),
      disabled: B, busy: B, confirm: obj({ title: S, message: S, label: S, destructive: B }),
      copy: { ...S, doc: 'copied natively on tap (no round trip)' } },
    events: { tap: {} }, children: NONE },
  toggle: { rev: 1, group: 'control', props: { label: S, value: B, disabled: B },
    events: { change: { payload: { value: 'bool' }, ...reports('value') } }, children: NONE },
  field: { rev: 1, group: 'control', doc: 'a text field; app-owned while focused',
    props: { label: S, value: S,
      kind: en('text', 'secure', 'number', 'email', 'url', 'multiline', 'search', 'date', 'time'),
      placeholder: S, hint: S, error: S, disabled: B,
      submit: { ...S, doc: 'the return-key label (go, send, done, search, next)' } },
    events: { input: { payload: { value: 'string' }, ...reports('value') },
      change: { payload: { value: 'string' }, ...reports('value') },
      submit: { payload: { value: 'string' }, ...reports('value') } },
    children: NONE },
  picker: { rev: 1, group: 'control',
    props: { label: S, value: scalar, options: arr(obj({ value: scalar, label: S, icon: tok('icon') })),
      style: en('menu', 'segmented', 'inline') },
    events: { change: { payload: { value: 'json' }, ...reports('value') } }, children: NONE },
  menu: { rev: 1, group: 'control', doc: 'a pull-down; its buttons fire',
    props: { label: S, icon: tok('icon') }, events: {}, children: only('button', 'divider') },

  // ── 8.4 the chat family ───────────────────────────────────────────────────
  transcript: { rev: 1, group: 'chat', doc: 'a chat transcript (stick to bottom with follow)',
    props: { follow: B, older: B },
    events: { more: {}, scrolled: { payload: { atBottom: 'bool' } } },
    children: only('message', 'thinking', 'toolcard', 'approval', 'question', 'plan', 'diff',
      'activity', 'step', 'notice', 'text', 'markdown', 'image', 'progress') },
  message: { rev: 1, group: 'chat',
    props: { role: en('user', 'assistant', 'system'), sender: S, text: S, markdown: B, streaming: B,
      time: { type: ['string', 'number'] }, files: arr(obj({ name: S, mime: S, src: S })), queued: B,
      tokens: { type: 'array', runtime: true, doc: 'added by the runtime when markdown is set' } },
    events: { tap: {} }, children: only('actions') },
  thinking: { rev: 1, group: 'chat', props: { text: S, live: B, seconds: N, open: B },
    events: { toggle: { payload: { open: 'bool' }, ...reports('open') } }, children: NONE },
  toolcard: { rev: 1, group: 'chat',
    props: { title: S, icon: tok('icon'), family: S, state: en('writing', 'running', 'ok', 'error', 'canceled'),
      chips: arr(obj({ text: S, tone: tok('tone') })), open: B },
    events: { toggle: { payload: { open: 'bool' }, ...reports('open') }, open: {} },
    children: only('code', 'text', 'diff', 'image', 'transcript', 'markdown', 'notice') },
  approval: { rev: 1, group: 'chat',
    props: { title: S, text: S, options: arr(obj({ id: S, label: S, kind: S })), note: S, feedback: B,
      settled: obj({ by: S, id: S }) },
    events: { choose: { payload: { id: 'string', feedback: 'string' } } }, children: NONE },
  question: { rev: 1, group: 'chat', props: { title: S, schema: J, settled: J },
    events: { submit: { payload: { content: 'json' } }, skip: {} }, children: NONE },
  plan: { rev: 1, group: 'chat',
    props: { entries: arr(obj({ text: S, status: en('pending', 'in_progress', 'completed') })) },
    events: {}, children: NONE },
  diff: { rev: 1, group: 'chat',
    props: { files: arr(obj({ path: S, status: S, add: N, del: N })), patch: S },
    events: { 'open-file': { payload: { path: 'string' } } }, children: NONE },
  activity: { rev: 1, group: 'chat', props: { text: S, live: B }, events: {}, children: NONE },
  step: { rev: 1, group: 'chat', props: { glyph: S, text: S, tone: tok('tone') }, events: {}, children: NONE },
  composer: { rev: 1, group: 'chat', doc: 'the message composer; attachments are uploaded by the app',
    props: { value: S, placeholder: S, busy: B, disabled: B,
      attachments: arr(obj({ id: S, name: S, mime: S, progress: N })), accept: S,
      upload: obj({ method: S, path: S }), slash: arr(obj({ name: S, hint: S, description: S })) },
    events: { input: { payload: { value: 'string' }, ...reports('value') },
      send: { payload: { value: 'string' }, ...reports('value') }, stop: {},
      uploaded: { payload: { name: 'string', response: 'json' } }, remove: { payload: { id: 'string' } } },
    children: only('button') },

  // ── 8.5 escape hatches ────────────────────────────────────────────────────
  terminal: { rev: 1, group: 'escape', doc: "a terminal on the tile's own pty WebSocket (tile-relative)",
    props: { src: S, title: S }, events: {}, children: NONE },
  canvas: { rev: 1, group: 'escape', doc: 'a WebView island: a tile page (src) or static no-script html',
    props: { src: S, html: S, height: tok('height') }, events: {}, children: NONE },
};

export const VOCAB = { v: 1, tokens: TOKENS, icons: ICONS, features: FEATURES, prims: P };

// The caps an app that supports everything in this vocabulary would send —
// the runtime's default when the app injected none (previews, node tests).
export function fullCaps() {
  const prims = {};
  for (const [name, p] of Object.entries(P)) prims[name] = p.rev;
  return { v: VOCAB.v, renderer: 'xb-native', app: null, prims, features: [...FEATURES] };
}
