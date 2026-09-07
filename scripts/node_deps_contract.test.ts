import { expect, test } from 'bun:test'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { watch } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parse, stringify } from 'yaml'

type Task = Record<string, unknown>

const repoRoot = resolve(import.meta.dir, '..')
const taskBinary = 'task'
const handshakeTimeoutMs = 5_000
const taskTimeoutMs = 10_000
const installerTimeoutMs = 8_000

async function waitFor(predicate: () => boolean | Promise<boolean>, watchedDirectory: string, description: string): Promise<void> {
  await new Promise<void>((resolvePromise, rejectPromise) => {
    let settled = false
    let timeout: ReturnType<typeof setTimeout>
    const watcher = watch(watchedDirectory, () => {
      void check()
    })
    const finish = (error?: unknown) => {
      if (settled) return
      settled = true
      watcher.close()
      clearTimeout(timeout)
      if (error) rejectPromise(error)
      else resolvePromise()
    }
    const check = async () => {
      try {
        if (await predicate()) finish()
      } catch (error) {
        finish(error)
      }
    }
    watcher.on('error', finish)
    timeout = setTimeout(() => {
      finish(new Error(`timed out waiting for ${description}`))
    }, handshakeTimeoutMs)
    // Register the watcher before checking so a marker created between the
    // initial check and watch setup cannot be missed.
    void check()
  })
}

async function readLines(path: string): Promise<string[]> {
  try {
    return (await readFile(path, 'utf8')).split('\n').filter(Boolean)
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return []
    throw error
  }
}

async function withFixture<T>(run: (fixture: string) => Promise<T>): Promise<T> {
  const fixture = await mkdtemp(join(tmpdir(), 'leapview-node-deps-contract-'))
  try {
    return await run(fixture)
  } finally {
    await rm(fixture, { recursive: true, force: true })
  }
}

async function writeFixture(fixture: string): Promise<void> {
  const taskfile = parse(await readFile(join(repoRoot, 'Taskfile.yml'), 'utf8')) as { tasks: Record<string, Task> }
  const nodeDeps = structuredClone(taskfile.tasks['node:deps']) as Task
  const fakeInstaller = join(fixture, 'fake-installer.ts')
  const consumer = join(fixture, 'consumer.ts')
  const installCommand = `bun "${fakeInstaller}"`

  nodeDeps.cmds = [installCommand]

  await writeFile(fakeInstaller, `
import { appendFile, mkdir, writeFile } from 'node:fs/promises'
import { existsSync, watch } from 'node:fs'
import { join } from 'node:path'

const root = process.env.FIXTURE_ROOT!
const release = join(root, 'release')
const installLog = join(root, 'install.log')
const sentinel = join(root, 'node_modules', '.bin', 'esbuild')
const complete = join(root, 'install-complete')
const deadline = setTimeout(() => process.exit(124), ${installerTimeoutMs})

await appendFile(installLog, \`\${process.pid}\\n\`)
await writeFile(join(root, 'install-started'), 'started\\n')
if (process.env.NODE_DEPS_FAIL === '1') process.exit(23)

if (!existsSync(release)) {
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const watcher = watch(root, (_event, filename) => {
      if (String(filename) !== 'release' || !existsSync(release)) return
      watcher.close()
      resolvePromise()
    })
    watcher.on('error', rejectPromise)
    // Check again after registering the watcher to close the exists/watch race.
    if (existsSync(release)) {
      watcher.close()
      resolvePromise()
    }
  })
}

await mkdir(join(root, 'node_modules', '.bin'), { recursive: true })
await writeFile(sentinel, 'installed\\n')
await writeFile(complete, 'complete\\n')
clearTimeout(deadline)
`)
  await writeFile(consumer, `
import { appendFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { join } from 'node:path'

const root = process.env.FIXTURE_ROOT!
if (!existsSync(join(root, 'install-complete'))) process.exit(41)
await appendFile(join(root, 'consumers.log'), \`\${process.argv[2]}\\n\`)
`)
  await writeFile(join(fixture, 'package.json'), '{}\n')
  await writeFile(join(fixture, 'bun.lock'), '{}\n')
  await writeFile(join(fixture, 'Taskfile.yml'), stringify({
    version: '3',
    tasks: {
      'node:deps': nodeDeps,
      'visualization-ir:generate': {
        deps: ['node:deps'],
        cmds: [`bun "${consumer}" visualization`],
      },
      'dashboard-contracts:generate': {
        deps: ['node:deps', 'visualization-ir:generate'],
        cmds: [`bun "${consumer}" dashboard`],
      },
    },
  }))
}

