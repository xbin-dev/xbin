// hack/ui-harness/passes/livereload.js — covers nothing yet: a stub registered
// ahead of pause live reload. Until the feature lands it checks that the seed
// laid its fixture out as the pass needs it, then prints SKIP not implemented
// yet.
//
// The fixture is apps/reloady: static, owned by org:devs, so dev1 manages it
// as a devs admin; infra1 reads it (seed.sh). The pass to come: the zero state
// shows no chip; dev1 pauses live reload from the terminal window and infra1
// sees it paused; a save while paused counts as pending instead of reloading;
// reload now applies it; resume returns the tile to the zero state; on
// apps/crawler (node) the control is disabled with the server's reason, since
// the harness xbind runs without --isolate (HARNESS_ISOLATE=1 runs that half
// instead, run.sh). Every run ends with the fixture back in the zero state.
const { URL, login, checker } = require('../lib');

const TILE = 'apps/reloady';

async function livereload(browser) {
  const { check, skip, done } = checker('livereload');
  const { ctx } = await login(browser, 'admin', 'admin');
  try {
    const comps = await (await ctx.request.get(`${URL}/api/xbin/components`)).json();
    const row = comps.find((c) => c.path === TILE);
    check(!!row && row.owner === 'org:devs', `${TILE} is seeded, owned by org:devs (${row?.owner})`);
    check(!!row && !row.runtime, `${TILE} is static (${row?.runtime || 'no runtime'})`);
    const users = (await (await ctx.request.get(`${URL}/api/xbin/users`)).json()).users || [];
    const infra1 = users.find((u) => u.id === 'infra1');
    check(infra1?.tiles?.[TILE] === 'read', `infra1 reads ${TILE} (${infra1?.tiles?.[TILE]})`);
  } finally {
    await ctx.close();
  }
  skip('not implemented yet');
  done();
}

module.exports = { livereload };
