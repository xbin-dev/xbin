// <xb-shell-scene> — the hero's right column: a faithful mini xbin shell that
// boots itself, over and over. The dashboard tile opens its own terminal, an
// agent adds a p95 latency card, and the dashboard updates live.
import { LitElement, html, css, nothing, unsafeHTML } from 'lit';

const SCENE = {
  path: 'apps/dashboard',
  dash: 'Service dashboard',
  side: 'dashboard',
  result: { v: '142ms', l: 'p95 latency', c: 'amber' },
  script: [
    '<span class="a">you@dashboard</span> $ claude "add a p95 latency card"',
    '<span class="m">● read apps/dashboard/index.html</span>',
    '<span class="m">● edit index.html</span>  <span class="b">+18 −2</span>',
    '<span class="m">● bx build apps/dashboard</span>  <span class="g">✓</span>',
    '<span class="g">✓ p95 latency card is live</span>',
  ],
};

const SPARKS = {
  amber: '0,11 15,9 30,10 45,6 60,7 75,5 90,4',
  green: '0,6 15,7 30,5 45,8 60,6 75,7 90,6',
  blue:  '0,12 15,10 30,11 45,7 60,8 75,4 90,5',
};

class XbShellScene extends LitElement {
  static properties = {
    _hot:  { state: true },
    _open: { state: true },
    _show: { state: true },
    _out:  { state: true },
  };

