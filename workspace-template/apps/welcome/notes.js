/**
 * <welcome-notes> — a clickable sticky-note board of builder docs.
 *
 * Three levels: the board (all notes) → a topic (intro + child notes) →
 * a detail page (prose + code). Content is data-driven in NOTES below;
 * everything here is distilled from AGENTS.md and /docs/ — if you change
 * behavior in the workspace, keep these notes honest too.
 */
import { LitElement, html, css, nothing } from 'lit';

// pastel paper colors per topic: [background, edge]
const PAPER = {
  blue:   ['#e3f0fd', '#b5d4f5'],
  purple: ['#f1e6fb', '#d8bef0'],
  green:  ['#e5f4e8', '#b8dfc2'],
  pink:   ['#fde7ef', '#f4bcd2'],
  yellow: ['#fdf6cf', '#ecd98a'],
  orange: ['#fdecd8', '#f2cba0'],
  cyan:   ['#e0f4f6', '#aadde2'],
};

const NOTES = [
  {
    id: 'glue', title: 'api glue', color: 'blue',
    teaser: 'how components call each other — one switchboard, no direct wires',
    intro: html`
      <p>Everything is HTTP through buxond. A component at
      <code>apps/thing</code> serves its view at <code>/c/apps/thing/</code>
      and its backend at <code>/api/apps/thing/&hellip;</code>. Nothing talks
      to anything directly: every call goes through the daemon, which
      verifies <em>who</em> is calling, checks the grant, and injects
      verified identity headers before routing.</p>
      <p>That one hop is what makes the rest work — calls are attributed,
      permissions are enforced in one place, and integrating with an app is
      always "read its <code>API.md</code>, request a role, make HTTP
      calls".</p>`,
    docs: 'elements.md',
    children: [
      {
        id: 'frontend', title: 'frontend → api', color: 'blue',
        teaser: 'buxon.fetch, and why raw fetch 403s',
        body: html`
          <p>Component pages get a <code>buxon</code> global injected. Use
          <code>buxon.fetch</code> for <em>every</em> <code>/api/</code>
          call — it attaches your frame token, so buxond knows which
          component is calling:</p>
          <pre>await buxon.fetch(\`/api/\${buxon.self}/items\`)   // your own backend
await buxon.fetch('/api/apps/calendar/events')  // another app — needs a grant</pre>
          <p>A raw <code>fetch()</code> to another element returns
          <strong>403 by design</strong>: without the frame token the call
          can't be attributed, and unattributed calls are denied. This is a
          feature, not a bug to work around.</p>
          <p>Live updates come over the bus, not polling:</p>
          <pre>buxon.bus.on('res:apps/thing/bus/', (topic, data) => { /* re-render */ })</pre>`,
      },
      {
        id: 'backend', title: 'backend → backend', color: 'blue',
        teaser: 'outbound calls via the gateway socket',
        body: html`
          <p>Backends call other components through the gateway. In Go the
          SDK client is pre-wired:</p>
          <pre>resp, err := buxon.Client().Get("http://buxon/api/apps/calendar/events")</pre>
          <p>node / python need no SDK: send the request over the
          <code>BUXON_GATEWAY</code> unix socket with
          <code>Authorization: Bearer $BUXON_TOKEN</code>. That token is
          <em>this generation's</em> credential — it identifies your
          component, is rotated on every rebuild, and only carries the roles
          you were actually granted.</p>
          <p>The callee never sees the token; it sees the verified
          <code>X-Buxon-From</code> / <code>X-Buxon-Role</code> headers
          buxond injects after checking the grant.</p>`,
      },
      {
        id: 'discover', title: 'discovering an api', color: 'blue',
        teaser: 'API.md is the contract; bx api reads it for you',
        body: html`
          <p>Every component that exposes roles ships an
          <code>API.md</code>: endpoints per role, plus a copy-paste
          <code>uses</code> snippet. <code>bx doctor</code> flags exposing
          components that don't have one.</p>
          <pre>bx api apps/calendar    # roles + API.md — everything needed to integrate
bx ls                   # what exists in this workspace</pre>
          <p>Integration is always the same three moves: read the
          <code>API.md</code>, add the <code>uses</code> entry to your
          manifest, get the grant (automatic same-scope, owner-approved
          across scopes — see <em>roles &amp; grants</em>).</p>`,
      },
    ],
  },
  {
    id: 'auth', title: 'auth & identity', color: 'purple',
    teaser: 'your path is your identity; buxond vouches for every call',
    intro: html`
      <p>A component's <strong>path is its identity</strong> —
      <code>apps/email</code> <em>is</em> the principal. There are no
      accounts to create and no passwords between components: buxond
      authenticates every request itself and tells the callee who called,
      via headers only it can set.</p>
      <p>The core rule: <strong>never verify auth yourself</strong>. Strip
      nothing, check nothing, sign nothing — trust exactly the verified
      headers you receive, and nothing else.</p>`,
    docs: 'auth.md',
    children: [
      {
        id: 'principals', title: 'the principals', color: 'purple',
        teaser: 'owner is root; elements are least-privileged tenants',
        body: html`
          <p>Three kinds of caller, very different trust:</p>
          <ul>
            <li><strong>Owner (you)</strong> — login cookie or
              <code>Bearer $BUXON_TOKEN</code>. Passes <em>every</em>
              permission check as role <code>admin</code>. Terminals run as
              owner, which is why your <code>curl</code> always works.</li>
            <li><strong>Element frontend</strong> — owner cookie
              <em>plus</em> a frame token that <code>buxon.fetch</code>
              attaches, attributing the call to that component.</li>
            <li><strong>Element backend</strong> — its per-generation
              gateway token. <strong>Default-deny</strong>: no grant, no
              call.</li>
          </ul>
          <p>So the human is root and running code is a tenant — an app you
          forked from the internet can't read your email unless you granted
          it that role.</p>`,
      },
      {
        id: 'headers', title: 'verified headers', color: 'purple',
        teaser: 'X-Buxon-From / X-Buxon-Role — the only truth',
        body: html`
          <p>buxond <strong>strips</strong> any inbound
          <code>X-Buxon-*</code> headers, then injects verified values after
          checking the grant. By the time a request reaches your backend,
          <code>X-Buxon-From</code> and <code>X-Buxon-Role</code> are
          trustworthy — a caller cannot forge them.</p>
          <pre>c := buxon.Caller(r)     // Go SDK: verified {From, Role, Owner}</pre>
          <p>node / python read the headers directly; cgi gets
          <code>BUXON_FROM</code> / <code>BUXON_ROLE</code> in env.</p>
          <p>Corollaries: never trust a role from a request body, query
          param, or custom header — and never build your own token check on
          top. If you received the request at all, buxond already
          authenticated it.</p>`,
      },
    ],
  },
  {
    id: 'rbac', title: 'roles & grants', color: 'green',
    teaser: 'callee declares roles, caller requests, owner approves',
    intro: html`
      <p>Permissioning is a three-way handshake. The <strong>callee
      declares</strong> what roles exist on it (<code>expose.roles</code>,
      each with a human description). The <strong>caller requests</strong>
      the roles it wants (<code>uses</code> in its manifest). The
      <strong>owner approves</strong> anything that crosses a scope
      boundary — inside one scope (one app), grants are automatic.</p>
      <p>Both halves are declarative and live in manifests, so access is
      reviewable in git: what an app <em>can</em> touch is written down,
      not implied by network reachability.</p>`,
    docs: 'auth.md',
    children: [
      {
        id: 'declare', title: 'declaring roles', color: 'green',
        teaser: 'expose.roles in the manifest, RoleFunc in code',
        body: html`
          <p>In the callee's <code>buxon.json</code>:</p>
          <pre>"expose": {
  "roles": {
    "reader": "Read thing data",          // description is required
    "writer": "Modify thing data"
  },
  "implies": { "auditor": ["reader"] }    // only for custom role names
}</pre>
          <p>Then guard handlers by role — the check is one wrapper, because
          identity was already verified upstream:</p>
          <pre>mux.HandleFunc("GET /items", list)                        // any granted role
mux.Handle("POST /items", buxon.RoleFunc("writer", add))  // writer and up</pre>
          <p>Exposing roles means shipping an <code>API.md</code> documenting
          what each role can do — it's the contract callers integrate
          against.</p>`,
      },
      {
        id: 'grant', title: 'requesting & granting', color: 'green',
        teaser: 'uses → pending → bx grant',
        body: html`
          <p>The caller asks in its own manifest:</p>
          <pre>"uses": [
  { "target": "apps/calendar",     "role": "reader" },
  { "target": "res:apps/me/db",    "role": "writer" }
]</pre>
          <p>Same scope → granted automatically. Cross-scope → the request
          sits <em>pending</em> until you approve it (role after the last
          colon):</p>
          <pre>bx grant apps/email apps/calendar:reader
bx grant --revoke apps/email apps/calendar:reader
bx grants                                # list everything, incl. pending</pre>
          <p>The grants panel on the root page does the same with clicks.
          Don't hand-edit the <code>grants</code> array in the workspace
          <code>buxon.json</code> — it's machine-rewritten.</p>`,
      },
      {
        id: 'convention', title: 'role conventions', color: 'green',
        teaser: 'reader ⊂ writer ⊂ admin, implication downward',
        body: html`
          <p>Stick to <code>reader</code> / <code>writer</code> /
          <code>admin</code> unless you have a real reason not to. They
          imply each other downward — <code>admin ⊃ writer ⊃ reader</code> —
          so a handler guarded with <code>RoleFunc("reader", …)</code>
          accepts writers and admins too.</p>
          <p>Custom role names are exact-match unless the callee's manifest
          declares <code>implies</code>. On <code>bus</code> resources,
          <code>subscriber</code> / <code>publisher</code> are aliases for
          reader / writer.</p>
          <p>And remember the escape hatch that isn't one: the owner passes
          every check as <code>admin</code>. That's why your terminal never
          needs a grant — and why code should never run as owner.</p>`,
      },
    ],
  },
  {
    id: 'vault', title: 'vault', color: 'pink',
    teaser: 'per-element secrets — never in source, manifests, or env',
    intro: html`
      <p>Each element has a private key-value vault for third-party
      credentials — IMAP passwords, API keys. Secrets stop living in source
      files, manifests, or <code>.env</code>: code fetches them at runtime,
      and only its <em>own</em>.</p>`,
    docs: 'auth.md',
    children: [
      {
        id: 'use', title: 'using it', color: 'pink',
        teaser: 'bx vault set + buxon.Secret',
        body: html`
          <p>You (the owner) write secrets in; the value is read from stdin
          so it never lands in shell history:</p>
          <pre>bx vault set apps/email imap-pass     # then type/paste the value
bx vault ls apps/email
bx vault get apps/email imap-pass
bx vault rm  apps/email imap-pass</pre>
          <p>The element reads its own vault at runtime:</p>
          <pre>pass, err := buxon.Secret("imap-pass")   // Go; own vault only</pre>
          <p>node / python fetch the same thing over the gateway
          (<code>/api/buxon/&hellip;</code> — see docs/protocol.md).</p>`,
      },
      {
        id: 'bounds', title: 'boundaries', color: 'pink',
        teaser: 'no cross-element access — share via an API instead',
        body: html`
          <p><strong>There is no cross-element vault access.</strong> If two
          apps need the same credential, don't copy it into both vaults —
          put it in one element and expose a role-guarded API in front of
          it. The secret stays in one place; access is a revocable
          grant.</p>
          <p>Secrets are fetched at runtime, never injected into env — env
          leaks via <code>/proc</code>, error reports, and logs. Storage is
          <code>data/vault/</code>, buxond-owned, mode 0600, always
          gitignored, and excluded from <code>bx backup</code> unless you
          pass <code>--with-vault</code>.</p>`,
      },
    ],
  },
  {
    id: 'storage', title: 'data storage', color: 'yellow',
    teaser: 'kv · sqlite · blob — declared per scope, granted like APIs',
    intro: html`
      <p>Backends must not keep state in RAM: <strong>every save is a new
      process</strong>, and idle backends are reaped. Anything you care
      about lives in a <em>resource</em> — declared in your scope, granted
      through <code>uses</code>, addressed as
      <code>res:&lt;scope&gt;/&lt;name&gt;</code>.</p>
      <pre>// apps/thing/scope.json
{ "resources": { "store": {"type":"filesystem"},
                 "kvx":   {"type":"kv"},
                 "files": {"type":"blob"} } }</pre>
      <p>Each granted resource shows up in the backend's env as
      <code>BUXON_RES_&lt;NAME&gt;</code>. Resources belong to the scope —
      they survive rebuilds, renames of code, and crashes.</p>`,
    docs: 'resources.md',
    children: [
      {
        id: 'kv', title: 'kv', color: 'yellow',
        teaser: 'namespaced key-value, ≤1 MiB per value',
        body: html`
          <p>The default choice for config, small documents, and anything
          JSON-shaped. Values up to 1 MiB.</p>
          <pre>kv := buxon.KV(buxon.Resource("kvx"))
kv.PutJSON("settings", s)
var s Settings; kv.GetJSON("settings", &s)
keys, _ := kv.List("item/")</pre>
          <p>No SDK? It's plain HTTP through the gateway:</p>
          <pre>GET/PUT/DELETE /api/buxon/kv/res:apps/thing/kvx/&lt;key&gt;</pre>`,
      },
      {
        id: 'filesystem', title: 'filesystem', color: 'yellow',
        teaser: 'a rw directory — same-scope only',
        body: html`
          <p><code>BUXON_RES_STORE</code> is a <strong>directory</strong> path
          your backend can write — a db, files, a cache, anything. It's bound
          read-write and backed up. Write only inside it; anywhere else is a
          throwaway overlay.</p>
          <pre>dir := buxon.Resource("store")
db, _ := sql.Open("sqlite", dir+"/app.db?_journal_mode=WAL")</pre>
          <p>(<code>type:"sqlite"</code> is a convenience — the same rw dir with
          <code>BUXON_RES_&lt;N&gt;</code> pre-pointed at a <code>.sqlite</code>
          file.) Same-scope only, deliberately: direct file access across trust
          boundaries corrupts data and blurs permissions. If another app needs
          your data, expose a role-guarded API — "email reads calendar" is a
          reader grant on calendar's API, never a shared file.</p>`,
      },
      {
        id: 'blob', title: 'blob', color: 'yellow',
        teaser: 'file store for attachments and uploads, ≤256 MiB/write',
        body: html`
          <p>A path-addressed file store for things too big or too binary
          for kv — attachments, images, exports.</p>
          <pre>GET/PUT/DELETE /api/buxon/blob/res:apps/thing/files/&lt;path&gt;</pre>
          <p>Writes up to 256 MiB. Frontends can fetch blob URLs with
          <code>buxon.fetch</code> like any other API path. Don't reach into
          <code>data/</code> on disk — the broker's layout is not API; the
          HTTP surface is.</p>`,
      },
    ],
  },
  {
    id: 'events', title: 'events & time', color: 'orange',
    teaser: 'bus for "something changed", cron for "do this later"',
    intro: html`
      <p>Two resources cover async: a <strong>bus</strong> for live
      change-notifications, and <strong>cron</strong> for scheduled work.
      Between them there is no reason for a backend to sit in a loop — and
      it couldn't anyway, since idle backends get reaped.</p>`,
    docs: 'resources.md',
    children: [
      {
        id: 'bus', title: 'bus', color: 'orange',
        teaser: 'at-most-once notifications — not a queue',
        body: html`
          <p>Publish from anywhere, subscribe in frontends:</p>
          <pre>// backend (Go)
buxon.Publish(buxon.Resource("bus"), "changed", payload)

// frontend
buxon.bus.on('res:apps/thing/bus/', (topic, data) => refresh())</pre>
          <p>Delivery is <strong>at-most-once</strong>: offline subscribers
          miss messages, and that's fine — the bus says <em>"something
          changed, go look"</em>, while truth lives in kv/sqlite. If you
          need every message processed, it's not a bus problem: persist work
          items and sweep them with cron.</p>
          <p>Backends don't subscribe (they'd be reaped mid-listen) —
          frontends subscribe, backends sweep.</p>`,
      },
      {
        id: 'cron', title: 'cron', color: 'orange',
        teaser: 'scheduled POSTs to your own endpoints',
        body: html`
          <p>Cron delivers scheduled POSTs to your own API — it wakes an
          idle backend, so periodic work costs nothing between runs:</p>
          <pre>PUT /api/buxon/cron/jobs
{ "name": "sweep", "resource": "res:apps/thing/cron",
  "schedule": "*/5 * * * *", "path": "/sweep", "role": "admin" }</pre>
          <p>Make handlers <strong>idempotent</strong> — a tick can arrive
          twice or land during a blue/green swap. <code>bx cron ls</code>
          shows what's scheduled. Never substitute a sleeping loop: it dies
          with the process, silently.</p>`,
      },
    ],
  },
  {
    id: 'lifecycle', title: 'backend lifecycle', color: 'cyan',
    teaser: 'a save is a new process; design for the swap',
    intro: html`
      <p>Backends are cattle with a very short memory. They start lazily on
      the first request, get <strong>reaped after ~30 min idle</strong>
      (the next request revives them in ~200 ms), and every save replaces
      the process entirely. State you care about goes in resources —
      <em>data storage</em> is the other half of this note.</p>`,
    docs: 'elements.md',
    children: [
      {
        id: 'swap', title: 'the swap', color: 'cyan',
        teaser: 'blue/green on every save, 30 s drain',
        body: html`
          <p>On save, buxond builds the new generation and swaps traffic to
          it. In-flight requests on the old process finish; long-lived
          WebSocket / SSE connections die at the <strong>30-second
          drain</strong> — so frontends must reconnect (the injected
          <code>buxon.events</code> / <code>buxon.bus</code> clients already
          do).</p>
          <p>Handle SIGTERM for a clean drain — <code>buxon.Serve</code> and
          the <code>bx new</code> skeletons do it for you. Each generation
          gets a fresh gateway token, so nothing from the old process stays
          usable.</p>`,
      },
      {
        id: 'broken', title: 'when it breaks', color: 'cyan',
        teaser: 'bx logs first, always',
        body: html`
          <p>A backend that crashes <strong>3× fast</strong> is marked
          <code>failed</code> and stays down until you save a change — no
          crash-loop burning CPU. Build errors overlay the component's frame
          with compiler output until the next good save.</p>
          <pre>bx status              # building | healthy | failed (+ error)
bx logs -f apps/thing  # stdout/stderr, per generation
bx doctor              # manifest errors, missing API.md, dangling deps</pre>
          <p>Saves are picked up within ~300–500 ms; Go swaps in about a
          second. After structural changes (new scope.json, renames), give
          the scanner a beat, then <code>bx doctor</code>.</p>`,
      },
    ],
  },
];