type SpawnOptions = { env?: Record<string, string>; force?: boolean }

function spawnTask(fixture: string, taskName: string, options: SpawnOptions = {}) {
  const args = [taskBinary]
  if (options.force) args.push('--force')
  args.push('--dir', fixture, taskName)
  return Bun.spawn(args, {
    cwd: fixture,
    env: { ...Bun.env, FIXTURE_ROOT: fixture, ...options.env },
    stdout: 'pipe',
    stderr: 'pipe',
  })
}

async function collectTask(taskProcess: ReturnType<typeof spawnTask>) {
  // A failed handshake must not leave a Task subprocess (or its pipes) open.
  const watchdog = setTimeout(() => taskProcess.kill(), taskTimeoutMs)
  try {
    const [exitCode, stdout, stderr] = await Promise.all([
      taskProcess.exited,
      new Response(taskProcess.stdout).text(),
      new Response(taskProcess.stderr).text(),
    ])
    return { exitCode, stdout, stderr }
  } finally {
    clearTimeout(watchdog)
  }
}

async function runTask(fixture: string, taskName: string, extraEnv: Record<string, string> = {}) {
  return collectTask(spawnTask(fixture, taskName, { env: extraEnv }))
}

test('node:deps runs once and gates both diamond consumers until install completes', async () => {
  await withFixture(async (fixture) => {
    await writeFixture(fixture)
    // Force the graph so checksum/generate metadata cannot hide a second
    // installer if the first branch happens to finish before the other starts.
    const taskProcess = spawnTask(fixture, 'dashboard-contracts:generate', { force: true })
    const taskResult = collectTask(taskProcess)

    let result: Awaited<typeof taskResult>
    try {
      await waitFor(async () => (await readLines(join(fixture, 'install.log'))).length > 0, fixture, 'installer start')
      expect(await readLines(join(fixture, 'consumers.log'))).toEqual([])
    } finally {
      await writeFile(join(fixture, 'release'), 'release\n')
      result = await taskResult
    }

    expect(result).toMatchObject({ exitCode: 0 })
    expect(await readLines(join(fixture, 'install.log'))).toHaveLength(1)
    expect(await readLines(join(fixture, 'consumers.log'))).toEqual(['visualization', 'dashboard'])
  })
}, { timeout: taskTimeoutMs + 5_000 })

test('node:deps failure is nonzero and prevents both diamond consumers', async () => {
  await withFixture(async (fixture) => {
    await writeFixture(fixture)
    const result = await runTask(fixture, 'dashboard-contracts:generate', { NODE_DEPS_FAIL: '1' })
    expect(result.exitCode).not.toBe(0)
    expect(await readLines(join(fixture, 'consumers.log'))).toEqual([])
  })
}, { timeout: taskTimeoutMs + 5_000 })

test('node:deps revalidates on a separate Task invocation when installation files exist', async () => {
  await withFixture(async (fixture) => {
    await writeFixture(fixture)
    const first = spawnTask(fixture, 'node:deps')
    const firstResult = collectTask(first)
    let firstOutcome: Awaited<typeof firstResult>
    try {
      await waitFor(async () => (await readLines(join(fixture, 'install.log'))).length > 0, fixture, 'first installer start')
    } finally {
      await writeFile(join(fixture, 'release'), 'release\n')
      firstOutcome = await firstResult
    }
    expect(firstOutcome.exitCode).toBe(0)
    expect(await readLines(join(fixture, 'install.log'))).toHaveLength(1)
    // This is the real task's historical generated sentinel; its existence
    // must not suppress a fresh install on a later Task invocation.
    // Even the old freshness sentinel must not bypass a new frozen verification.
    expect(await readLines(join(fixture, 'node_modules', '.bin', 'esbuild'))).toEqual(['installed'])

    const second = await runTask(fixture, 'node:deps')
    expect(second.exitCode).toBe(0)
    expect(await readLines(join(fixture, 'install.log'))).toHaveLength(2)
  })
}, { timeout: taskTimeoutMs + 5_000 })
