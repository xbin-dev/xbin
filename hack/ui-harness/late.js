// hack/ui-harness/late.js — holdReads(page, glob, ms): every GET the page
// makes to glob answers ms late (a page route) until release(); arrived()
// resolves once each held answer has reached the page. For passes that
// must make an older answer land after a newer one (netPickers: a tile
// admin reload overtaken by the next action's).
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function holdReads(page, glob, ms = 1500) {
  let holding = true;
  const late = [];
  await page.route(glob, async (route) => {
    if (!holding || route.request().method() !== 'GET') return route.continue();
    late.push(page.waitForEvent('requestfinished', { predicate: (r) => r === route.request(), timeout: 10000 }));
    const resp = await route.fetch();
    await sleep(ms);
    await route.fulfill({ response: resp });
  });
  return { release: () => { holding = false; }, arrived: () => Promise.all(late) };
}

module.exports = { holdReads };