  static styles = css`
    :host { display: block; min-width: 0; }
    * { box-sizing: border-box; }
    .mock {
      --cut: 18px;
      display: flex; flex-direction: column; min-height: 0; position: relative;
      background: #1b1e24;
      clip-path: polygon(0 0, calc(100% - var(--cut)) 0, 100% var(--cut),
        100% 100%, var(--cut) 100%, 0 calc(100% - var(--cut)));
      box-shadow: 0 24px 70px rgba(0,0,0,.55);
      outline: 1px solid #454d59; outline-offset: -1px;
      font: 12px/1.5 -apple-system, "Segoe UI", Inter, system-ui, sans-serif;
      color: #d4d9e0;
    }
    .mono { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
    .dt { width: 8px; height: 8px; border-radius: 50%; flex: none; display: inline-block; }
    .dt.sm { width: 7px; height: 7px; }
    .dg { background: #4caf50; } .da { background: #f5a623; } .dam { background: #f2a71b; }

    .m-top { display: flex; align-items: center; gap: 8px; background: #23272e;
      border-bottom: 1px solid #363c45; padding: 8px 11px; }
    .brand { display: flex; align-items: center; gap: 7px; font-weight: 700;
      letter-spacing: .05em; font-size: 12px; font-family: "IBM Plex Sans", -apple-system, sans-serif; }
    .m-chip { font: 11px ui-monospace, Menlo, Consolas, monospace; color: #868f9a;
      background: #2b3038; border: 1px solid #363c45; padding: 1px 9px;
      clip-path: polygon(5px 0, 100% 0, calc(100% - 5px) 100%, 0 100%); }
    .m-sp { flex: 1; }
    .m-tabs { display: flex; gap: 2px; background: #2b3038; border-bottom: 1px solid #363c45; padding: 5px 9px 0; }
    .m-tab { font: 12px ui-monospace, Menlo, Consolas, monospace; color: #868f9a;
      padding: 6px 11px; display: flex; gap: 6px; align-items: center; }
    .m-tab.on { background: #1b1e24; color: #d4d9e0; border: 1px solid #363c45; border-bottom: none; margin-bottom: -1px; }

    .m-body { display: flex; min-height: 340px; }
    .m-side { width: 150px; flex: none; border-right: 1px solid #363c45; padding: 6px 0; background: #20232a; }
    .m-grp { font: 9.5px ui-monospace, Menlo, Consolas, monospace; letter-spacing: .1em;
      text-transform: uppercase; color: #5c6672; padding: 8px 11px 3px; }
    .m-item { display: flex; align-items: center; gap: 6px; padding: 4px 11px;
      font: 12px ui-monospace, Menlo, Consolas, monospace; color: #868f9a; }
    .m-item.on { background: rgba(245,166,35,.18); color: #fff; box-shadow: inset 2px 0 0 #f5a623; }
    .m-item .st { margin-left: auto; }

    .m-main { flex: 1; min-width: 0; padding: 9px; display: flex; flex-direction: column; gap: 9px;
      background: radial-gradient(#363c45 1px, transparent 1.4px); background-size: 22px 22px; background-position: 11px 11px; }
    .m-card { border: 1px solid #363c45; overflow: hidden; background: #23272e; flex: none; min-width: 0; }
    .m-card .h { display: flex; align-items: center; gap: 7px; padding: 5px 9px;
      border-bottom: 1px solid #363c45; background: #2b3038;
      font: 10.5px ui-monospace, Menlo, Consolas, monospace; color: #868f9a; }
    .m-card .h .path { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
    .tbtn { margin-left: auto; flex: none; font: 11px ui-monospace, Menlo, Consolas, monospace; color: #868f9a;
      border: 1px solid #363c45; padding: 2px 8px;
      clip-path: polygon(0 0, calc(100% - 6px) 0, 100% 6px, 100% 100%, 0 100%);
      transition: background .2s, color .2s; }
    .tbtn.hot { color: #231a06; background: #f5a623; }

    .m-card .body { display: flex; flex-direction: column; height: 300px; }
    .dash { flex: 1; min-height: 0; overflow: hidden; padding: 12px 13px; display: flex; flex-direction: column; gap: 11px; }
    .dash-h { display: flex; align-items: center; gap: 8px; font-weight: 600; font-size: 12px; }
    .dash-h .sp { flex: 1; }
    .dash-h .live { font: 10px ui-monospace, Menlo, Consolas, monospace; color: #4caf50;
      border: 1px solid rgba(76,175,80,.45); padding: 1px 7px; letter-spacing: .05em;
      clip-path: polygon(4px 0, 100% 0, calc(100% - 4px) 100%, 0 100%); }
    .stats { display: flex; gap: 9px; }
    .stat { flex: 1 1 0; min-width: 0; background: #2b3038; border: 1px solid #363c45;
      padding: 9px 10px; display: flex; flex-direction: column; gap: 2px; }
    .stat b { font-weight: 700; font-size: 16px; letter-spacing: -.02em; font-variant-numeric: tabular-nums; }
    .stat span { font: 10px ui-monospace, Menlo, Consolas, monospace; color: #868f9a;
      text-transform: uppercase; letter-spacing: .05em; }
    .stat.added { max-width: 0; opacity: 0; overflow: hidden; padding: 9px 0; border-width: 0;
      margin-left: -9px; white-space: nowrap;
      transition: max-width .55s ease, opacity .45s ease, padding .45s, border-width .3s, margin .5s; }
    .stat.added.show { max-width: 38%; opacity: 1; padding: 9px 10px; border-width: 1px; margin-left: 0; }
    .stat.added.show.c-amber { border-color: rgba(245,166,35,.55); box-shadow: 0 0 0 1px rgba(245,166,35,.26); }
    .stat.added.show.c-green { border-color: rgba(76,175,80,.55);  box-shadow: 0 0 0 1px rgba(76,175,80,.26); }
    .stat.added.show.c-blue  { border-color: rgba(97,175,239,.55); box-shadow: 0 0 0 1px rgba(97,175,239,.26); }
    .bars { display: flex; align-items: flex-end; gap: 5px; height: 52px; }
    .bars i { flex: 1; background: linear-gradient(180deg, rgba(245,166,35,.8), rgba(245,166,35,.32)); }
    .bars i:nth-child(1){height:40%}.bars i:nth-child(2){height:54%}.bars i:nth-child(3){height:47%}
    .bars i:nth-child(4){height:61%}.bars i:nth-child(5){height:57%}.bars i:nth-child(6){height:69%}
    .bars i:nth-child(7){height:64%}.bars i:nth-child(8){height:77%}.bars i:nth-child(9){height:71%}
    .bars i:nth-child(10){height:83%}

    .tpane { flex: none; height: 0; overflow: hidden; background: #0d0f13;
      border-top: 1px solid #363c45; transition: height .45s ease; }
    .tpane.open { height: 150px; }
    .term { color: #c8d0da; font: 11.5px/1.6 ui-monospace, "SF Mono", Menlo, Consolas, monospace;
      padding: 10px 11px; min-height: 150px; white-space: pre-wrap; word-break: break-word; }
    .term .g { color: #4caf50; } .term .a { color: #f5a623; }
    .term .m { color: #868f9a; } .term .b { color: #61afef; }
    .cursor { display: inline-block; width: 7px; height: 14px; background: #f5a623;
      vertical-align: -2px; animation: blink 1.05s steps(1) infinite; }
    @keyframes blink { 50% { opacity: 0; } }
    @media (prefers-reduced-motion: reduce) { .cursor { animation: none; } }
    @media (max-width: 560px) { .m-side { width: 118px; } }
  `;

  constructor() {
    super();
    this._timers = [];
    this._reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  }

  connectedCallback() {
    super.connectedCallback();
    this._run();
  }

