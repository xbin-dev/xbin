// backend.mjs — the tile, served for a test, against an in-page fake backend.
//
// serveTile(ctx) answers the tile's own files (index.html, every module next
// to it, the shared model under model/ and the native view under native/),
// the kit, lit and marked. STUB is
// the fake backend a test installs with ctx.addInitScript(STUB, seed): a
// window.xbin whose fetch answers the routes the tile uses — the run list,
// run views, messages, the queue, interrupts, approvals, the classes (D116:
// the three built-ins, or seed.classes; PUT refuses a mixed class it was not
// told to confirm, and the harness toolset without a sandbox that reaches
// out), the coding sandboxes (D115: seed.sandboxes and
// seed.sbxManagers — GET/POST/PATCH/DELETE /sandboxes, their lifecycle, and
// PATCH /runs {sandbox, detach} into the view's config), the coding
// harnesses (D147 §4, until the backend serves them: the
// catalog seed.harnesses, the per-person modes seed.harnessModes, a harness
// ask or run, GET|PATCH /runs/{id}/harness, …/answer, …/authenticate, …/log,
// approve {option, feedback}, conversation rows' waiting and kids, seed.trees,
// a person's message to a harness child (its parent's notice), a harness run's
// cancel — test/harness-fixtures.mjs has a seed of each) — and a live stream
// the test drives with window.__push(event) (the same SSE the real backend
// writes). Tests add or override routes with window.__route(method, regexp,
// fn) from their own init script (fn answering null leaves the request to
// the stub), and read what the tile sent from window.__calls.
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { serveKit, tileHtml } from './kit.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const tileDir = join(here, '..');
// lit and marked come from the xbin checkout's web/vendor (BX_VENDOR in an instance).
const vendor = process.env.BX_VENDOR || join(here, '..', '..', '..', 'web', 'vendor');
export const ORIGIN = 'http://tile.test';

export const THEME = '<style>:root{--bx-border:#ccc;--bx-panel:#fff;--bx-panel-2:#f4f4f4;--bx-text:#111;' +
  '--bx-muted:#777;--bx-accent:#b57e10;--bx-mono:monospace;--bx-red:#c33;--bx-green:#3a3}</style>';

