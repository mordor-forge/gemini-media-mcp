#!/usr/bin/env node
// Launcher for the gemini-media-mcp Go binary.
//
// npm installs exactly one of the gemini-media-mcp-<os>-<cpu> optional
// dependencies (selected by their "os"/"cpu" fields); this script finds its
// binary and runs it with the same arguments and stdio, forwarding signals and
// the exit status. It never writes to stdout: in MCP stdio mode stdout carries
// the JSON-RPC stream, so every diagnostic goes to stderr.
//
// Lookup order:
//   1. GEMINI_MEDIA_MCP_BINARY (explicit override)
//   2. the platform package for process.platform/process.arch
//   3. a gemini-media-mcp executable on PATH (e.g. from a release archive)
'use strict';

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const NAME = 'gemini-media-mcp';
const EXE = process.platform === 'win32' ? `${NAME}.exe` : NAME;
const RELEASES = 'https://github.com/mordor-forge/gemini-media-mcp/releases';
// Set in the child's environment so a PATH lookup can never re-enter this launcher.
const GUARD = 'GEMINI_MEDIA_MCP_LAUNCHER';

const PLATFORM_PACKAGES = {
  'darwin-arm64': `${NAME}-darwin-arm64`,
  'darwin-x64': `${NAME}-darwin-x64`,
  'linux-arm64': `${NAME}-linux-arm64`,
  'linux-x64': `${NAME}-linux-x64`,
  'win32-arm64': `${NAME}-win32-arm64`,
  'win32-x64': `${NAME}-win32-x64`,
};

function log(message) {
  process.stderr.write(`${NAME}: ${message}\n`);
}

function isFile(p) {
  try {
    return fs.statSync(p).isFile();
  } catch {
    return false;
  }
}

function realpath(p) {
  try {
    return fs.realpathSync(p);
  } catch {
    return p;
  }
}

// Package managers sometimes drop the executable bit; restore it when we can.
function ensureExecutable(bin) {
  if (process.platform === 'win32') return;
  try {
    fs.accessSync(bin, fs.constants.X_OK);
  } catch {
    try {
      fs.chmodSync(bin, 0o755);
    } catch {
      // Read-only install: spawn reports EACCES with a clear message below.
    }
  }
}

function fromOverride() {
  const bin = process.env.GEMINI_MEDIA_MCP_BINARY;
  if (!bin) return null;
  if (isFile(bin)) return bin;
  log(`GEMINI_MEDIA_MCP_BINARY=${bin} does not exist; ignoring it`);
  return null;
}

function fromPlatformPackage() {
  const pkg = PLATFORM_PACKAGES[`${process.platform}-${process.arch}`];
  if (!pkg) return null;
  let manifest;
  try {
    manifest = require.resolve(`${pkg}/package.json`);
  } catch {
    return null;
  }
  const bin = path.join(path.dirname(manifest), 'bin', EXE);
  return isFile(bin) ? bin : null;
}

function fromPath() {
  if (process.env[GUARD]) return null;
  const self = realpath(__filename);
  for (const dir of (process.env.PATH || '').split(path.delimiter)) {
    if (!dir) continue;
    const candidate = path.join(dir, EXE);
    if (!isFile(candidate) || realpath(candidate) === self) continue;
    // Skip npm/npx shims that point back at a copy of this script.
    if (process.platform !== 'win32') {
      try {
        const fd = fs.openSync(candidate, 'r');
        const head = Buffer.alloc(2);
        fs.readSync(fd, head, 0, 2, 0);
        fs.closeSync(fd);
        if (head.toString() === '#!') continue;
      } catch {
        continue;
      }
    }
    return candidate;
  }
  return null;
}

function explainMissing() {
  const target = `${process.platform}-${process.arch}`;
  const pkg = PLATFORM_PACKAGES[target];
  if (!pkg) {
    log(`no prebuilt binary for ${target}. Supported: ${Object.keys(PLATFORM_PACKAGES).join(', ')}.`);
    log(`Build from source with: go install github.com/mordor-forge/gemini-media-mcp/cmd/gemini-media-mcp@latest`);
  } else {
    log(`the platform package ${pkg} is not installed.`);
    log('It is an optional dependency; installs with --omit=optional / --no-optional skip it.');
    log(`Reinstall without that flag, run "npm install ${pkg}", or download a binary from ${RELEASES}`);
  }
  log('and put it on PATH or point GEMINI_MEDIA_MCP_BINARY at it.');
}

function main() {
  const bin = fromOverride() || fromPlatformPackage() || fromPath();
  if (!bin) {
    explainMissing();
    process.exit(1);
  }
  ensureExecutable(bin);

  const child = spawn(bin, process.argv.slice(2), {
    stdio: 'inherit',
    windowsHide: true,
    env: { ...process.env, [GUARD]: '1' },
  });

  const signals = ['SIGINT', 'SIGTERM', 'SIGHUP', 'SIGQUIT', 'SIGBREAK'];
  const forwarders = new Map();
  for (const signal of signals) {
    const forward = () => {
      if (child.exitCode === null && child.signalCode === null) child.kill(signal);
    };
    try {
      process.on(signal, forward);
      forwarders.set(signal, forward);
    } catch {
      // Signal not supported on this platform.
    }
  }

  child.on('error', (err) => {
    log(`failed to start ${bin}: ${err.message}`);
    process.exit(1);
  });

  child.on('exit', (code, signal) => {
    for (const [sig, forward] of forwarders) process.removeListener(sig, forward);
    if (signal) {
      // Re-raise so our parent sees the same termination signal.
      try {
        process.kill(process.pid, signal);
        return;
      } catch {
        process.exit(1);
      }
    }
    process.exit(code === null ? 1 : code);
  });
}

main();
