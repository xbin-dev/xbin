// hack/helpers.test.mjs — the prebuilt-helpers scripts (hack/helpers-lib.sh,
// fetch-helpers.sh, publish-helpers.sh, s3-lib.sh) and
// hack/check-large-files.sh, run by `make js-test` (part of `make check`):
// node's built-in runner plus bash, git, tar, zstd, curl and python3 — no
// docker, no network beyond 127.0.0.1. Each test gets a throwaway git repo
// holding the real scripts next to FAKE build scripts (they write marker
// files and log that they ran). The bucket is `rclone serve s3` over a
// directory (the publisher's signed side; those tests skip without an
// rclone that has it) and `python3 -m http.server` over the same directory
// (the public read URL). Covered: the key (stable, moved by any build
// input), the manifest's syntax, fetch → verify → install, a sha256
// mismatch refused with nothing installed, the source-build fallback (an
// unpublished key, a failed download, a version override, --build) and its
// GitHub warning, the missing-URL error, publish's refusals (dirty tree,
// CI, settings, a URL make helpers wouldn't use), its upload + public
// read-back + manifest rewrite round trip, never overwriting an object;
// the large-files guard's size, ELF/PE and allowlist rules.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createServer, connect } from 'node:net';
import {
  mkdtempSync, readFileSync, writeFileSync, existsSync, mkdirSync, rmSync, copyFileSync,
  chmodSync, statSync, appendFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..');
const root = mkdtempSync(join(tmpdir(), 'xbin-helpers-test-'));
const buckets = join(root, 's3');
mkdirSync(buckets);
const procs = [];
process.on('exit', () => {
  for (const p of procs) p.kill('SIGKILL');
  rmSync(root, { recursive: true, force: true });
});

const freePort = () => new Promise((res) => {
  const srv = createServer().listen(0, '127.0.0.1', () => {
    const { port } = srv.address();
    srv.close(() => res(port));
  });
});
const listening = async (port) => {
  for (let i = 0; i < 100; i++) {
    const ok = await new Promise((res) => {
      const c = connect(port, '127.0.0.1', () => { c.destroy(); res(true); });
      c.on('error', () => res(false));
    });
    if (ok) return true;
    await new Promise((r) => setTimeout(r, 50));
  }
  return false;
};
const S3_KEY = 'AKIDXBINTEST01';
const S3_SECRET = 'xbin-test-secret-0001';
let publicURL = '';
let s3Endpoint = ''; // empty: no rclone with `serve s3` here
before(async () => {
  const pport = await freePort();
  const py = spawn('python3', ['-m', 'http.server', '--bind', '127.0.0.1', '--directory', buckets, String(pport)], { stdio: 'ignore' });
  procs.push(py);
  assert.ok(await listening(pport), 'python3 -m http.server did not come up');
  publicURL = `http://127.0.0.1:${pport}`;
  let haveRclone = false;
  try {
    haveRclone = execFileSync('rclone', ['serve', 's3', '--help'], { stdio: 'pipe', encoding: 'utf8' }).includes('--auth-key');
  } catch { /* no rclone */ }
  if (!haveRclone) return;
  const sport = await freePort();
  const rc = spawn('rclone', ['serve', 's3', buckets, '--addr', `127.0.0.1:${sport}`, '--auth-key', `${S3_KEY},${S3_SECRET}`],
    { stdio: 'ignore', env: { ...process.env, RCLONE_CONFIG: '/dev/null' } });
  procs.push(rc);
  if (await listening(sport)) s3Endpoint = `http://127.0.0.1:${sport}`;
});
after(() => { for (const p of procs) p.kill(); });

// The fake build scripts: each writes its outputs ("<name> <MARK>") and logs.
const builds = {
  'build-gocryptfs.sh': ['gocryptfs'],
  'build-fuse-overlayfs.sh': ['fuse-overlayfs'],
  'build-vmkernel.sh': ['vmlinux'],
  'build-mkfs-erofs.sh': ['mkfs.erofs'],
  'build-qemu.sh': ['qemu-system-x86_64', 'qemu-bios-microvm.bin', 'qemu-pvh.bin'],
  'build-vhost-vsock.sh': ['vhost-device-vsock'],
};

let n = 0;
// fixture(): a fresh committed repo; returns its paths and a runner.
function fixture() {
  const dir = join(root, `t${n++}`);
  const hack = join(dir, 'hack');
  mkdirSync(join(hack, 'gocryptfs-patches'), { recursive: true });
  mkdirSync(join(hack, 'gofuse-patches'));
  mkdirSync(join(hack, 'vmkernel'));
  for (const f of ['helpers-lib.sh', 's3-lib.sh', 'fetch-helpers.sh', 'publish-helpers.sh', 'helpers.sha256']) {
    copyFileSync(join(repo, 'hack', f), join(hack, f));
  }
  for (const [s, outs] of Object.entries(builds)) {
    const body = outs.map((o) => `echo "${o} \${MARK:-one}" > "$1/${o}"`).join('\n');
    writeFileSync(join(hack, s), `#!/bin/sh\nset -eu\nmkdir -p "$1"\n${body}\necho ${s} >> "$BUILD_LOG"\n`);
    chmodSync(join(hack, s), 0o755);
  }
  writeFileSync(join(hack, 'gocryptfs-patches/0001-a.patch'), 'patch a\n');
  writeFileSync(join(hack, 'gofuse-patches/0001-b.patch'), 'patch b\n');
  writeFileSync(join(hack, 'vmkernel/xbin.config'), 'CONFIG_X=y\n');
  // this fixture's bucket: <buckets>/<name>, publicly at <publicURL>/<name>
  const bucket = `b${n}`;
  const s3 = join(buckets, bucket);
  mkdirSync(s3);
  const env = {
    PATH: process.env.PATH, HOME: dir, GIT_CONFIG_NOSYSTEM: '1',
    GIT_AUTHOR_NAME: 't', GIT_AUTHOR_EMAIL: 't@t', GIT_COMMITTER_NAME: 't', GIT_COMMITTER_EMAIL: 't@t',
    PLATFORM: 'linux/amd64', BUILD_LOG: join(dir, 'build.log'),
    XBIN_HELPERS_URL: `${publicURL}/${bucket}`, XBIN_HELPERS_S3_PREFIX: 'helpers/',
  };
  // the publisher's settings (gitignored, like the real one)
  const secret = (over = {}) => {
    const kv = {
      XBIN_HELPERS_S3_BUCKET: bucket, XBIN_HELPERS_S3_ENDPOINT: s3Endpoint, XBIN_HELPERS_S3_REGION: 'us-east-1',
      XBIN_HELPERS_S3_PREFIX: 'helpers/', XBIN_HELPERS_URL: `${publicURL}/${bucket}`,
      AWS_ACCESS_KEY_ID: S3_KEY, AWS_SECRET_ACCESS_KEY: S3_SECRET, ...over,
    };
    writeFileSync(join(dir, 's3secret.env'), Object.entries(kv).map(([k, v]) => `${k}=${v}\n`).join(''), { mode: 0o600 });
  };
  const git = (...a) => execFileSync('git', a, { cwd: dir, env, stdio: 'pipe' });
  git('init', '-q');
  writeFileSync(join(dir, '.gitignore'), 'bin*/\n*.log\nstage/\ns3secret.env\n');
  git('add', '-A');
  git('commit', '-qm', 'init');
  const run = (script, args = [], extra = {}) => {
    const r = spawnSync(join(hack, script), args, {
      cwd: dir, env: { ...env, ...extra }, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
    });
    return { out: `${r.stdout}${r.stderr}`, code: r.status };
  };
  const built = () => (existsSync(env.BUILD_LOG) ? readFileSync(env.BUILD_LOG, 'utf8') : '');
  const manifest = join(hack, 'helpers.sha256');
  return { dir, hack, bin: join(dir, 'bin'), s3, env, git, run, built, manifest, secret };
}

const keyOf = (fx, g) => {
  const r = fx.run('fetch-helpers.sh', ['--status', g]);
  assert.equal(r.code, 0, r.out);
  const [grp, key, arch, state] = r.out.trim().split(' ');
  assert.equal(grp, g);
  assert.equal(arch, 'amd64');
  return { key, state };
};

test('key: 12 hex digits, stable, moved by every build input', () => {
  const fx = fixture();
  const k = keyOf(fx, 'containerfs').key;
  assert.match(k, /^[0-9a-f]{12}$/);
  assert.equal(keyOf(fx, 'containerfs').key, k);
  assert.equal(keyOf(fx, 'containerfs').state, 'unpublished');
  const vm = keyOf(fx, 'vm').key;
  assert.notEqual(vm, k);
  // a patch edit, a new patch, a build script's pin
  appendFileSync(join(fx.hack, 'gofuse-patches/0001-b.patch'), 'more\n');
  const k2 = keyOf(fx, 'containerfs').key;
  assert.notEqual(k2, k);
  writeFileSync(join(fx.hack, 'gocryptfs-patches/0002-c.patch'), 'patch c\n');
  const k3 = keyOf(fx, 'containerfs').key;
  assert.notEqual(k3, k2);
  assert.equal(keyOf(fx, 'vm').key, vm, 'containerfs inputs leave vm alone');
  appendFileSync(join(fx.hack, 'vmkernel/xbin.config'), 'CONFIG_Y=y\n');
  assert.notEqual(keyOf(fx, 'vm').key, vm);
  appendFileSync(join(fx.hack, 'build-qemu.sh'), '# QEMU_VERSION bump\n');
  // unknown group
  assert.notEqual(fx.run('fetch-helpers.sh', ['--status', 'nope']).code, 0);
});

test('unpublished key: builds from source, warns under GitHub Actions, then is up to date', () => {
  const fx = fixture();
  const r = fx.run('fetch-helpers.sh', [], { GITHUB_ACTIONS: 'true' });
  assert.equal(r.code, 0, r.out);
  assert.match(r.out, /::warning title=unpublished helpers::containerfs helpers .* make helpers-publish/);
  assert.match(r.out, /::warning title=unpublished helpers::vm helpers/);
  assert.equal(fx.built().trim().split('\n').length, 6, 'every build script ran');
  const { key } = keyOf(fx, 'containerfs');
  assert.equal(readFileSync(join(fx.bin, '.helpers-containerfs'), 'utf8').trim(), `${key} amd64 built`);
  assert.equal(readFileSync(join(fx.bin, 'qemu-pvh.bin'), 'utf8'), 'qemu-pvh.bin one\n');
  const again = fx.run('fetch-helpers.sh', ['containerfs'], { GITHUB_ACTIONS: 'true' });
  assert.equal(again.code, 0, again.out);
  assert.match(again.out, /containerfs: up to date/);
  assert.match(again.out, /::warning title=unpublished helpers::/, 'a cached build still says to publish');
  assert.equal(fx.built().trim().split('\n').length, 6, 'nothing rebuilt');
  // arm64: no vm group there; containerfs builds
  const arm = fx.run('fetch-helpers.sh', ['--dest', join(fx.dir, 'bin-arm')], { PLATFORM: 'linux/arm64' });
  assert.equal(arm.code, 0, arm.out);
  assert.match(arm.out, /vm: not built for arm64/);
  assert.ok(existsSync(join(fx.dir, 'bin-arm/gocryptfs')));
  assert.ok(!existsSync(join(fx.dir, 'bin-arm/vmlinux')));
});

// publish(fx): make helpers-publish into this fixture's bucket, commit.
function publish(fx, args = [], extra = {}) {
  const r = fx.run('publish-helpers.sh', args, extra);
  assert.equal(r.code, 0, r.out);
  fx.git('commit', '-qam', 'publish');
  return r;
}

// stageInto(fx): publish --stage-only, then lay the staged sets into the
// bucket under the prefix and their lines into the manifest (committed) —
// what an upload leaves, without S3.
function stageInto(fx, groups = [], extra = {}) {
  const stage = join(fx.dir, 'stage');
  const r = fx.run('publish-helpers.sh', ['--stage-only', stage, ...groups], extra);
  assert.equal(r.code, 0, r.out);
  execFileSync('bash', ['-c', 'mkdir -p "$2/helpers" && cp -r "$1"/*/ "$2/helpers/"', '_', stage, fx.s3]);
  appendFileSync(fx.manifest, readFileSync(join(stage, 'helpers.sha256')));
  fx.git('commit', '-qam', 'stage');
  rmSync(stage, { recursive: true });
}

test('published key: fetched, every file verified, installed; no build', () => {
  const fx = fixture();
  stageInto(fx, [], { MARK: 'published' });
  assert.equal(keyOf(fx, 'containerfs').state, 'published');
  assert.equal(keyOf(fx, 'vm').state, 'published');
  const lines = readFileSync(fx.manifest, 'utf8').split('\n').filter((l) => l && !l.startsWith('#'));
  assert.equal(lines.length, 2 + 2 + 6, 'each object + each file');
  for (const l of lines) assert.match(l, /^(containerfs|vm) [0-9a-f]{12} amd64 \S+ [0-9a-f]{64}$/);
  const { key } = keyOf(fx, 'containerfs');
  assert.ok(existsSync(join(fx.s3, 'helpers/containerfs', key, 'amd64.tar.zst')));

  writeFileSync(fx.env.BUILD_LOG, '');
  const r = fx.run('fetch-helpers.sh');
  assert.equal(r.code, 0, r.out);
  assert.match(r.out, new RegExp(`fetching prebuilt ${key} \\(amd64\\) from ${fx.env.XBIN_HELPERS_URL}/helpers/containerfs/${key}/amd64\\.tar\\.zst`));
  assert.match(r.out, /containerfs: installed prebuilt .* every file verified/);
  assert.equal(fx.built(), '', 'nothing built');
  assert.equal(readFileSync(join(fx.bin, 'gocryptfs'), 'utf8'), 'gocryptfs published\n');
  assert.equal(statSync(join(fx.bin, 'gocryptfs')).mode & 0o777, 0o755);
  assert.equal(statSync(join(fx.bin, 'qemu-bios-microvm.bin')).mode & 0o777, 0o644);
  assert.equal(readFileSync(join(fx.bin, '.helpers-vm'), 'utf8').split(' ')[2].trim(), 'fetched');
  assert.doesNotMatch(r.out, /::warning/);
  assert.match(fx.run('fetch-helpers.sh').out, /up to date .*fetched/);

  // --build and a version override go to source even though published
  const b = fx.run('fetch-helpers.sh', ['--build', 'containerfs']);
  assert.equal(b.code, 0, b.out);
  assert.match(fx.built(), /build-gocryptfs\.sh/);
  writeFileSync(fx.env.BUILD_LOG, '');
  rmSync(join(fx.bin, '.helpers-containerfs'));
  const o = fx.run('fetch-helpers.sh', ['containerfs'], { GOCRYPTFS_VERSION: 'v9' });
  assert.match(o.out, /GOCRYPTFS_VERSION set/);
  assert.match(fx.built(), /build-gocryptfs\.sh/);

  // a download failure falls back to source
  writeFileSync(fx.env.BUILD_LOG, '');
  const d = fx.run('fetch-helpers.sh', ['--dest', join(fx.dir, 'bin2'), 'containerfs'],
    { XBIN_HELPERS_S3_PREFIX: 'nowhere/', GITHUB_ACTIONS: 'true' });
  assert.equal(d.code, 0, d.out);
  assert.match(d.out, /::warning title=helpers download failed::/);
  assert.match(fx.built(), /build-gocryptfs\.sh/);

  // a published key but no URL: fails, saying what to set
  const u = fx.run('fetch-helpers.sh', ['--dest', join(fx.dir, 'bin3')], { XBIN_HELPERS_URL: '' });
  assert.notEqual(u.code, 0);
  assert.match(u.out, /no helpers URL is configured — set XBIN_HELPERS_URL/);
});

test('sha256 mismatch: refused, nothing installed', () => {
  const fx = fixture();
  stageInto(fx, ['containerfs']);
  const good = readFileSync(fx.manifest, 'utf8');
  const { key } = keyOf(fx, 'containerfs');
  // a file inside the object differs from the manifest
  writeFileSync(fx.manifest, good.replace(/^(containerfs \S+ amd64 gocryptfs )\S+$/m, `$1${'0'.repeat(64)}`));
  let r = fx.run('fetch-helpers.sh', ['containerfs']);
  assert.notEqual(r.code, 0);
  assert.match(r.out, /sha256 mismatch for gocryptfs .*\(nothing installed\)/);
  assert.ok(!existsSync(join(fx.bin, 'gocryptfs')));
  assert.ok(!existsSync(join(fx.bin, 'fuse-overlayfs')), 'no file of the set lands');
  // the object itself replaced in the bucket
  writeFileSync(fx.manifest, good);
  const obj = join(fx.s3, 'helpers/containerfs', key, 'amd64.tar.zst');
  execFileSync('bash', ['-c', 'cd "$(mktemp -d)" && echo evil > gocryptfs && echo x > fuse-overlayfs && tar -cf - gocryptfs fuse-overlayfs | zstd -qf -o "$1"', '_', obj]);
  r = fx.run('fetch-helpers.sh', ['containerfs']);
  assert.notEqual(r.code, 0);
  assert.match(r.out, /sha256 mismatch for http:\/\/.*amd64\.tar\.zst/);
  assert.ok(!existsSync(join(fx.bin, 'gocryptfs')));
});

test('manifest: malformed lines are refused with their line number', () => {
  const fx = fixture();
  const k = keyOf(fx, 'containerfs').key;
  const sha = 'a'.repeat(64);
  for (const [line, why, at = 3] of [
    [`containerfs ${k} amd64 gocryptfs`, /want 5 fields/],
    [`nope ${k} amd64 gocryptfs ${sha}`, /unknown group 'nope'/],
    [`containerfs XYZ amd64 gocryptfs ${sha}`, /not 12 hex digits/],
    [`vm ${k} arm64 vmlinux ${sha}`, /vm has no arch 'arm64'/],
    [`containerfs ${k} amd64 gocryptfs abc`, /not a sha256/],
    [`containerfs ${k} amd64 vmlinux ${sha}`, /'vmlinux' is not a containerfs file/],
    [`containerfs ${k} amd64 gocryptfs ${sha}\ncontainerfs ${k} amd64 gocryptfs ${sha}`, /duplicate entry/, 4],
  ]) {
    writeFileSync(fx.manifest, `# header\n\n${line}\n`);
    const r = fx.run('fetch-helpers.sh', ['--status']);
    assert.notEqual(r.code, 0, line);
    assert.match(r.out, new RegExp(`hack/helpers\\.sha256:${at}: `), line);
    assert.match(r.out, why, line);
  }
  // comments, blank lines and a partial entry (not every file) parse; the
  // partial one doesn't count as published
  writeFileSync(fx.manifest, `# c\n\n  # indented comment\ncontainerfs ${k} amd64 gocryptfs ${sha}\n`);
  const r = fx.run('fetch-helpers.sh', ['--status', 'containerfs']);
  assert.equal(r.code, 0, r.out);
  assert.match(r.out, /unpublished/);
});

test('publish: refuses a dirty tree, CI, missing or tracked settings, a URL make helpers would not use', () => {
  const fx = fixture();
  fx.secret();
  writeFileSync(join(fx.hack, 'gofuse-patches/0002-new.patch'), 'uncommitted\n');
  let r = fx.run('publish-helpers.sh');
  assert.notEqual(r.code, 0);
  assert.match(r.out, /tree is dirty/);
  rmSync(join(fx.hack, 'gofuse-patches/0002-new.patch'));
  r = fx.run('publish-helpers.sh', [], { GITHUB_ACTIONS: 'true' });
  assert.notEqual(r.code, 0);
  assert.match(r.out, /never in CI/);
  fx.secret({ AWS_SECRET_ACCESS_KEY: '', XBIN_HELPERS_URL: '' });
  r = fx.run('publish-helpers.sh');
  assert.notEqual(r.code, 0);
  assert.match(r.out, /not set in .*s3secret\.env: XBIN_HELPERS_URL AWS_SECRET_ACCESS_KEY/);
  rmSync(join(fx.dir, 's3secret.env'));
  r = fx.run('publish-helpers.sh');
  assert.match(r.out, /no .*s3secret\.env — copy s3secret\.env\.example/);
  fx.secret();
  fx.git('add', '-f', 's3secret.env');
  fx.git('commit', '-qm', 'oops');
  r = fx.run('publish-helpers.sh');
  assert.match(r.out, /tracked by git — it holds secrets/);
  fx.git('rm', '-q', '--cached', 's3secret.env');
  fx.git('commit', '-qm', 'fix');
  // the lib's defaults (what make helpers fetches from) name another place
  const lib = join(fx.hack, 'helpers-lib.sh');
  writeFileSync(lib, readFileSync(lib, 'utf8').replace(/^HELPERS_URL_DEFAULT=""$/m, 'HELPERS_URL_DEFAULT="https://elsewhere.example"'));
  fx.git('commit', '-qam', 'url');
  r = fx.run('publish-helpers.sh');
  assert.notEqual(r.code, 0);
  assert.match(r.out, /make helpers would look elsewhere/);
  assert.equal(fx.built(), '', 'refused before building anything');
});

test('publish: uploads, checks the public read, rewrites the manifest; never overwrites', (t) => {
  if (!s3Endpoint) return t.skip('no `rclone serve s3` here');
  const fx = fixture();
  fx.secret();
  let r = publish(fx, [], { MARK: 'first' });
  assert.match(r.out, /HELPERS_URL_DEFAULT in hack\/helpers-lib\.sh is still a placeholder/);
  const { key } = keyOf(fx, 'containerfs');
  const obj = join(fx.s3, 'helpers/containerfs', key, 'amd64.tar.zst');
  assert.ok(existsSync(obj), 'uploaded under the prefix');
  assert.equal(keyOf(fx, 'vm').state, 'published');
  const pinned = readFileSync(fx.manifest, 'utf8');
  const bytes = readFileSync(obj);
  const f = fx.run('fetch-helpers.sh');
  assert.equal(f.code, 0, f.out);
  assert.equal(readFileSync(join(fx.bin, 'vmlinux'), 'utf8'), 'vmlinux first\n');
  r = fx.run('publish-helpers.sh', ['containerfs']);
  assert.match(r.out, /already in hack\/helpers\.sha256/);

  // manifest entries lost, objects still there: republishing builds a new
  // (different) set but uploads nothing — the bucket's objects win
  writeFileSync(fx.manifest, readFileSync(join(repo, 'hack/helpers.sha256'), 'utf8'));
  fx.git('commit', '-qam', 'drop');
  r = publish(fx, [], { MARK: 'second' });
  assert.match(r.out, /already in the bucket — not overwriting it/);
  assert.equal(readFileSync(fx.manifest, 'utf8'), pinned);
  assert.deepEqual(readFileSync(obj), bytes);

  // --staged: upload what --stage-only built, building nothing
  appendFileSync(join(fx.hack, 'vmkernel/xbin.config'), 'CONFIG_NEW=y\n');
  fx.git('commit', '-qam', 'vm input');
  const stage = join(fx.dir, 'stage');
  assert.equal(fx.run('publish-helpers.sh', ['--stage-only', stage, 'vm'], { MARK: 'staged' }).code, 0);
  writeFileSync(fx.env.BUILD_LOG, '');
  publish(fx, ['--staged', stage, 'vm']);
  assert.equal(fx.built(), '');
  assert.equal(keyOf(fx, 'vm').state, 'published');
  assert.equal(fx.run('fetch-helpers.sh', ['vm']).code, 0);
  assert.equal(readFileSync(join(fx.bin, 'vmlinux'), 'utf8'), 'vmlinux staged\n');

  // a wrong secret: the S3 error, the manifest untouched
  appendFileSync(join(fx.hack, 'gofuse-patches/0001-b.patch'), 'bump\n');
  fx.git('commit', '-qam', 'cfs input');
  fx.secret({ AWS_SECRET_ACCESS_KEY: 'wrong-secret-0000' });
  const before = readFileSync(fx.manifest, 'utf8');
  r = fx.run('publish-helpers.sh', ['containerfs']);
  assert.notEqual(r.code, 0);
  assert.match(r.out, /upload of helpers\/containerfs\/[0-9a-f]{12}\/amd64\.tar\.zst: HTTP 403/);
  assert.equal(readFileSync(fx.manifest, 'utf8'), before);
});

test('large files: over 1 MiB or native binaries fail unless allowlisted with a reason', () => {
  const dir = join(root, `lf${n++}`);
  mkdirSync(join(dir, 'hack'), { recursive: true });
  copyFileSync(join(repo, 'hack/check-large-files.sh'), join(dir, 'hack/check-large-files.sh'));
  writeFileSync(join(dir, 'hack/large-files.allow'), '# nothing\n');
  const env = { PATH: process.env.PATH, HOME: dir, GIT_CONFIG_NOSYSTEM: '1' };
  const git = (...a) => execFileSync('git', a, { cwd: dir, env, stdio: 'pipe' });
  const check = () => {
    try {
      return { out: execFileSync(join(dir, 'hack/check-large-files.sh'), { cwd: dir, env, encoding: 'utf8', stdio: 'pipe' }), code: 0 };
    } catch (e) {
      return { out: `${e.stdout}${e.stderr}`, code: e.status };
    }
  };
  git('init', '-q');
  writeFileSync(join(dir, 'small.txt'), 'hi\n');
  git('add', '-A');
  assert.equal(check().code, 0);

  writeFileSync(join(dir, 'big.txt'), 'x'.repeat(1024 * 1024 + 1));
  git('add', 'big.txt');
  let r = check();
  assert.notEqual(r.code, 0);
  assert.match(r.out, /big\.txt: 1024 KiB \(over 1 MiB\)/);
  git('rm', '-q', '--cached', 'big.txt');
  rmSync(join(dir, 'big.txt'));

  writeFileSync(join(dir, 'tool'), Buffer.concat([Buffer.from([0x7f, 0x45, 0x4c, 0x46, 2, 1, 1, 0]), Buffer.alloc(64)]));
  writeFileSync(join(dir, 'app.exe'), Buffer.concat([Buffer.from('MZ'), Buffer.alloc(64)]));
  writeFileSync(join(dir, 'pic.png'), Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47]), Buffer.alloc(64)]));
  git('add', '-A');
  r = check();
  assert.notEqual(r.code, 0);
  assert.match(r.out, /tool: an ELF binary/);
  assert.match(r.out, /app\.exe: an PE binary/);
  assert.doesNotMatch(r.out, /pic\.png/, 'other binary files are fine');
  // the index is what counts: a work-tree-only change doesn't hide it
  writeFileSync(join(dir, 'tool'), 'now text\n');
  assert.notEqual(check().code, 0);

  writeFileSync(join(dir, 'hack/large-files.allow'), 'tool\n');
  r = check();
  assert.notEqual(r.code, 0);
  assert.match(r.out, /large-files\.allow:1: want/);
  writeFileSync(join(dir, 'hack/large-files.allow'), '# fixtures\ntool   # a test fixture\n*.exe  # windows fixtures\n');
  r = check();
  assert.equal(r.code, 0, r.out);
  assert.match(r.out, /2 allowlisted/);
});
