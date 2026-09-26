// hack/install-sh.test.mjs — unit tests for deploy/install.sh's host-editing
// helpers, run by `make js-test` (part of `make check`): node's built-in
// runner plus bash, no root, nothing outside a temp dir. The installer is
// one script that acts on the host as soon as it runs, so the functions
// under test are cut out of it by name and driven against temp files:
//   - the AppArmor block in /etc/apparmor.d/local/fusermount3 (D110):
//     written, replaced in place (the hand-written QA-box block included),
//     never duplicated, removed when the stock profile covers the workspace,
//     other lines kept, a hand-broken file left alone, paths escaped;
//   - the VM policy it writes for a never-configured workspace (D110):
//     xbind's JSON field names, never an existing file, backends only on KVM.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync, existsSync, mkdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..');
const src = readFileSync(join(repo, 'deploy/install.sh'), 'utf8').split('\n');

// fn(name): the text of a top-level function — one line when it closes on
// its own line, else up to the first column-0 "}".
function fn(name) {
  const re = new RegExp(`^${name}\\(\\)\\s*\\{`);
  const i = src.findIndex((l) => re.test(l));
  assert.ok(i >= 0, `install.sh has no function ${name}`);
  if (src[i].trimEnd().endsWith('}')) return src[i];
  const j = src.findIndex((l, k) => k > i && l === '}');
  return src.slice(i, j + 1).join('\n');
}
const assign = (name) => {
  const l = src.find((x) => x.startsWith(`${name}=`));
  assert.ok(l, `install.sh sets no ${name}`);
  return l;
};

const lib = [
  'set -euo pipefail',
  ...['ok', 'warn', 'have'].map(fn),
  assign('AA_BEGIN'), assign('AA_END'),
  ...['aa_path_escape', 'aa_fuse_block', 'aa_block_of', 'aa_markers_ok', 'aa_replace_block',
    'aa_fuse_covered', 'aa_fuse_plan', 'aa_fuse_manual', 'aa_fuse_warn',
    'vm_policy_file', 'vm_have', 'vm_emulation_ok', 'vm_policy_json', 'setup_vm_policy'].map(fn),
].join('\n');

const dir = mkdtempSync(join(tmpdir(), 'xbin-install-test-'));
process.on('exit', () => rmSync(dir, { recursive: true, force: true }));

// sh(script, env): run lib + script in bash; returns {out, code}.
function sh(script, env = {}) {
  try {
    const out = execFileSync('bash', ['-c', `${lib}\n${script}`], {
      env: { PATH: process.env.PATH, B: '', R: '', G: '', Y: '', RED: '', C: '', ...env },
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
    });
    return { out, code: 0 };
  } catch (e) {
    return { out: `${e.stdout}${e.stderr}`, code: e.status };
  }
}

// The stock Ubuntu 26.04 profile's shape: the rules aa_fuse_covered reads,
// and the include the block rides on.
const profile = (include = true) => `abi <abi/5.0>,
include <tunables/global>
@{fuse_types} = {fuse,fuse.*,fuseblk,fusectl}
profile fusermount3 /usr/bin/fusermount3 {
  mount fstype=@{fuse_types} options=(nosuid,nodev) options in (ro,rw,noatime,dirsync,nodiratime,noexec,sync) -> @{HOME}/**/,
  mount fstype=@{fuse_types} options=(nosuid,nodev) options in (ro,rw,noatime,dirsync,nodiratime,noexec,sync) -> /mnt/{,**/},
  mount fstype=@{fuse_types} options=(nosuid,nodev) options in (ro,rw,noatime,dirsync,nodiratime,noexec,sync) -> /tmp/**/,
  umount @{HOME}/**/,
  umount /mnt/{,**/},
  umount /tmp/**/,
${include ? '  include if exists <local/fusermount3>\n' : ''}}
`;

// What was put on the QA box by hand before the installer knew how.
const handBlock = `# BEGIN xbin (install.sh) — encrypted resources (gocryptfs) under the workspace
mount fstype=@{fuse_types} options=(nosuid,nodev) options in (ro,rw,noatime,dirsync,nodiratime,noexec,sync) -> /opt/xbin/workspace/.xbin/resenc/**/,
umount /opt/xbin/workspace/.xbin/resenc/**/,
# END xbin
`;

const block = (d) => sh(`aa_fuse_block '${d}'`).out;
let n = 0;
const file = (text) => {
  const p = join(dir, `f${n++}`);
  if (text !== null) writeFileSync(p, text);
  return p;
};
// replace(localText|null, blockText) → the new local file text.
const replace = (local, blk) => sh(`aa_replace_block '${file(local)}' '${file(blk)}'`).out;

test('the block: quoted path, the stock rule shape, the markers', () => {
  const b = block('/opt/xbin/workspace/.xbin/resenc');
  assert.equal(b, `# BEGIN xbin (install.sh) — encrypted resources (gocryptfs) under the workspace
mount fstype=@{fuse_types} options=(nosuid,nodev) options in (ro,rw,noatime,dirsync,nodiratime,noexec,sync) -> "/opt/xbin/workspace/.xbin/resenc/**/",
umount "/opt/xbin/workspace/.xbin/resenc/**/",
# END xbin
`);
});