  disconnectedCallback() {
    this._clear();
    super.disconnectedCallback();
  }

  get _scene() { return SCENE; }

  _clear() { this._timers.forEach(clearTimeout); this._timers = []; }
  _at(ms, fn) { this._timers.push(setTimeout(fn, ms)); }

  _run() {
    this._clear();
    const s = this._scene;
    this._hot = false; this._open = false; this._show = false; this._out = '';
    if (this._reduce) {
      this._hot = this._open = this._show = true;
      this._out = s.script.join('\n');
      return;
    }
    this._at(1100, () => { this._hot = true; });        // "click" >_ terminal
    this._at(1500, () => { this._open = true; });       // the pane slides in
    this._at(1950, () => this._type(() => this._at(350, () => { this._show = true; })));
    this._at(9500, () => this._run());                  // replay the scene
  }

  _type(after) {
    const lines = this._scene.script;
    let i = 0, out = '';
    const next = () => {
      if (i >= lines.length) { this._out = out; after?.(); return; }
      out += lines[i] + '\n'; i++;
      this._out = out + '<span class="cursor"></span>';
      this._at(640, next);
    };
    next();
  }

  render() {
    const s = this._scene;
    const side = ['dashboard', 'calendar', 'notes', 'queue'];
    return html`
    <div class="mock" aria-hidden="true">
      <div class="m-top">
        <span class="brand"><svg viewBox="0 0 64 64" width="15" height="15"><path d="M18 4H56a4 4 0 0 1 4 4v38L46 60H8a4 4 0 0 1-4-4V18z" fill="#f5a623"></path><path d="M21 21 43 43M43 21 21 43" stroke="#1b1e24" stroke-width="9"></path></svg>X/BIN</span>
        <span class="m-chip">home-lab</span>
        <span class="m-sp"></span>
        <span class="dt dg"></span>
      </div>
      <div class="m-tabs">
        <div class="m-tab on">Home</div>
        <div class="m-tab">Ops <span class="dt sm dam"></span></div>
        <div class="m-tab">Media</div>
      </div>
      <div class="m-body">
        <div class="m-side">
          <div class="m-grp">apps</div>
          ${side.map(n => html`<div class="m-item ${s.side === n ? 'on' : ''}">
            ${s.side === n ? html`<span class="dt sm da"></span>` : nothing}${n}
            ${n === 'notes' ? html`<span class="st dt sm dg"></span>` : nothing}
            ${n === 'queue' ? html`<span class="st dt sm dam"></span>` : nothing}
          </div>`)}
          <div class="m-grp">tiles</div>
          <div class="m-item ${s.side === 'manager' ? 'on' : ''}">
            ${s.side === 'manager' ? html`<span class="dt sm da"></span>` : nothing}manager</div>
          <div class="m-item">admin</div>
        </div>
        <div class="m-main">
          <div class="m-card">
            <div class="h"><span class="dt sm da"></span><span class="path">${s.path}</span>
              <span class="tbtn ${this._hot ? 'hot' : ''}">&gt;_ terminal</span></div>
            <div class="body">
              <div class="dash">
                <div class="dash-h">${s.dash}<span class="sp"></span><span class="live">live</span></div>
                <div class="stats">
                  <div class="stat"><b>1.24M</b><span>requests</span>
                    <svg width="100%" height="15" viewBox="0 0 90 15" preserveAspectRatio="none"><polyline points="${SPARKS.blue}" fill="none" stroke="#61afef" stroke-width="1.4"/></svg></div>
                  <div class="stat"><b>0.31%</b><span>errors</span>
                    <svg width="100%" height="15" viewBox="0 0 90 15" preserveAspectRatio="none"><polyline points="${SPARKS.green}" fill="none" stroke="#4caf50" stroke-width="1.4"/></svg></div>
                  <div class="stat added ${this._show ? `show c-${s.result.c}` : ''}"><b>${s.result.v}</b><span>${s.result.l}</span>
                    <svg width="100%" height="15" viewBox="0 0 90 15" preserveAspectRatio="none"><polyline points="${SPARKS[s.result.c]}" fill="none" stroke="${({ amber: '#f5a623', green: '#4caf50', blue: '#61afef' })[s.result.c]}" stroke-width="1.4"/></svg></div>
                </div>
                <div class="bars">${Array.from({ length: 10 }, () => html`<i></i>`)}</div>
              </div>
              <div class="tpane ${this._open ? 'open' : ''}"><div class="term">${unsafeHTML(this._out || '')}</div></div>
            </div>
          </div>
        </div>
      </div>
    </div>`;
  }
}

customElements.define('xb-shell-scene', XbShellScene);
