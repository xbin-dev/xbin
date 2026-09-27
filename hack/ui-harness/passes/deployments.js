// hack/ui-harness/passes/deployments.js — covers nothing yet: a stub
// registered ahead of tile deployments. Until the feature lands it checks that
// the seed laid its fixture out as the pass needs it, then prints SKIP not
// implemented yet.
//
// The fixture is apps/deployy: static, owned by org:sales, so sales1 manages
// it; dev1 has terminal level without managing it; infra1 reads it; a granted
// `uses` edge on apps/leads (seed.sh). The pass to come: dev1 adds a
// deployment from the Deployments layout and opens its URL; a reader sees the
// primary only; promote and roll back move the bare URL; the edge policy and
// the dormant list; a protected primary refuses dev1 and takes sales1's
// reviewed promotion. Every run ends with the fixture back in the zero state.
const { URL, login, checker } = require('../lib');

const TILE = 'apps/deployy';
const EDGE = { from: TILE, target: 'apps/leads', role: 'reader' };

async function deployments(browser) {
  const { check, skip, done } = checker('deployments');
  const { ctx } = await login(browser, 'admin', 'admin');
  try {
    const comps = await (await ctx.request.get(`${URL}/api/xbin/components`)).json();
    const row = comps.find((c) => c.path === TILE);
    check(!!row && row.owner === 'org:sales', `${TILE} is seeded, owned by org:sales (${row?.owner})`);
    check(!!row && !row.runtime, `${TILE} is static (${row?.runtime || 'no runtime'})`);
    check((row?.uses || []).some((u) => u.target === EDGE.target && u.role === EDGE.role), `${TILE} uses ${EDGE.target} (${JSON.stringify(row?.uses)})`);
    const grants = (await (await ctx.request.get(`${URL}/api/xbin/grants`)).json()).grants || [];
    check(grants.some((g) => g.from === EDGE.from && g.target === EDGE.target && g.role === EDGE.role), `the ${EDGE.target} edge is granted, not pending`);
    const users = (await (await ctx.request.get(`${URL}/api/xbin/users`)).json()).users || [];
    const level = (id) => users.find((u) => u.id === id)?.tiles?.[TILE];
    check(level('dev1') === 'terminal', `dev1 has terminal level on ${TILE} (${level('dev1')})`);
    check(level('infra1') === 'read', `infra1 reads ${TILE} (${level('infra1')})`);
  } finally {
    await ctx.close();
  }
  skip('not implemented yet');
  done();
}

module.exports = { deployments };
