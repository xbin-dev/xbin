#!/usr/bin/env node
// hack/xbn/vocab-json.mjs — print the JSON export of web/xb/vocab.js, the
// checked-in native/spec/vocab.json the Swift side reads:
//   node hack/xbn/vocab-json.mjs > native/spec/vocab.json
// hack/xb-native.test.mjs fails when the two differ.
const { VOCAB } = await import(new URL('../../web/xb/vocab.js', import.meta.url).href);
process.stdout.write(`${JSON.stringify(VOCAB, null, 1)}\n`);
