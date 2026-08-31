import { mkdir, readFile, rm } from 'node:fs/promises'

const portFile = '.tmp/dev-server.port'
const qaHome = '.tmp/qa-ui-framework/home'
const managedServerReadyAttempts = 1800
let startedServer = false
let cleanedUp = false
let devTask: Bun.Subprocess | null = null
let devTaskExitCode: number | null = null

for (const signal of ['SIGINT', 'SIGTERM'] as const) {
  process.on(signal, () => {
    void cleanup().finally(() => process.exit(signal === 'SIGINT' ? 130 : 143))
  })
}

try {
  await main()
} catch (error) {
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
}

async function main(): Promise<void> {
  try {
    const baseURL = await resolveBaseURL()
    const qaScope = Bun.env.LEAPVIEW_UI_QA_SCOPE?.trim() || 'all'
    if (qaScope !== 'all' && qaScope !== 'visual') {
      throw new Error(`Unsupported LEAPVIEW_UI_QA_SCOPE=${JSON.stringify(qaScope)}; expected "all" or "visual"`)
    }
    if (qaScope === 'all') {
      await run(['bun', 'run', 'qa:datastar-lit-routes'], { LEAPVIEW_BASE_URL: baseURL })
    }
    const visualCommand = [
      'bun',
      'x',
      'playwright',
      'test',
      '--config',
      'scripts/playwright.visual.config.ts',
    ]
    const visualEnv = { LEAPVIEW_BASE_URL: baseURL }
    if (Bun.env.LEAPVIEW_UPDATE_VISUAL_BASELINES === '1') {
      await run([...visualCommand, '--update-snapshots'], visualEnv)
    }
    await run(visualCommand, visualEnv)
  } finally {
    await cleanup()
  }
}

async function resolveBaseURL(): Promise<string> {
  const configured = normalizeBaseURL(Bun.env.LEAPVIEW_BASE_URL)
  if (configured) return configured

  const current = await managedBaseURL()
  if (current && await reachable(current)) return current

  startedServer = true
  await prepareManagedHome()
  devTask = spawn(['task', 'dev'], {
    LEAPVIEW_DEV_LOG_LINES: '0',
    LEAPVIEW_DEV_READY_ATTEMPTS: String(managedServerReadyAttempts),
    LEAPVIEW_DEV_SKIP_PUBLISH: '1',
    LEAPVIEW_HOME: qaHome,
    LEAPVIEW_MANAGED_DATA_DIR: `${qaHome}/managed-data`,
    LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES: '67108864',
  }, 'ignore')
  void devTask.exited.then((code) => {
    devTaskExitCode = code
  })

  const started = await waitForManagedServer()
  await deployManagedProject()
  await waitForReachable(started)
  return started
}

async function prepareManagedHome(): Promise<void> {
  await removeManagedHome()
  await mkdir(qaHome, { recursive: true })
}

async function removeManagedHome(): Promise<void> {
  const chmod = spawn(['chmod', '-R', 'u+w', qaHome], {}, 'ignore')
  await chmod.exited
  await rm(qaHome, { recursive: true, force: true })
}

async function deployManagedProject(): Promise<void> {
  const command = ['task', 'dev:publish']
  let lastError: unknown
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      await run(command)
      return
    } catch (error) {
      lastError = error
      if (attempt < 3) await sleep(1000)
    }
  }
  throw lastError
}

async function waitForManagedServer(): Promise<string> {
  for (let attempt = 0; attempt < managedServerReadyAttempts; attempt++) {
    const baseURL = await managedBaseURL()
    if (baseURL && await reachable(baseURL)) return baseURL
    if (devTaskExitCode !== null) {
      throw new Error(`task dev exited before the managed server became reachable with status ${devTaskExitCode}`)
    }
    await sleep(200)
  }
  throw new Error('managed dev server did not become reachable')
}

async function waitForReachable(baseURL: string): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (await reachable(baseURL)) return
    await sleep(200)
  }
  throw new Error(`managed dev server did not become reachable after deployment at ${baseURL}`)
}

async function managedBaseURL(): Promise<string | null> {
  try {
    const port = (await readFile(portFile, 'utf8')).trim()
    if (!/^\d+$/.test(port)) return null
    return `http://localhost:${port}`
  } catch {
    return null
  }
}

async function reachable(baseURL: string): Promise<boolean> {
  try {
    const response = await fetch(baseURL, {
      redirect: 'follow',
      signal: AbortSignal.timeout(2000),
    })
    return response.ok
  } catch {
    return false
  }
}

async function cleanup(): Promise<void> {
  if (!startedServer || cleanedUp) return
  cleanedUp = true
  try {
    await run(['task', 'dev:stop'])
  } finally {
    if (devTask && devTaskExitCode === null) {
      const exited = await Promise.race([
        devTask.exited.then(() => true),
        sleep(5000).then(() => false),
      ])
      if (!exited) {
        devTask.kill()
      }
    }
    await removeManagedHome()
  }
}

async function run(command: string[], extraEnv: Record<string, string> = {}): Promise<void> {
  const proc = spawn(command, extraEnv, 'inherit')
  const code = await proc.exited
  if (code !== 0) {
    throw new Error(`${command.join(' ')} exited with status ${code}`)
  }
}

function spawn(command: string[], extraEnv: Record<string, string> = {}, stdio: 'inherit' | 'ignore'): Bun.Subprocess {
  return Bun.spawn(command, {
    cwd: process.cwd(),
    env: { ...Bun.env, ...extraEnv },
    stdin: stdio,
    stdout: stdio,
    stderr: stdio,
  })
}

function normalizeBaseURL(value: string | undefined): string {
  return (value ?? '').trim().replace(/\/+$/, '')
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
