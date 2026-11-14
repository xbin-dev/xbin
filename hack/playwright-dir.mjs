// hack/playwright-dir.mjs — `node --import hack/playwright-dir.mjs <script>`:
// the bare specifier 'playwright' resolves to the install PLAYWRIGHT_DIR
// names (hack/dev-setup.sh writes it into .dev.mk), wherever the script
// lives. The agent template's browser scripts import('playwright') as they
// do inside a workspace, where the rootfs carries it; in this checkout no
// node_modules sits above them, so without this hook they print SKIP and
// exit 0 — hack/agent-ui-check.mjs runs them through it.
import { registerHooks } from 'node:module';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

const dir = process.env.PLAYWRIGHT_DIR;
if (dir) {
  const url = pathToFileURL(join(dir, 'node_modules', 'playwright', 'index.mjs')).href;
  registerHooks({ resolve: (spec, ctx, next) => (spec === 'playwright' ? { url, shortCircuit: true } : next(spec, ctx)) });
}