test('paths are escaped for AppArmor; control characters are refused', () => {
  assert.equal(sh(`aa_path_escape '/srv/my ws/a[b]{c}*d?e^f"g\\h@x'`).out,
    '/srv/my ws/a\\[b\\]\\{c\\}\\*d\\?e\\^f\\"g\\\\h\\@x');
  assert.equal(sh('aa_path_escape "/opt/xbin/workspace"').out, '/opt/xbin/workspace');
  assert.notEqual(sh(`aa_path_escape $'/srv/a\\nb'`).code, 0);
  assert.notEqual(sh(`aa_fuse_block $'/srv/a\\tb'`).code, 0);
});

test('replace: a missing or empty file gets just the block', () => {
  const b = block('/opt/xbin/workspace/.xbin/resenc');
  assert.equal(replace(null, b), b);
  assert.equal(replace('', b), b);
});

test('replace: other lines are kept, the block is appended once', () => {
  const b = block('/opt/xbin/workspace/.xbin/resenc');
  const admin = '# site rule\n/srv/fuse/** rw,\n';
  const once = replace(admin, b);
  assert.equal(once, admin + b);
  assert.equal(replace(once, b), once, 'idempotent');
  // a last line without its newline still ends before the block
  assert.equal(replace('/srv/x r,', b), `/srv/x r,\n${b}`);
});

test('replace: the hand-written QA-box block is replaced in place, not duplicated', () => {
  const b = block('/opt/xbin/workspace/.xbin/resenc');
  const out = replace(`# before\n${handBlock}# after\n`, b);
  assert.equal(out, `# before\n${b}# after\n`);
  assert.equal(out.split('# BEGIN xbin').length, 2);
});

test('replace: duplicated blocks collapse into one, at the first', () => {
  const b = block('/w/.xbin/resenc');
  assert.equal(replace(`a\n${handBlock}b\n${handBlock}c\n`, b), `a\n${b}b\nc\n`);
});

test('replace: an empty block removes it and keeps the rest', () => {
  assert.equal(replace(`a\n${handBlock}b\n`, ''), 'a\nb\n');
});

test('markers: balanced pairs pass; a lone or nested marker fails', () => {
  const ok = (t) => sh(`aa_markers_ok '${file(t)}'`).code === 0;
  assert.ok(ok(''));
  assert.ok(ok(`x\n${handBlock}${handBlock}`));
  assert.ok(sh(`aa_markers_ok '${join(dir, 'missing')}'`).code === 0);
  assert.ok(!ok('# BEGIN xbin (install.sh)\nmount -> /x/,\n'));
  assert.ok(!ok('mount -> /x/,\n# END xbin\n'));
  assert.ok(!ok('# BEGIN xbin (install.sh)\n# BEGIN xbin (install.sh)\n# END xbin\n'));
});

// plan(workspace, {profile, local, enabled}) → "STATE COVERED DIR"
function plan(ws, { prof = profile(), local = null, enabled = true } = {}) {
  const p = file(prof);
  const l = file(local);
  const r = sh(`aa_enabled() { ${enabled ? 'true' : 'false'}; }
AA_PROFILE='${p}' AA_LOCAL='${l}' WORKSPACE='${ws}'
aa_fuse_plan; echo "$AA_STATE $AA_COVERED $AA_DIR"`);
  assert.equal(r.code, 0, r.out);
  return r.out.trim();
}

test('plan: /opt needs the block; written, it is current', () => {
  assert.equal(plan('/opt/xbin/workspace'), 'write 0 /opt/xbin/workspace/.xbin/resenc');
  const b = block('/opt/xbin/workspace/.xbin/resenc');
  assert.equal(plan('/opt/xbin/workspace', { local: `x\n${b}` }), 'current 0 /opt/xbin/workspace/.xbin/resenc');
  // the hand-written block differs (unquoted) → rewritten once
  assert.equal(plan('/opt/xbin/workspace', { local: handBlock }), 'write 0 /opt/xbin/workspace/.xbin/resenc');
  // the workspace moved → the old block is replaced
  assert.equal(plan('/srv/ws', { local: b }), 'write 0 /srv/ws/.xbin/resenc');
});

test('plan: a workspace the stock profile already allows needs no block', () => {
  assert.equal(plan('/home/xbin/ws'), 'current 1 /home/xbin/ws/.xbin/resenc');
  assert.equal(plan('/tmp/ws'), 'current 1 /tmp/ws/.xbin/resenc');
  assert.equal(plan('/mnt/data/ws'), 'current 1 /mnt/data/ws/.xbin/resenc');
  // a stale block from an earlier path is dropped
  assert.equal(plan('/home/xbin/ws', { local: handBlock }), 'write 1 /home/xbin/ws/.xbin/resenc');
  // /media is allowed by the real profile, not by this one: checked, not assumed
  assert.equal(plan('/media/ws'), 'write 0 /media/ws/.xbin/resenc');
});

