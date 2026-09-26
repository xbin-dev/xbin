#!/usr/bin/env node
// markdown-parity.mjs — the expected tokens for XbinCore's MarkdownLexer
// (the ACP agent screen lexes agent markdown natively): each corpus string
// through the runtime's own lexer, web/xb/rt-markdown.js (marked), so the
// Swift port is held to what native tiles render.
//
//   node native/tools/markdown-parity.mjs [repo] [rt-markdown.js]
//
// Writes native/ios/Packages/XbinCore/Tests/XbinCoreTests/Resources/markdown/expected.json.
// rt-markdown.js defaults to <repo>/web/xb/rt-markdown.js (the runtime WP).
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { pathToFileURL } from 'node:url';

const repo = resolve(process.argv[2] || '.');
const rt = resolve(process.argv[3] || join(repo, 'web/xb/rt-markdown.js'));
const dir = join(repo, 'native/ios/Packages/XbinCore/Tests/XbinCoreTests/Resources/markdown');

// rt-markdown.js imports marked from the served /vendor path: point it at the file.
const src = readFileSync(rt, 'utf8').replace("'/vendor/marked.esm.js'",
  JSON.stringify(pathToFileURL(join(repo, 'web/vendor/marked.esm.js')).href));
const tmp = join(mkdtempSync(join(tmpdir(), 'mdparity-')), 'rt-markdown.mjs');
writeFileSync(tmp, src);
const { markdownTokens } = await import(pathToFileURL(tmp).href);

const corpus = JSON.parse(readFileSync(join(dir, 'corpus.json'), 'utf8'));
const out = corpus.map((s) => ({ src: s, tokens: markdownTokens(s) }));
writeFileSync(join(dir, 'expected.json'), JSON.stringify(out, null, 1) + '\n');
console.log(`${out.length} cases → ${join(dir, 'expected.json')}`);
