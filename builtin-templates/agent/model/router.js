// model/router.js — where the tile is, as an address: a conversation
// (#c=<id>), the Automations page (#auto, or one automation: #auto=<kind>:<id>),
// or an invite to join a conversation (#join=<token>). The web keeps it in
// the frame's hash; a native view maps its own deep links onto the same words
// (app.follow()).

// parse reads an address (a hash, or any text holding one).
export function parse(hash) {
  const h = String(hash || '');
  const c = /(?:^#|&)c=(\d+)/.exec(h);
  const a = /(?:^#|&)auto(?:=(\w+):(\d+))?/.exec(h);
  return {
    conv: c ? +c[1] : null,
    join: h.includes('join=') ? h : '',
    auto: a ? { kind: a[1], id: a[2] } : null,
  };
}

// The addresses (without the '#'); home is the empty one.
export const convHash = (id) => 'c=' + id;
export const autoHash = (kind, id) => (kind ? `auto=${kind}:${id}` : 'auto');