class WelcomeNotes extends LitElement {
  static properties = {
    _path: { state: true }, // [] board · [topic] · [topic, child]
  };

  constructor() {
    super();
    // deep-linkable: /c/apps/welcome/#rbac or #storage/kv
    const [topId, childId] = location.hash.replace(/^#/, '').split('/');
    const top = NOTES.find((n) => n.id === topId);
    const child = top?.children?.find((c) => c.id === childId);
    this._path = child ? [top.id, child.id] : top ? [top.id] : [];
  }

  static styles = css`
    :host { display: block; }
    p { margin: 6px 0 0; font-size: 12.5px; color: var(--bx-text, #33414e); }
    ul { margin: 6px 0 0; padding-left: 18px; }
    li { font-size: 12.5px; margin-top: 4px; }
    code {
      background: rgba(255, 255, 255, 0.55);
      border: 1px solid rgba(51, 65, 78, 0.12);
      border-radius: 4px; padding: 0 4px;
      font: 11.5px var(--bx-mono, ui-monospace, monospace);
    }
    pre {
      background: rgba(255, 255, 255, 0.6);
      border: 1px solid rgba(51, 65, 78, 0.12);
      border-radius: 4px; padding: 7px 9px; margin: 8px 0 0;
      font: 11.5px/1.5 var(--bx-mono, ui-monospace, monospace);
      overflow-x: auto; white-space: pre;
    }
    a { color: var(--bx-accent, #1e88e5); text-decoration: none; }

    /* ---- breadcrumbs ---- */
    .crumbs {
      display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
      margin: 2px 0 10px;
      font-size: 11px; color: var(--bx-muted, #8794a1);
    }
    .crumbs button {
      background: none; border: none; padding: 0; cursor: pointer;
      font: inherit; color: var(--bx-accent, #1e88e5);
    }
    .crumbs button:hover { text-decoration: underline; }
    .crumbs .here { color: var(--bx-text, #33414e); font-weight: 600; }

    /* ---- sticky notes ---- */
    .board {
      display: grid; gap: 14px;
      grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
      padding: 4px 2px 8px;
    }
    .note {
      position: relative;
      background: var(--paper);
      border: 1px solid var(--edge);
      border-radius: 2px;
      padding: 22px 12px 12px;
      min-height: 74px;
      box-shadow: 1px 2px 5px rgba(16, 24, 40, 0.13);
      cursor: pointer;
      transform: rotate(-1deg);
      transition: transform 0.12s ease, box-shadow 0.12s ease;
      text-align: left; font: inherit; color: var(--bx-text, #33414e);
    }
    .board .note:nth-child(even) { transform: rotate(1.1deg); }
    .board .note:nth-child(3n)   { transform: rotate(-0.4deg); }
    .note:hover {
      transform: rotate(0deg) translateY(-2px);
      box-shadow: 2px 5px 12px rgba(16, 24, 40, 0.18);
    }
    .note::before {              /* tape */
      content: ""; position: absolute; top: -7px; left: 50%;
      width: 42px; height: 14px; transform: translateX(-50%) rotate(-2deg);
      background: rgba(255, 255, 255, 0.55);
      border: 1px solid rgba(51, 65, 78, 0.1);
      box-shadow: 0 1px 1px rgba(16, 24, 40, 0.06);
    }
    .note h3 {
      margin: 0; font-size: 12.5px; font-weight: 700;
      color: var(--bx-text, #33414e);
    }
    .note .teaser {
      margin: 4px 0 0; font-size: 11.5px; font-style: italic;
      color: color-mix(in srgb, var(--bx-text, #33414e) 72%, transparent);
    }
    .board.sub { grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); }

    /* ---- opened topic / detail: one big sheet of the same paper ---- */
    .sheet {
      position: relative;
      background: var(--paper);
      border: 1px solid var(--edge);
      border-radius: 2px;
      padding: 24px 16px 16px;
      box-shadow: 1px 3px 8px rgba(16, 24, 40, 0.14);
    }
    .sheet::before {
      content: ""; position: absolute; top: -8px; left: 26px;
      width: 52px; height: 16px; transform: rotate(-3deg);
      background: rgba(255, 255, 255, 0.55);
      border: 1px solid rgba(51, 65, 78, 0.1);
    }
    .sheet h3 { margin: 0; font-size: 13px; font-weight: 700; }
    .sheet .kids-label {
      display: block; margin: 16px 0 2px;
      font-size: 10.5px; font-weight: 600; letter-spacing: 0.08em;
      text-transform: uppercase;
      color: color-mix(in srgb, var(--bx-text, #33414e) 55%, transparent);
    }
    .sheet .docs-link { margin-top: 12px; font-size: 11px; }
  `;

  _go(path) {
    this._path = path;
    history.replaceState(null, '', path.length ? `#${path.join('/')}`
                                               : location.pathname);
    this.updateComplete.then(() =>
      this.scrollIntoView({ block: 'nearest', behavior: 'smooth' }));
  }

  _paperVars(color) {
    const [paper, edge] = PAPER[color] ?? PAPER.yellow;
    return `--paper:${paper};--edge:${edge}`;
  }

  _crumbs(top, child) {
    if (!top) return nothing;
    return html`
      <nav class="crumbs">
        <button @click=${() => this._go([])}>all notes</button>
        <span>›</span>
        ${child
          ? html`<button @click=${() => this._go([top.id])}>${top.title}</button>
                 <span>›</span><span class="here">${child.title}</span>`
          : html`<span class="here">${top.title}</span>`}
      </nav>`;
  }

  _sticky(note, path) {
    return html`
      <button class="note" style=${this._paperVars(note.color)}
              @click=${() => this._go(path)}>
        <h3>${note.title}</h3>
        <p class="teaser">${note.teaser}</p>
      </button>`;
  }

  _board() {
    return html`<div class="board">
      ${NOTES.map((n) => this._sticky(n, [n.id]))}
    </div>`;
  }

  _topic(top) {
    return html`
      <article class="sheet" style=${this._paperVars(top.color)}>
        <h3>${top.title}</h3>
        ${top.intro}
        ${top.children?.length
          ? html`<span class="kids-label">pull a thread</span>
                 <div class="board sub">
                   ${top.children.map((c) => this._sticky(c, [top.id, c.id]))}
                 </div>`
          : nothing}
        ${top.docs
          ? html`<p class="docs-link">full story:
              <a href="/docs/${top.docs}" target="_blank">docs/${top.docs}</a></p>`
          : nothing}
      </article>`;
  }

  _detail(top, child) {
    return html`
      <article class="sheet" style=${this._paperVars(child.color)}>
        <h3>${top.title} › ${child.title}</h3>
        ${child.body}
        ${top.docs
          ? html`<p class="docs-link">full story:
              <a href="/docs/${top.docs}" target="_blank">docs/${top.docs}</a></p>`
          : nothing}
      </article>`;
  }

  render() {
    const [topId, childId] = this._path;
    const top = NOTES.find((n) => n.id === topId);
    const child = top?.children?.find((c) => c.id === childId);
    return html`
      ${this._crumbs(top, child)}
      ${!top ? this._board() : child ? this._detail(top, child) : this._topic(top)}
    `;
  }
}

customElements.define('welcome-notes', WelcomeNotes);
