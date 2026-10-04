// model/router.js — where the tile is, as an address: a conversation
// (#c=<id>), the Automations page (#auto, or one automation: #auto=<kind>:<id>),
// the Projects page (#proj, or one project: #proj=<id> — its home is
// homeOf(id), model/homes.js), or an invite to join a conversation
// (#join=<token>). The web keeps it in
// the frame's hash; a native view maps its own deep links onto the same words
// (app.follow()).

// parse reads an address (a hash, or any text holding one).
export function parse(hash) {
  const h = String(hash || '');
  const c = /(?:^#|&)c=(\d+)/.exec(h);
  const a = /(?:^#|&)auto(?:=(\w+):(\d+))?/.exec(h);
  const p = /(?:^#|&)proj(?:=(\d+))?(?=&|$)/.exec(h);
  return {
    conv: c ? +c[1] : null,
    join: h.includes('join=') ? h : '',
    auto: a ? { kind: a[1], id: a[2] } : null,
    // the Projects page — only when the address names it (null-ish otherwise:
    // an address of the other kinds reads as it always did)
    ...(p ? { proj: { id: p[1] ? +p[1] : null } } : {}),
  };
}

// The addresses (without the '#'); home is the empty one.
export const convHash = (id) => 'c=' + id;
export const autoHash = (kind, id) => (kind ? `auto=${kind}:${id}` : 'auto');
export const projHash = (id) => (id != null ? `proj=${id}` : 'proj');
