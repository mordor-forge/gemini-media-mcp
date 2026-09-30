// Tests for npm/gemini-media-mcp/bin/gemini-media-mcp.js.
// Run: node --test npm/test/*.test.js
//
// Each test lays out a throwaway node_modules tree with the launcher and a fake
// platform package whose "binary" is a POSIX shell script, then runs the
// launcher the way npx would.
'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const LAUNCHER = path.join(__dirname, '..', 'gemini-media-mcp', 'bin', 'gemini-media-mcp.js');
const skip = process.platform === 'win32' ? 'fake binaries are shell scripts' : false;

function layout({ withPlatformPackage = true, binary } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'gmm-launcher-'));
  const nm = path.join(root, 'node_modules');
  const main = path.join(nm, 'gemini-media-mcp', 'bin');
  fs.mkdirSync(main, { recursive: true });
  fs.writeFileSync(path.join(nm, 'gemini-media-mcp', 'package.json'), '{"name":"gemini-media-mcp"}');
  const launcher = path.join(main, 'gemini-media-mcp.js');
  fs.copyFileSync(LAUNCHER, launcher);
  if (withPlatformPackage) {
    const pkg = path.join(nm, `gemini-media-mcp-${process.platform}-${process.arch}`);
    fs.mkdirSync(path.join(pkg, 'bin'), { recursive: true });
    fs.writeFileSync(path.join(pkg, 'package.json'), '{"name":"platform"}');
    const bin = path.join(pkg, 'bin', 'gemini-media-mcp');
    fs.writeFileSync(bin, binary);
    fs.chmodSync(bin, 0o644); // the launcher must restore the exec bit
  }
  return { root, launcher };
}

function run(launcher, args, env = {}) {
  return spawnSync(process.execPath, [launcher, ...args], {
    input: 'ping\n',
    encoding: 'utf8',
    // PATH without any gemini-media-mcp so the fallback cannot kick in.
    env: { PATH: '/usr/bin:/bin', ...env },
  });
}

test('passes arguments, stdin, stdout and the exit code through', { skip }, () => {
  const { launcher } = layout({
    binary: '#!/bin/sh\nread line\necho "args=$* stdin=$line"\necho "to stderr" >&2\nexit 7\n',
  });
  const res = run(launcher, ['serve', '--log-level', 'debug']);
  assert.equal(res.status, 7);
  assert.equal(res.stdout, 'args=serve --log-level debug stdin=ping\n');
  assert.equal(res.stderr, 'to stderr\n');
});

test('GEMINI_MEDIA_MCP_BINARY overrides the platform package', { skip }, () => {
  const { root, launcher } = layout({ binary: '#!/bin/sh\necho platform\n' });
  const override = path.join(root, 'custom');
  fs.writeFileSync(override, '#!/bin/sh\necho override\n', { mode: 0o755 });
  const res = run(launcher, [], { GEMINI_MEDIA_MCP_BINARY: override });
  assert.equal(res.status, 0);
  assert.equal(res.stdout, 'override\n');
});

test('falls back to a binary on PATH and never recurses into itself', { skip }, () => {
  const { root, launcher } = layout({ withPlatformPackage: false });
  const binDir = path.join(root, 'pathbin');
  fs.mkdirSync(binDir);
  // An npm-style shim pointing back at the launcher must be skipped...
  fs.symlinkSync(launcher, path.join(binDir, 'gemini-media-mcp'));
  let res = run(launcher, [], { PATH: `${binDir}:/usr/bin:/bin` });
  assert.equal(res.status, 1);
  assert.equal(res.stdout, '');
  // ...while a real binary later on PATH is used.
  const realDir = path.join(root, 'realbin');
  fs.mkdirSync(realDir);
  // A compiled binary does not start with "#!"; emulate one with a copy of /bin/true's behaviour.
  fs.copyFileSync('/bin/true', path.join(realDir, 'gemini-media-mcp'));
  fs.chmodSync(path.join(realDir, 'gemini-media-mcp'), 0o755);
  res = run(launcher, [], { PATH: `${binDir}:${realDir}:/usr/bin:/bin` });
  assert.equal(res.status, 0);
  assert.equal(res.stdout, '');
});

test('explains a missing platform package on stderr only', { skip }, () => {
  const { launcher } = layout({ withPlatformPackage: false });
  const res = run(launcher, ['version']);
  assert.equal(res.status, 1);
  assert.equal(res.stdout, '');
  assert.match(res.stderr, /not installed|no prebuilt binary/);
});

test('forwards SIGTERM to the binary and exits with the same signal', { skip }, async () => {
  const { root, launcher } = layout({
    binary: '#!/bin/sh\ntrap \'echo got-term >&2; exit 0\' TERM\necho ready\nwhile :; do sleep 0.1; done\n',
  });
  const child = spawn(process.execPath, [launcher], { cwd: root, env: { PATH: '/usr/bin:/bin' } });
  let stderr = '';
  child.stderr.on('data', (d) => (stderr += d));
  await new Promise((resolve) => child.stdout.once('data', resolve));
  child.kill('SIGTERM');
  const [code, signal] = await new Promise((resolve) => child.on('exit', (c, s) => resolve([c, s])));
  assert.match(stderr, /got-term/);
  // The fake binary handled TERM and exited 0, so the launcher exits 0 too.
  assert.equal(signal, null);
  assert.equal(code, 0);
});

test('re-raises the signal that killed the binary', { skip }, async () => {
  const { root, launcher } = layout({ binary: '#!/bin/sh\necho ready\nexec sleep 30\n' });
  const child = spawn(process.execPath, [launcher], { cwd: root, env: { PATH: '/usr/bin:/bin' } });
  await new Promise((resolve) => child.stdout.once('data', resolve));
  child.kill('SIGTERM');
  const [code, signal] = await new Promise((resolve) => child.on('exit', (c, s) => resolve([c, s])));
  assert.equal(code, null);
  assert.equal(signal, 'SIGTERM');
});