export async function serveTile(ctx, { realMarked = false, noWindow = false } = {}) {
  const modules = new Set([...readdirSync(tileDir).filter((f) => f.endsWith('.js')),
    ...['model', 'native'].flatMap((d) => readdirSync(join(tileDir, d)).filter((f) => f.endsWith('.js')).map((f) => `${d}/${f}`))]);
  await ctx.route(`${ORIGIN}/**`, (route) => {
    let path = new URL(route.request().url()).pathname.replace(/^\//, '') || 'index.html';
    if (path !== 'index.html' && !modules.has(path)) return route.fulfill({ status: 404, body: '' });
    let body = readFileSync(join(tileDir, path), 'utf8');
    if (path === 'index.html') body = tileHtml(body).replace(/<link rel="stylesheet" href="\/vendor\/theme.css">/, THEME);
    route.fulfill({ contentType: path.endsWith('.js') ? 'text/javascript' : 'text/html', body });
  });
  await serveKit(ctx);
  await ctx.route('**/vendor/lit-all.min.js', (r) =>
    r.fulfill({ contentType: 'text/javascript', body: readFileSync(join(vendor, 'lit-all.min.js'), 'utf8') }));
  // the chat's window (chat-window.js); without it the tile renders every block, as on an older xbind
  const sw = join(vendor, '..', 'scroll-window.js');
  await ctx.route('**/vendor/scroll-window.js', (r) => (noWindow || !existsSync(sw) ? r.fulfill({ status: 404, body: '' })
    : r.fulfill({ contentType: 'text/javascript', body: readFileSync(sw, 'utf8') })));
  await ctx.route('**/vendor/marked.esm.js', (r) => r.fulfill({
    contentType: 'text/javascript',
    body: realMarked ? readFileSync(join(vendor, 'marked.esm.js'), 'utf8') : 'export const marked={parse:(s)=>s,use(){}};',
  }));
}

// STUB runs in the page (addInitScript): seed = {runs: [...], views: {id: view}}.
export function STUB(seed) {
  window.__calls = [];
  window.__runs = seed.runs || [];
  window.__views = seed.views || {};
  window.__prefs = {};
  const routes = [];
  window.__route = (method, re, fn) => routes.unshift({ method, re, fn });
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  window.__json = json;

  // The live stream: every open stream gets what __push sends.
  const streams = new Set();
  let seq = 100;
  window.__push = (ev) => {
    ev.seq = ev.seq || ++seq;
    ev.ts = ev.ts || Date.now();
    const line = `id: g.${ev.seq}\ndata: ${JSON.stringify(ev)}\n\n`;
    for (const c of streams) { try { c.enqueue(new TextEncoder().encode(line)); } catch { streams.delete(c); } }
  };
  window.__streams = () => streams.size;

  const view = (id) => {
    const v = window.__views[id];
    const run = (v && v.run) || window.__runs.find((r) => r.id === id) || { id, status: 'idle', title: 'run ' + id };
    // a conversation's class is resolved by its id, as _backend does: one a
    // PUT /classes stored is read as saved
    const cid = v && v.class && v.class.id;
    const saved = cid && window.__classes.classes.some((c) => c.id === cid) ? classesView().classes.find((c) => c.id === cid) : null;
    return { cursor: 'g.' + seq, run: { pendingState: {}, ...run }, messages: [], steps: [], links: [], queued: [], drafts: [],
      chain: [], files: [], memory: {}, config: {}, messageFiles: {}, ...(v || {}), ...(saved ? { class: saved } : {}) };
  };

  const base = [
    ['GET', /\/api\/xbin\/prefs\/(.+)$/, (m) => (m[1] in window.__prefs ? json(window.__prefs[m[1]]) : json({}, 404))],
    ['PUT', /\/api\/xbin\/prefs\/(.+)$/, (m, o) => { window.__prefs[m[1]] = JSON.parse(o.body); return json({}); }],
    ['GET', /\/runs\?roots=1$/, () => json(window.__runs.filter((r) => !r.parentId))],
    // the conversation list (D83): roots, newest activity first; pins apart
    ['GET', /\/conversations\?(.*)$/, (m) => {
      const q = new URLSearchParams(m[1]);
      const conv = (r) => ({ access: 'owner', mine: true, visibility: 'private', origin: 'chat', activityMs: r.id * 1000, ...r, ...below(r) });
      let rows = window.__runs.filter((r) => !r.parentId).map(conv).filter((r) => ['', 'chat', 'api'].includes(r.origin));
      if (q.get('q')) {
        const t = q.get('q').toLowerCase();
        return json({ pinned: [], items: rows.filter((r) => (r.title || '').toLowerCase().includes(t)), next: '' });
      }
      rows = rows.filter((r) => !!r.archivedAt === (q.get('archived') === '1'));
      if (q.get('scope') === 'team') rows = rows.filter((r) => !r.mine && r.visibility === 'team');
      if (q.get('scope') === 'shared') rows = rows.filter((r) => r.visibility === 'team' || (r.members || 0) > 0);
      rows.sort((a, b) => b.activityMs - a.activityMs || b.id - a.id);
      return json({ pinned: rows.filter((r) => r.pinnedAt), items: rows.filter((r) => !r.pinnedAt), next: '' });
    }],
    ['PATCH', /\/runs\/(\d+)$/, (m, o) => {
      const r = window.__runs.find((x) => x.id === +m[1]) || {};
      const b = JSON.parse(o.body);
      if ('sandbox' in b || 'detach' in b) {
        const why = bindRun(+m[1], b);
        if (why) return json({ error: why }, 403);
      }
      if ('pinned' in b) r.pinnedAt = b.pinned ? Date.now() : 0;
      if ('archived' in b) r.archivedAt = b.archived ? Date.now() : 0;
      if (b.title) r.title = b.title;
      if (b.visibility) r.visibility = b.visibility;
      return json({ access: 'owner', mine: true, origin: 'chat', activityMs: r.id * 1000, ...r });
    }],
    ['POST', /\/runs\/(\d+)\/read$/, () => json({ readMs: Date.now() })],
    ['GET', /\/needs$/, () => json({ items: seed.needs || [] })],
    // automations (D83)
    ['GET', /\/automations\?summary=1$/, () => json({ count: (seed.automations || []).length,
      unread: (seed.automations || []).reduce((n, a) => n + (a.access === 'oversee' ? 0 : a.unread || 0), 0), failing: 0,
      attention: (seed.automations || []).reduce((n, a) => n + (['owner', 'claim'].includes(a.access) ? a.attention || 0 : 0), 0) })],
    ['GET', /\/automations$/, () => json({ items: seed.automations || [] })],
    ['GET', /\/automations\/(\w+)\/(\d+)\/runs/, (m) => json({ items: (seed.autoRuns || {})[m[2]] || [], next: '' })],
    ['POST', /\/automations\/(\w+)\/(\d+)\/(read|reset)$/, () => json({ ok: 'true' })],
    ['POST', /\/schedules$/, (m, o) => {
      const b = JSON.parse(o.body);
      const it = { kind: b.watcher ? 'watcher' : 'schedule', id: 99, name: b.name, access: 'owner', enabled: true, mode: b.mode,
        visibility: b.visibility, config: { cron: b.cron, goal: b.goal, class: b.class || '', toolset: b.toolset || 'private' }, runs: 0, unread: 0 };
      (seed.automations = seed.automations || []).push(it);
      return json({ id: 99, watcher: !!b.watcher, ...b });
    }],
    ['PUT', /\/schedules\/(\d+)$/, (m) => json({ id: +m[1] })],
    ['POST', /\/schedules\/(\d+)\/trigger$/, () => json({ ok: 'true' })],
    ['GET', /\/runs\/(\d+)\/view(?:\?.*)?$/, (m) => json(view(+m[1]))], // a paged read (the native view) gets it all
    ['GET', /\/stream\b/, (m, o) => new Response(new ReadableStream({
      start(c) {
        streams.add(c);
        c.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: 'hello', data: { cursor: 'g.' + seq } })}\n\n`));
        o.signal && o.signal.addEventListener('abort', () => { streams.delete(c); try { c.close(); } catch { /* closed */ } });
      },
    }), { headers: { 'Content-Type': 'text/event-stream' } })],
    ['POST', /\/runs\/(\d+)\/message$/, (m, o) => harnessMessage(+m[1], o) || json({ ok: 'true', inboxId: 1, queued: false })],
    ['POST', /\/runs\/(\d+)\/interrupt$/, () => json({ ok: 'true', returned: [] })],
    // as _backend/inbox.go: a verdict naming an ask that is no longer pending is refused
    ['POST', /\/runs\/(\d+)\/approve$/, (m, o) => {
      const b = JSON.parse((o && o.body) || '{}');
      const now = ((window.__views[+m[1]] || {}).run || window.__runs.find((r) => r.id === +m[1]) || {}).pendingState?.park;
      if (b.park && now && b.park !== now) return json({ error: 'that approval is no longer pending — the agent is asking something else now' }, 409);
      return harnessApprove(+m[1], b) || json({ ok: 'true' });
    }],
    ['DELETE', /\/runs\/(\d+)\/inbox\/(\d+)$/, () => json({ ok: 'true' })],
    ['GET', /\/halt$/, () => json({ on: false })],
    ['GET', /\/me$/, () => json(seed.me || { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false })],
    ['GET', /\/runs\/(\d+)\/tree$/, (m) => json((seed.trees || {})[m[1]] || { root: +m[1], nodes: [], totals: {} })],
    // agent classes (D116), as _backend/classes.go answers them
    ['GET', /\/classes$/, () => json(classesView())],
    ['PUT', /\/classes$/, (m, o) => {
      const b = JSON.parse(o.body);
      // the harness toolset needs sandbox and an egress other than none (D147 §4.3.11)
      const lame = (b.classes || []).find((c) => (c.toolsets || []).includes('harness')
        && (!(c.toolsets || []).includes('sandbox') || !(c.sandboxEgress || []).some((e) => e !== 'none')));
      if (lame) return json({ error: `class ${lame.id}: the harness toolset needs sandbox and an egress other than none — a coding agent must reach its provider` }, 400);
      const mixed = (b.classes || []).filter((c) => reach(c).mixed).map((c) => c.id);
      if (mixed.length && !b.confirmMixed) return json({ error: 'these classes can move internal data out: ' + mixed.join(', '), mixed }, 409);
      window.__classes = { classes: b.classes || [], default: b.default || '' };
      return json(classesView());
    }],
  ];
  // The stored classes (window.__classes: {classes, default}): the built-ins
  // first (as stored, or their default), then the rest. GET filters by who
  // for a non-manager.
  const BUILTIN = [
    { id: 'internal', name: 'Internal', icon: '🔒', description: 'Your workspace\'s systems and data (xbin_call, MCP servers) — no web.',
      toolsets: ['files', 'repl', 'internal', 'subagents', 'schedule', 'threads', 'skills'], mcp: 'all', managers: [], sandboxEgress: [] },
    { id: 'web', name: 'Web', icon: '🌐', description: 'Searches and reads the web — no internal systems.',
      toolsets: ['files', 'repl', 'web', 'subagents', 'schedule', 'threads', 'skills'], mcp: [], managers: [], sandboxEgress: [] },
    { id: 'coding', name: 'Coding', icon: '▣', description: 'Works in a coding sandbox, with the web — no internal systems.',
      toolsets: ['sandbox', 'web', 'files', 'subagents', 'skills', 'harness'], mcp: [], managers: 'all', sandboxEgress: ['none', 'internet'], harnesses: 'all' },
  ];
  window.__classes = seed.classes || { classes: [], default: '' };
  const reach = (c) => {
    const ts = c.toolsets || [];
    const egress = ts.includes('web') || (ts.includes('sandbox') && (c.sandboxEgress || []).some((e) => e !== 'none'));
    return { mixed: ts.includes('internal') && egress, lane: egress && !ts.includes('internal') ? 'web' : 'private', egress };
  };
  const classesView = () => {
    const st = window.__classes;
    const saved = (id) => st.classes.find((c) => c.id === id);
    const all = [...BUILTIN.map((b) => (saved(b.id) ? { ...saved(b.id), stored: true } : { ...b, stored: false })),
      ...st.classes.filter((c) => !BUILTIN.some((b) => b.id === c.id)).map((c) => ({ ...c, stored: true }))]
      .map((c) => ({ description: '', icon: '', model: '', system: '', who: 'everyone', mcp: [], managers: [], sandboxEgress: [], ...c,
        builtin: ['internal', 'web', 'coding'].includes(c.id), ...reach(c) }));
    const me = seed.me || { manager: true };
    const mine = all.filter((c) => c.who !== 'managers' || me.manager);
    const def = mine.some((c) => c.id === st.default) ? st.default : mine.some((c) => c.id === 'internal') ? 'internal' : (mine[0] || {}).id;
    return { classes: mine, default: def };
  };
  // The coding sandboxes (D115), as _backend/sandbox_routes.go answers them:
  // window.__sbx = {sandboxes, managers}; a sandbox in a route's path is
  // percent-encoded (| → %7C) with its slashes as they are.
  const MGR = { provider: 'apps/coding-sandbox', title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'tar', 'archive'],
    egress: ['none', 'internet', 'open'], images: [{ id: 'base', title: 'Debian', default: true }, { id: 'go', title: 'Go' }],
    sizes: [{ id: 'small', memMiB: 2048, vcpus: 2, diskGiB: 20, default: true }], limits: {} };
  window.__sbx = { sandboxes: seed.sandboxes || [], managers: seed.sbxManagers || [MGR] };
  const box = (ref) => window.__sbx.sandboxes.find((x) => x.ref === ref);
  const refOf = (enc) => decodeURIComponent(enc);
  const binding = (s, cwd) => ({ ref: s.ref, cwd: cwd || s.workdir || '/work', name: s.name, manager: s.manager, image: (s.image || {}).id || '',
    egress: s.egress, by: (seed.me || {}).user || 'admin', at: Date.now() });
  const bindRun = (id, b) => {
    const v = window.__views[id] || (window.__views[id] = {});
    const cfg = v.config || (v.config = {});
    if (b.detach) {
      cfg.attached = (cfg.attached || []).filter((a) => a.ref !== b.detach);
      if (cfg.sandbox && cfg.sandbox.ref === b.detach) delete cfg.sandbox;
    }
    if (b.sandbox === null) delete cfg.sandbox;
    else if (b.sandbox) {
      const s = box(b.sandbox.ref);
      if (!s) return 'no such sandbox';
      if (b.sandbox.cwd && !b.sandbox.cwd.startsWith('/')) return 'sandbox.cwd: an absolute path in the sandbox';
      cfg.sandbox = binding(s, b.sandbox.cwd);
      cfg.attached = [...(cfg.attached || []).filter((a) => a.ref !== s.ref), cfg.sandbox];
      s.boundTo = [...new Set([...(s.boundTo || []), id])];
    }
    return '';
  };
  base.push(
    ['GET', /\/sandboxes(\?fresh=1)?$/, () => json(window.__sbx)],
    ['POST', /\/sandboxes$/, (m, o) => {
      const b = JSON.parse(o.body);
      const s = { ref: `${b.provider || MGR.provider}|sb-${b.name}`, provider: b.provider || MGR.provider, manager: MGR.title, id: 'sb-' + b.name,
        name: b.name, state: 'running', egress: b.egress || 'none', visibility: b.visibility || 'private', image: { id: b.image || 'base' },
        owner: { user: (seed.me || {}).user || 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work',
        caps: MGR.caps, lastActive: Date.now() };
      window.__sbx.sandboxes.unshift(s);
      if (b.conversation && b.bind !== false) { bindRun(b.conversation, { sandbox: { ref: s.ref, cwd: b.cwd } }); s.binding = window.__views[b.conversation].config.sandbox; }
      return json(s, 201);
    }],
    ['POST', /\/sandboxes\/(.+)\/(start|stop|archive|thaw)\?/, (m) => {
      const s = box(refOf(m[1]));
      if (!s) return json({ error: 'no such sandbox' }, 404);
      s.state = { start: 'running', stop: 'stopped', archive: 'archived', thaw: 'stopped' }[m[2]];
      if (m[2] === 'start' || m[2] === 'thaw') s.lastActive = Date.now(); // it was active just now
      return json(s);
    }],
    ['GET', /\/sandboxes\/(.+)$/, (m) => {
      const s = box(refOf(m[1]));
      return s ? json(s) : json({ error: 'no such sandbox' }, 404);
    }],
    // a sandbox with a version refuses a PATCH made at another one (412
    // precondition, as a manager does), and moves on with every change
    ['PATCH', /\/sandboxes\/(.+)$/, (m, o) => {
      const s = box(refOf(m[1]));
      if (!s) return json({ error: 'no such sandbox' }, 404);
      const b = JSON.parse(o.body);
      if (b.version != null && s.version != null && b.version !== s.version) {
        return json({ error: `apps/coding-sandbox: version ${b.version} is not ${s.version}`, refusal: 'precondition' }, 412);
      }
      delete b.version;
      Object.assign(s, b);
      if (s.version != null) s.version++;
      return json(s);
    }],
    ['DELETE', /\/sandboxes\/(.+)$/, (m) => {
      const ref = refOf(m[1]);
      window.__sbx.sandboxes = window.__sbx.sandboxes.filter((x) => x.ref !== ref);
      for (const [id, v] of Object.entries(window.__views)) if (JSON.stringify(v.config || {}).includes(ref)) bindRun(+id, { detach: ref });
      return json({ ok: true, detached: 0 });
    }],
  );
  // --- coding harnesses (D147 §4) ------------------------------------
  // window.__harness = {catalog, modes, logs}: GET /harnesses' entries
  // (§4.3.10, `setting` from modes), the caller's Auto / Always approve
  // (§4.3.12), each run's adapter stderr. A harness run is a run with
  // engine "harness" and its `harness` summary (§4.3.2) — its view may hold
  // harnessSession and harnessRules for GET /runs/{id}/harness. Changes land
  // in the run as the stub holds it and go out as the backend's events.
  const H = window.__harness = { catalog: seed.harnesses || [], modes: { ...(seed.harnessModes || {}) }, logs: seed.harnessLogs || {} };
  const person = () => (seed.me || { kind: 'user' }).kind === 'user';
  const hentry = (id) => H.catalog.find((h) => h.id === id);
  const hname = (id) => (hentry(id) || {}).name || id;
  const runsOf = (id) => [(window.__views[id] || {}).run, window.__runs.find((r) => r.id === id)].filter(Boolean);
  const hrun = (id) => runsOf(id).find((r) => r.engine === 'harness') || null;
  const notHarness = () => json({ error: 'not a coding-agent conversation' }, 409);
  // setRun changes a run where the stub holds it and says so on the stream
  const setRun = (id, patch) => {
    for (const r of runsOf(id)) Object.assign(r, patch);
    const r = runsOf(id)[0] || { id };
    window.__push({ type: 'run', run: id, root: r.rootId || id, data: { id, ...patch, harness: r.harness } });
  };
  const setHarness = (id, patch) => {
    const r = hrun(id);
    const h = { ...r.harness, ...patch };
    for (const x of runsOf(id)) x.harness = h;
    window.__push({ type: 'harness', run: id, root: r.rootId || id, data: h });
    return h;
  };
  // unpark: a park answered — the run goes on (the backend's next events say more)
  const unpark = (id) => {
    const h = { ...hrun(id).harness, state: 'working' };
    delete h.pending;
    setRun(id, { status: 'running', pendingState: {}, harness: h });
  };
  const oneOf = (xs) => xs.join(', ');
  // --- saved sign-ins and the guided sign-in (D179) -----------------------
  // window.__signins = {available, why, list, harnesses}: GET
  // /prefs/harness-signins (seed.signins; seed.signinsAvailable false: an
  // unpartitioned agent's, with seed.signinsWhy), never a secret. A guided
  // sign-in (authenticate {method: "guided"}) answers its link (202); a code
  // without '#' is "not the whole code" (409), bad… is the CLI's refusal
  // (502), any other signs in — with remember, a saved sign-in named so.
  const SIGNIN_URL = 'https://claude.com/cai/oauth/authorize?code=true&client_id=stub&response_type=code&scope=user%3Ainference&state=stub-state';
  const CLAUDE_KEYS = [{ env: 'CLAUDE_CODE_OAUTH_TOKEN', label: 'Claude subscription token (claude setup-token)', kind: 'setup-token', prefix: 'sk-ant-oat' },
    { env: 'ANTHROPIC_API_KEY', label: 'Anthropic API key', kind: 'api-key' }];
  const SI = window.__signins = {
    available: seed.signinsAvailable !== false, why: seed.signinsWhy || '', list: (seed.signins || []).map((x) => ({ ...x })),
    harnesses: seed.signinHarnesses || { claude: { name: 'Claude Code', keys: CLAUDE_KEYS, mint: true },
      codex: { name: 'Codex', keys: [{ env: 'CODEX_API_KEY', label: 'OpenAI API key', kind: 'api-key' }], mint: true },
      opencode: { name: 'OpenCode', keys: [{ env: 'ANTHROPIC_API_KEY', label: 'Anthropic API key', kind: 'api-key', prefix: 'sk-ant-' },
        { env: 'OPENAI_API_KEY', label: 'OpenAI API key', kind: 'api-key', prefix: 'sk-' }], mint: false } },
  };
  let siSeq = SI.list.length;
  const siWhy = () => SI.why || 'saved sign-ins need a partitioned agent';
  const addSignin = (x) => {
    const mine = SI.list.filter((s) => s.harness === x.harness);
    const name = x.name || (mine.length ? `Sign-in ${mine.length + 1}` : 'Personal');
    const same = mine.find((s) => s.name.toLowerCase() === name.toLowerCase());
    if (same) { Object.assign(same, { kind: x.kind, env: x.env, refusedAt: 0, mintedAt: x.mintedAt || 0, expiresAt: x.expiresAt || 0 }); return same; }
    const s = { id: `hs${++siSeq}`, harness: x.harness, name, kind: x.kind, env: x.env, isDefault: !mine.length, createdAt: Date.now(), updatedAt: Date.now(),
      ...(x.mintedAt ? { mintedAt: x.mintedAt, expiresAt: x.expiresAt } : {}) };
    SI.list.push(s);
    return s;
  };
  const guidedSignin = (id, r, b) => {
    const h = r.harness;
    const G = (H.guided = H.guided || {});
    if (!b.code) {
      if (b.remember && !SI.available) return json({ error: siWhy() }, 409);
      if (!b.remember && (h.sandbox || {}).shared && !b.confirm) {
        return json({ error: `anyone who may use ${(h.sandbox || {}).name} acts as you with ${h.name} there — confirm to sign in`, confirm: true }, 409);
      }
      G[id] = { remember: !!b.remember, name: b.name || '' };
      return json({ ok: 'true', signin: { url: SIGNIN_URL, paste: true } }, 202);
    }
    const g = G[id];
    if (!g) return json({ error: 'no sign-in of yours is under way here — start one' }, 409);
    const code = String(b.code);
    if (!code.includes('#')) return json({ error: `${h.name} says that isn't the whole code — copy it again from the sign-in page and paste it` }, 409);
    delete G[id];
    if (code.startsWith('bad')) return json({ error: 'Login failed: Request failed with status code 400' }, 502);
    const now = Date.now();
    const saved = g.remember ? addSignin({ harness: h.provider, name: g.name, kind: 'setup-token', env: 'CLAUDE_CODE_OAUTH_TOKEN', mintedAt: now, expiresAt: now + 365 * 86400000 }) : null;
    const next = { ...h, state: 'ready' };
    delete next.login; delete next.pending;
    if (saved) next.signin = { pick: (h.signin || {}).pick || 'default', using: { id: saved.id, name: saved.name } };
    setRun(id, { status: 'running', pendingState: {}, harness: next });
    return json({ ok: 'true', state: 'ready', ...(saved ? { saved } : {}) });
  };
  // below: a conversation row's waiting and kids (§4.3.8), from the runs under it
  const LIVE = ['running', 'awaiting', 'sleeping', 'waiting_input'];
  const below = (r) => {
    const kids = window.__runs.filter((x) => x.id !== r.id && (x.rootId === r.id || x.parentId === r.id));
    const k = { harness: kids.filter((x) => x.engine === 'harness' && LIVE.includes(x.status)).length, waiting: kids.filter((x) => x.status === 'waiting_input').length };
    return { ...(r.status === 'waiting_input' || k.waiting ? { waiting: true } : {}), ...(k.harness || k.waiting ? { kids: k } : {}) };
  };
  // startHarness: POST /ask or /runs with a harness (§4.2.3) — refused as the
  // backend refuses, else a new run (and its view) with the first message
  const startHarness = (b, text) => {
    const hb = b.harness || {};
    const h = hentry(hb.provider);
    if (!h) return json({ error: `harness.provider: no coding agent "${hb.provider || ''}" (GET /harnesses lists them)` }, 400);
    if (b.system) return json({ error: 'system: a coding agent keeps its own instructions — system is for the built-in agent' }, 400);
    if (b.model) return json({ error: "model: a coding agent's model is harness.options.model" }, 400);
    const opts = hb.options || {};
    if ('mode' in opts || (h.options || []).some((x) => x.category === 'mode' && x.id in opts)) return json({ error: 'harness.options: the mode is harness.mode' }, 400);
    if (b.class && !(h.classes || []).includes(b.class)) return json({ error: `class: the ${b.class} class doesn't allow ${h.name}` }, 400);
    const cls = b.class || (h.classes || [])[0];
    if (!cls) return json({ error: `no class you may use allows ${h.name}` }, 403);
    if (hb.mode && !(h.modes || []).some((x) => x.id === hb.mode)) return json({ error: `harness.mode: one of ${oneOf((h.modes || []).map((x) => x.id))}` }, 400);
    if (hb.mode && !person()) return json({ error: `only a person can start ${h.name} in ${hb.mode}` }, 403);
    if (!b.sandbox || !b.sandbox.ref) return json({ error: `a coding agent needs a sandbox: sandbox {ref, cwd?} whose image has ${h.name}` }, 400);
    const s = box(b.sandbox.ref);
    if (!s) return json({ error: 'no such sandbox' }, 404);
    if (![s.egress, s.egressNext].some((e) => e && e !== 'none')) return json({ error: `${h.name} must reach its provider — ${s.name}'s egress is none` }, 409);
    if (!(h.images || []).some((i) => i.provider === s.provider && i.image === ((s.image || {}).id || s.image))) return json({ error: `${s.name}'s image doesn't have ${h.name}` }, 409);
    const id = Math.max(100, ...window.__runs.map((r) => r.id)) + 1;
    const setting = H.modes[h.id] || 'approve';
    const mode = hb.mode || (setting === 'auto' ? h.autoMode : h.approveMode) || h.defaultMode || '';
    const harness = { provider: h.id, name: h.name, state: b.hold ? 'stopped' : 'starting', error: '',
      mode: { current: mode, available: h.modes || [] }, options: h.options || [], commands: [], counts: { tools: 0, files: 0, add: 0, del: 0 },
      sandbox: { ref: s.ref, name: s.name, cwd: b.sandbox.cwd || s.workdir || '/work', shared: s.visibility === 'team' || (s.shares || []).length > 0 },
      steering: false, title: '', gen: 0 };
    const run = { id, title: b.title || String(text || '').slice(0, 60), status: b.hold ? 'idle' : 'running', parentId: 0, rootId: id,
      engine: 'harness', harness }; // the Run JSON + its summary (the class is the view's)
    window.__runs.push(run);
    window.__views[id] = { access: 'owner', run, class: classesView().classes.find((c) => c.id === cls) || { id: cls },
      config: { sandbox: binding(s, b.sandbox.cwd), engine: 'harness', harness: { provider: h.id, mode, options: opts, ref: s.ref, cwd: b.sandbox.cwd || '' } },
      messages: b.hold || !text ? [] : [{ id: 1, runId: id, seq: 1, role: 'user', content: text, created: Math.floor(Date.now() / 1000) }] };
    window.__push({ type: 'run', run: id, root: id, data: run });
    return json(run);
  };
  // harnessApprove: a verdict on a harness park (§4.2.9); null: not one (the built-in answer stands)
  const harnessApprove = (id, b) => {
    const r = hrun(id);
    const ps = r && r.pendingState;
    if (!r || !ps || !ps.harness || ps.kind !== 'approval') {
      return b.option ? json({ error: "option is for a coding agent's permission request" }, 400) : null;
    }
    const opts = ps.harness.options || [];
    const o = b.option ? opts.find((x) => x.optionId === b.option) : null;
    if (b.option && !o) return json({ error: `option: one of ${oneOf(opts.map((x) => x.optionId))}` }, 400);
    const allow = o ? o.kind.startsWith('allow') : !!b.approve;
    if (b.feedback && allow) return json({ error: 'feedback goes with a rejection' }, 400);
    if (o && o.explicit && (window.__views[id] || {}).access !== 'owner') return json({ error: `only the owner can allow ${o.name}` }, 403);
    unpark(id);
    return json({ ok: 'true' });
  };
  // harnessMessage: a person's message to a harness CHILD (§4.2.10): its
  // parent is told (§4.3.13) — the stub delivers that notice at once, as the
  // parent's user row (the backend: an hnote, at its next step); null: not one
  let noteId = 9000;
  const harnessMessage = (id, o) => {
    const r = hrun(id);
    if (!r || !r.parentId || !person()) return null;
    const b = JSON.parse((o && o.body) || '{}');
    const who = (seed.me || {}).user || 'admin';
    const pv = window.__views[r.parentId] || (window.__views[r.parentId] = {});
    const msgs = pv.messages || (pv.messages = []);
    const msg = { id: ++noteId, runId: r.parentId, seq: Math.max(0, ...msgs.map((x) => x.seq || 0)) + 1, role: 'user', created: Math.floor(Date.now() / 1000),
      content: `[direct message to #${id} (${r.harness.name || hname(r.harness.provider)}) from ${who}]\n${b.text || ''}` };
    msgs.push(msg);
    setTimeout(() => window.__push({ type: 'message', run: r.parentId, root: r.rootId || r.parentId, data: msg }), 0);
    return json({ ok: 'true', inboxId: noteId, queued: LIVE.includes(r.status) });
  };
  // cancelRun: a harness run canceled for good (its adapter stopped); the link to it settles canceled
  const cancelRun = (id) => {
    const r = hrun(id);
    setRun(id, { status: 'canceled', pendingState: {}, harness: { ...r.harness, state: 'stopped', pending: undefined, activity: undefined } });
    for (const v of Object.values(window.__views)) {
      const l = (v.links || []).find((x) => x.childId === id);
      if (!l) continue;
      Object.assign(l, { state: 'canceled', outcome: 'canceled', child: { ...l.child, status: 'canceled' } });
      window.__push({ type: 'link', run: l.parentId, root: r.rootId || l.parentId, data: l });
    }
    return json({ ok: 'true' });
  };
  base.push(
    ['POST', /\/runs\/(\d+)\/cancel$/, (m) => (hrun(+m[1]) ? cancelRun(+m[1]) : json({}))], // a built-in run's: as before
    ['GET', /\/harnesses(?:\?probe=([^&]+))?$/, (m) => json({ harnesses: H.catalog.map((h) => {
      const out = { ...h, setting: H.modes[h.id] || 'approve' };
      const s = m[1] && box(decodeURIComponent(m[1]));
      if (s && s.state === 'running' && !(h.sandboxes || {})[s.ref]) {
        const has = (h.images || []).some((i) => i.provider === s.provider && i.image === ((s.image || {}).id || s.image));
        out.sandboxes = { ...(h.sandboxes || {}), [s.ref]: { installed: has, signedIn: false, at: Date.now() } };
      }
      return out;
    }) })],
    ['GET', /\/prefs\/harness-signins$/, () => json({ available: SI.available, ...(SI.available ? {} : { why: siWhy() }), signins: SI.available ? SI.list : [],
      harnesses: SI.harnesses, warnDays: 14 })],
    ['POST', /\/prefs\/harness-signins$/, (m, o) => {
      const b = JSON.parse(o.body || '{}');
      if (!SI.available) return json({ error: siWhy() }, 409);
      const hk = SI.harnesses[b.harness];
      if (!hk) return json({ error: `harness: "${b.harness || ''}" takes no saved sign-in` }, 400);
      const secret = String(b.secret || '').trim();
      if (!secret) return json({ error: 'secret: the key or token, one line' }, 400);
      const key = b.env ? hk.keys.find((k) => k.env === b.env) : hk.keys.find((k) => k.prefix && secret.startsWith(k.prefix)) || hk.keys.find((k) => !k.prefix);
      if (!key) return json({ error: b.env ? `env: one of ${oneOf(hk.keys.map((k) => k.env))}` : 'env: say which key this is' }, 400);
      const s = addSignin({ harness: b.harness, name: b.name, kind: key.kind, env: key.env });
      if (b.default) for (const x of SI.list) if (x.harness === s.harness) x.isDefault = x === s;
      return json({ signin: s }, 201);
    }],
    ['PUT', /\/prefs\/harness-signins\/([^/?]+)$/, (m, o) => {
      const s = SI.list.find((x) => x.id === decodeURIComponent(m[1]));
      if (!s) return json({ error: 'no such saved sign-in' }, 404);
      const b = JSON.parse(o.body || '{}');
      if (b.name != null) {
        const name = String(b.name).trim();
        if (!name) return json({ error: 'name: up to 40 characters, one line' }, 400);
        if (SI.list.some((x) => x !== s && x.harness === s.harness && x.name.toLowerCase() === name.toLowerCase())) return json({ error: `you have a saved sign-in named ${name} already` }, 409);
        s.name = name;
      }
      if (b.default != null) for (const x of SI.list) if (x.harness === s.harness) x.isDefault = b.default ? x === s : x.isDefault && x !== s;
      return json({ signin: s });
    }],
    ['DELETE', /\/prefs\/harness-signins\/([^/?]+)$/, (m) => {
      const i = SI.list.findIndex((x) => x.id === decodeURIComponent(m[1]));
      if (i < 0) return json({ error: 'no such saved sign-in' }, 404);
      SI.list.splice(i, 1);
      return json({ ok: 'true', stopped: 0 });
    }],
    ['PUT', /\/runs\/(\d+)\/harness\/signin$/, (m, o) => {
      const id = +m[1];
      const r = hrun(id);
      if (!r) return notHarness();
      if (!SI.available) return json({ error: siWhy() }, 409);
      const b = JSON.parse(o.body || '{}');
      const pick = b.signin === 'sandbox' ? 'sandbox' : !b.signin || b.signin === 'default' ? 'default' : b.signin;
      if (pick !== 'sandbox' && pick !== 'default' && !SI.list.some((x) => x.id === pick && x.harness === r.harness.provider)) {
        return json({ error: `signin: one of your saved sign-ins for ${r.harness.name}, "default" or "sandbox"` }, 400);
      }
      return json({ harness: setHarness(id, { signin: { ...(r.harness.signin || {}), pick } }) });
    }],
    ['GET', /\/prefs\/harness-mode$/, () => (person() ? json({ modes: H.modes }) : json({ error: "the setting is a person's own" }, 403))],
    ['PUT', /\/prefs\/harness-mode\/([^/?]+)$/, (m, o) => {
      const b = JSON.parse(o.body || '{}');
      const id = decodeURIComponent(m[1]);
      const h = hentry(id);
      if (!person()) return json({ error: "the setting is a person's own" }, 403);
      if (b.mode !== 'auto' && b.mode !== 'approve') return json({ error: 'mode is "auto" or "approve"' }, 400);
      if (!h) return json({ error: `no coding agent "${id}"` }, 400);
      if (b.mode === 'auto' && !h.autoMode) return json({ error: `${h.name} has no auto mode — it asks as its own settings say` }, 400);
      H.modes[id] = b.mode;
      return json({ provider: id, mode: b.mode });
    }],
    // without a harness these answer as before (a test routes /ask itself)
    ['POST', /\/ask$/, (m, o) => { const b = JSON.parse(o.body || '{}'); return b.harness ? startHarness(b, b.text) : json({}); }],
    ['POST', /\/runs$/, (m, o) => { const b = JSON.parse(o.body || '{}'); return b.harness ? startHarness(b, b.goal) : json({}); }],
    ['GET', /\/runs\/(\d+)\/harness$/, (m) => {
      const r = hrun(+m[1]);
      if (!r) return notHarness();
      const v = window.__views[+m[1]] || {};
      const dev = (H.devices || {})[+m[1]];
      const harness = dev && r.harness.login && r.harness.login.device ? { ...r.harness, login: { ...r.harness.login, device: dev } } : r.harness;
      return json({ harness, session: { gen: r.harness.gen || 0, execId: '', acpSessionId: '', loadable: false, steering: !!r.harness.steering, startedAt: 0, lastActive: 0,
        ...(v.harnessSession || {}) }, rules: v.harnessRules || [] });
    }],
    ['PATCH', /\/runs\/(\d+)\/harness$/, (m, o) => {
      const id = +m[1];
      const r = hrun(id);
      if (!r) return notHarness();
      const b = JSON.parse(o.body || '{}');
      const h = r.harness;
      const patch = {};
      if (b.mode != null) {
        const modes = (h.mode || {}).available || [];
        const md = modes.find((x) => x.id === b.mode);
        if (!md) return json({ error: `mode: one of ${oneOf(modes.map((x) => x.id))}` }, 400);
        if (md.explicit && (window.__views[id] || {}).access !== 'owner') return json({ error: `only the owner can switch ${h.name} to ${md.name}` }, 403);
        patch.mode = { ...h.mode, current: b.mode };
      }
      if (b.option) {
        const opt = (h.options || []).find((x) => x.id === b.option.id);
        if (!opt) return json({ error: `option: one of ${oneOf((h.options || []).map((x) => x.id))}` }, 400);
        if (!(opt.options || []).some((x) => x.value === b.option.value)) return json({ error: `value: one of ${oneOf((opt.options || []).map((x) => x.value))}` }, 400);
        patch.options = h.options.map((x) => (x.id === opt.id ? { ...x, currentValue: b.option.value } : x));
      }
      if (!('mode' in patch) && !('options' in patch)) return json({ error: 'mode or option: name one' }, 400);
      return json({ harness: setHarness(id, patch) });
    }],
    ['POST', /\/runs\/(\d+)\/harness\/answer$/, (m, o) => {
      const id = +m[1];
      const r = hrun(id);
      if (!r) return notHarness();
      const b = JSON.parse(o.body || '{}');
      const ps = r.pendingState || {};
      if (r.status !== 'waiting_input' || ps.kind !== 'question') return json({ error: 'no pending question' }, 400);
      if (b.park && b.park !== ps.park) return json({ error: 'that question is no longer pending — the agent is asking something else now' }, 409);
      if (!['accept', 'decline', 'cancel'].includes(b.action)) return json({ error: 'action is accept, decline or cancel' }, 400);
      if (b.action === 'accept' && (!b.content || typeof b.content !== 'object' || Array.isArray(b.content))) return json({ error: "content: an object with the form's fields" }, 400);
      unpark(id);
      return json({ ok: 'true' });
    }],
    ['POST', /\/runs\/(\d+)\/harness\/authenticate$/, (m, o) => {
      const id = +m[1];
      const r = hrun(id);
      if (!r) return notHarness();
      const b = JSON.parse(o.body || '{}');
      const h = r.harness;
      if (h.state !== 'login') return json({ error: `${h.name} is signed in` }, 409); // its methods went with its login
      if (b.method === 'guided') return guidedSignin(id, r, b);
      const methods = ((h.login || {}).methods || []).filter((x) => x.kind === 'api-key' || x.kind === 'device-code');
      const md = methods.find((x) => x.id === b.method);
      if (!md) return json({ error: `method: one of ${oneOf(methods.map((x) => x.id))}` }, 400);
      if (md.kind === 'api-key' && !b.apiKey) return json({ error: `apiKey: needed for ${md.name}` }, 400);
      if (md.kind !== 'api-key' && b.apiKey) return json({ error: 'apiKey: only for an API-key method' }, 400);
      if ((h.sandbox || {}).shared && !b.confirm) {
        return json({ error: `anyone who may use ${(h.sandbox || {}).name} acts as you with ${h.name} there — confirm to sign in`, confirm: true }, 409);
      }
      if (md.kind === 'device-code') {
        // the code is the requester's: the answer and their GET …/harness;
        // the summary says only who started it
        const device = { url: 'https://example.invalid/device', message: 'Enter code FAKE-1234 at https://example.invalid/device' };
        const by = (seed.me || {}).user || 'admin';
        (H.devices = H.devices || {})[id] = { ...device, by };
        setHarness(id, { login: { ...h.login, device: { by } } });
        return json({ ok: 'true', device }, 202);
      }
      if (b.apiKey === 'bad') return json({ error: 'invalid API key' }, 502);
      const next = { ...h, state: 'ready' };
      delete next.login; delete next.pending;
      setRun(id, { status: 'running', pendingState: {}, harness: next });
      return json({ ok: 'true', state: 'ready' });
    }],
    ['GET', /\/runs\/(\d+)\/harness\/log(?:\?max=(\d+))?$/, (m) => {
      if (!hrun(+m[1])) return notHarness();
      const log = H.logs[m[1]];
      if (log == null) return json({ error: 'no log yet' }, 404);
      const max = Math.min(65536, Number(m[2]) || 65536);
      return new Response(log.slice(-max), { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
    }],
  );
  window.xbin = {
    self: 'apps/agent',
    fetch: async (url, opt = {}) => {
      const method = opt.method || 'GET';
      window.__calls.push({ method, url, body: typeof opt.body === 'string' ? opt.body : undefined, home: opt.partition || '' });
      for (const r of routes) {
        const m = r.method === method && url.match(r.re);
        const res = m ? r.fn(m, opt) : null;
        if (res != null) return res; // a test's route that answers nothing leaves it to the stub
      }
      for (const [meth, re, fn] of base) {
        const m = meth === method && url.match(re);
        if (m) return fn(m, opt);
      }
      return json({});
    },
    bus: { on: () => () => {} },
    iface: () => null,
    download: () => {},
  };
}

// launch starts a browser, or exits 0 (SKIP) without playwright.
export async function launch() {
  let chromium;
  try {
    ({ chromium } = await import('/usr/local/node/lib/node_modules/playwright/index.mjs'));
  } catch {
    try { ({ chromium } = await import('playwright')); } catch {
      console.log('SKIP: playwright not installed');
      process.exit(0);
    }
  }
  return chromium.launch();
}

export function checker() {
  let failures = 0;
  return {
    ok: (name, cond, extra = '') => {
      if (!cond) { console.log(`FAIL  ${name}  ← ${extra}`); failures++; }
      return cond;
    },
    done: (what) => {
      console.log(failures ? `\n${failures} FAILURE(S)` : `all ${what} checks passed`);
      process.exit(failures ? 1 : 0);
    },
  };
}
