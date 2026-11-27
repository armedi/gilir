#!/usr/bin/env node

const { spawn } = require('node:child_process');
const { constants } = require('node:os');
const { getBinary } = require('./download.cjs');

async function main() {
  const binary = await getBinary();
  const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit' });
  process.on('SIGINT', () => {
    // On Windows the child receives Ctrl+C from the console directly.
    if (process.platform !== 'win32') child.kill('SIGINT');
  });
  process.on('SIGTERM', () => child.kill('SIGTERM'));
  child.on('error', fail);
  child.on('exit', (code, signal) => {
    process.exitCode = code ?? (128 + constants.signals[signal]);
  });
}

function fail(error) {
  console.error(`gilir: ${error.message}`);
  process.exitCode = 1;
}

main().catch(fail);
