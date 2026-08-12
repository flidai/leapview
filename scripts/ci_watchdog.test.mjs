import assert from 'node:assert/strict'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'

import { parseArguments, runWithWatchdog } from './ci_watchdog.mjs'

test('parseArguments requires bounded positive settings and a command separator', () => {
  assert.deepEqual(
    parseArguments(['--timeout-seconds', '30', '--attempts', '2', '--grace-seconds', '3', '--', 'task', 'ci:lane:frontend']),
    {
      attempts: 2,
      command: 'task',
      commandArguments: ['ci:lane:frontend'],
      graceMilliseconds: 3_000,
      timeoutMilliseconds: 30_000,
    },
  )

  assert.throws(() => parseArguments(['--timeout-seconds', '0', '--', 'task']), /positive integer/)
  assert.throws(() => parseArguments(['--timeout-seconds', '30', 'task']), /command separator/)
})

test('runWithWatchdog returns a successful command without retrying it', async () => {
  const result = await runWithWatchdog({
    attempts: 2,
    command: process.execPath,
    commandArguments: ['-e', 'process.exit(0)'],
    graceMilliseconds: 100,
    timeoutMilliseconds: 2_000,
  })

  assert.equal(result.attempt, 1)
  assert.equal(result.code, 0)
  assert.equal(result.timedOut, false)
})

test('runWithWatchdog does not retry an ordinary command failure', async () => {
  const result = await runWithWatchdog({
    attempts: 2,
    command: process.execPath,
    commandArguments: ['-e', 'process.exit(23)'],
    graceMilliseconds: 100,
    timeoutMilliseconds: 2_000,
  })

  assert.equal(result.attempt, 1)
  assert.equal(result.code, 23)
  assert.equal(result.timedOut, false)
})

test('runWithWatchdog diagnoses a timed-out process group and retries once', async (t) => {
  if (process.platform === 'win32') {
    t.skip('POSIX process-group signals are required by the GitHub-hosted runner watchdog')
    return
  }

  const directory = await mkdtemp(path.join(tmpdir(), 'leapview-ci-watchdog-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  const counterPath = path.join(directory, 'attempts')
  const child = String.raw`
    const fs = require('node:fs');
    const counterPath = process.argv[1];
    let attempt = 1;
    try { attempt = Number(fs.readFileSync(counterPath, 'utf8')) + 1; } catch {}
    fs.writeFileSync(counterPath, String(attempt));
    if (attempt === 1) {
      process.on('SIGQUIT', () => process.exit(131));
      setInterval(() => {}, 1_000);
    }
  `

  const result = await runWithWatchdog({
    attempts: 2,
    command: process.execPath,
    commandArguments: ['-e', child, counterPath],
    graceMilliseconds: 500,
    // Keep the integration deadline comfortably above process startup under
    // the PR contract's concurrent Go compilation load. The child itself is
    // deterministic: attempt one waits forever and attempt two exits at once.
    timeoutMilliseconds: 2_000,
  })

  assert.equal(await readFile(counterPath, 'utf8'), '2')
  assert.equal(result.attempt, 2)
  assert.equal(result.code, 0)
  assert.equal(result.timedOut, false)
})

test('runWithWatchdog sends diagnostics to descendants before force-killing the process group', async (t) => {
  if (process.platform === 'win32') {
    t.skip('POSIX process-group signals are required by the GitHub-hosted runner watchdog')
    return
  }

  const directory = await mkdtemp(path.join(tmpdir(), 'leapview-ci-watchdog-group-'))
  t.after(() => rm(directory, { recursive: true, force: true }))
  const diagnosticPath = path.join(directory, 'diagnostic')
  const grandchild = String.raw`
    const fs = require('node:fs');
    process.on('SIGQUIT', () => {
      fs.writeFileSync(process.argv[1], 'received SIGQUIT');
      process.exit(0);
    });
    setInterval(() => {}, 1_000);
  `
  const parent = String.raw`
    const { spawn } = require('node:child_process');
    process.on('SIGQUIT', () => {});
    spawn(process.execPath, ['-e', process.argv[1], process.argv[2]], { stdio: 'inherit' });
    setInterval(() => {}, 1_000);
  `

  const result = await runWithWatchdog({
    attempts: 1,
    command: process.execPath,
    commandArguments: ['-e', parent, grandchild, diagnosticPath],
    graceMilliseconds: 500,
    timeoutMilliseconds: 2_000,
  })

  assert.equal(await readFile(diagnosticPath, 'utf8'), 'received SIGQUIT')
  assert.equal(result.code, 124)
  assert.equal(result.timedOut, true)
})
