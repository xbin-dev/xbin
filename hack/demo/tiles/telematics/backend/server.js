// apps/telematics — the feeds live dispatch will read (feeds.json): van
// positions from the customers' telematics providers and the weather
// alerts for the depots' counties. A new tile on the film set: its `egress`
// interface (a net) is left for an admin to bind (hack/demo/seed.sh), which is
// what the shell's "interfaces to bind" shows them. It makes no network
// calls of its own — the feeds' API keys and the sync aren't part of the
// set — and only serves the feed list to its page.
'use strict';
const fs = require('fs');
const path = require('path');
const { json, serve } = require('./tile');

const feeds = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'feeds.json'), 'utf8')).feeds;

serve([
  ['GET', '/feeds', (req, res) => json(res, { feeds })],
]);