test('plan: skips cleanly, or says why it cannot write', () => {
  assert.equal(plan('/opt/xbin/workspace', { enabled: false }), 'skip 0');
  assert.equal(sh(`aa_enabled() { true; }
AA_PROFILE='${join(dir, 'no-profile')}' WORKSPACE=/opt/w; aa_fuse_plan; echo "$AA_STATE"`).out.trim(), 'skip');
  assert.equal(plan('/opt/xbin/workspace', { prof: profile(false) }), 'noinclude 0 /opt/xbin/workspace/.xbin/resenc');
  assert.equal(plan('/opt/xbin/workspace', { local: '# BEGIN xbin (install.sh)\nmount -> /x/,\n' }),
    'badmarks 0 /opt/xbin/workspace/.xbin/resenc');
});

test('warnings print root commands that work when pasted', () => {
  const p = file(profile());
  const local = file(null);
  const r = sh(`aa_enabled() { true; }
AA_PROFILE='${p}' AA_LOCAL='${local}' WORKSPACE=/srv/ws
aa_fuse_plan; aa_fuse_warn`);
  assert.equal(r.code, 0, r.out);
  // the commands as printed, from "sudo tee" to the reload, run without sudo
  const cmds = r.out.slice(r.out.indexOf('sudo tee'), r.out.indexOf('\n', r.out.indexOf('sudo apparmor_parser')));
  assert.match(cmds, new RegExp(`apparmor_parser -r ${p.replace(/[/.]/g, '\\$&')}$`));
  execFileSync('bash', ['-c', `apparmor_parser() { :; }\n${cmds.replace(/^sudo /gm, '')}`]);
  assert.equal(readFileSync(local, 'utf8'), block('/srv/ws/.xbin/resenc'));
});

test('the VM policy: xbind\'s field names, terminals on, backends only on KVM', () => {
  const policyGo = readFileSync(join(repo, 'internal/vm/policy.go'), 'utf8');
  for (const b of ['true', 'false']) {
    const j = JSON.parse(sh(`vm_policy_json ${b}`).out);
    assert.deepEqual(j, { terminals: true, backends: b === 'true' });
    for (const k of Object.keys(j)) assert.match(policyGo, new RegExp(`json:"${k}"`), `policy.go has no field ${k}`);
  }
});

// vm(setup): run setup_vm_policy in user mode against a temp prefix/workspace;
// kvm decides vm_kvm_ok (the host's /dev/kvm must not leak into the test).
function vm({ assets = [], kvm = false, existing = null } = {}) {
  const root = join(dir, `vm${n++}`);
  mkdirSync(join(root, 'bin'), { recursive: true });
  for (const a of assets) writeFileSync(join(root, 'bin', a), '');
  const ws = join(root, 'workspace');
  const f = join(ws, '.xbin/vm/policy.json');
  if (existing !== null) { mkdirSync(dirname(f), { recursive: true }); writeFileSync(f, existing); }
  const r = sh(`vm_kvm_ok() { ${kvm ? 'true' : 'false'}; }
MODE=user PREFIX='${root}' WORKSPACE='${ws}'; setup_vm_policy`);
  assert.equal(r.code, 0, r.out);
  return { out: r.out, policy: existsSync(f) ? readFileSync(f, 'utf8') : null };
}
const common = ['xbin-vmagent', 'vmlinux', 'mkfs.erofs'];
const emu = ['qemu-system-x86_64', 'qemu-bios-microvm.bin', 'qemu-pvh.bin', 'vhost-device-vsock'];

test('VM policy: written when never configured', () => {
  assert.deepEqual(JSON.parse(vm({ assets: [...common, 'firecracker'], kvm: true }).policy), { terminals: true, backends: true });
  // no usable KVM: emulated VMs — terminals only
  assert.deepEqual(JSON.parse(vm({ assets: [...common, ...emu] }).policy), { terminals: true, backends: false });
});

test('VM policy: an existing file is never touched, "off" included', () => {
  const off = '{\n  "terminals": false,\n  "backends": false\n}';
  const r = vm({ assets: [...common, 'firecracker'], kvm: true, existing: off });
  assert.equal(r.policy, off);
  assert.match(r.out, /already set .* never rewritten/);
});

test('VM policy: nothing without the VM pieces', () => {
  assert.equal(vm({ assets: [] }).policy, null);
  assert.equal(vm({ assets: [...common] }).policy, null, 'neither firecracker+KVM nor emulation');
  assert.equal(vm({ assets: ['firecracker', ...emu], kvm: true }).policy, null, 'no guest kernel/agent/mkfs');
});

test('KVM usability is an open of /dev/kvm, never `test -r/-w`', () => {
  // Ubuntu 26.04's /usr/bin/test (uutils) ignores supplementary groups, so
  // `runuser -u xbin -- test -r /dev/kvm` said "no" for a kvm-group member
  // and the QA box got backends off. Open it the way internal/vm does.
  const f = fn('vm_kvm_ok');
  assert.match(f, /<>\/dev\/kvm/);
  assert.doesNotMatch(f, /\btest -[rw]\b/);
});
