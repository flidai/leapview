import { chmod, mkdir, open, readFile, rm } from 'node:fs/promises'
import { chromium } from '@playwright/test'

const portFile = '.tmp/dev-server.port'
const qaHome = '.tmp/qa-ui-framework/home'
const qaSessionPath = `${qaHome}/browser-session.json`
const qaPostgresEnv = {
  LEAPVIEW_POSTGRES_PROJECT_SUFFIX: '-qa-ui-framework',
  LEAPVIEW_POSTGRES_TEST_MODE: '1',
  LEAPVIEW_POSTGRES_DEV_ENV_FILE: `${qaHome}/postgres-dev.env`,
}
const qaRuntimeEnv = {
  LEAPVIEW_HOME: qaHome,
  LEAPVIEW_MANAGED_DATA_DIR: `${qaHome}/managed-data`,
  LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES: '67108864',
}
const managedServerReadyAttempts = 1800
let startedServer = false
let cleanedUp = false
let devTask: Bun.Subprocess | null = null
let devTaskExitCode: number | null = null
let createdSessionState = false

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
    const storageState = await prepareBrowserSession(baseURL)
    const browserEnv = {
      LEAPVIEW_BASE_URL: baseURL,
      ...(storageState ? { LEAPVIEW_QA_STORAGE_STATE: storageState } : {}),
    }
    const qaScope = Bun.env.LEAPVIEW_UI_QA_SCOPE?.trim() || 'all'
    if (qaScope !== 'all' && qaScope !== 'visual') {
      throw new Error(`Unsupported LEAPVIEW_UI_QA_SCOPE=${JSON.stringify(qaScope)}; expected "all" or "visual"`)
    }
    if (qaScope === 'all') {
      await run(['bun', 'run', 'qa:datastar-lit-routes'], browserEnv)
    }
    const visualCommand = [
      'bun',
      'x',
      'playwright',
      'test',
      '--config',
      'scripts/playwright.visual.config.ts',
    ]
    const visualEnv = browserEnv
    if (Bun.env.LEAPVIEW_UPDATE_VISUAL_BASELINES === '1') {
      await run([...visualCommand, '--update-snapshots'], visualEnv)
    }
    await run(visualCommand, visualEnv)
  } finally {
    await cleanup()
  }
}

async function prepareBrowserSession(baseURL: string): Promise<string | null> {
  const supplied = Bun.env.LEAPVIEW_QA_STORAGE_STATE?.trim()
  if (supplied) return supplied
  const address = new URL(baseURL)
  if (address.protocol !== 'http:' || !['localhost', '127.0.0.1', '::1'].includes(address.hostname)) return null

  const browser = await chromium.launch()
  try {
    const context = await browser.newContext()
    const page = await context.newPage()
    const response = await page.goto(new URL('/login', baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!response?.ok()) throw new Error(`QA development login returned HTTP ${response?.status() ?? 'unknown'}`)
    if (new URL(page.url()).pathname === '/login') {
      const shortcut = page.getByRole('button', { name: 'Continue as Local Developer', exact: true })
      if (!await shortcut.isVisible()) {
        throw new Error('Local QA requires the managed development quick login or LEAPVIEW_QA_STORAGE_STATE')
      }
      await shortcut.click()
      await page.waitForURL((url) => url.pathname !== '/login')
    }
    const root = await page.goto(new URL('/', baseURL).toString(), { waitUntil: 'domcontentloaded' })
    if (!root?.ok() || new URL(page.url()).pathname !== '/') {
      throw new Error('QA browser session cannot open Insights after local sign-in')
    }
    await page.locator('lv-catalog-page').waitFor()
    const state = await context.storageState()
    if (state.cookies.length === 0) return null
    await mkdir(qaHome, { recursive: true, mode: 0o700 })
    await chmod(qaHome, 0o700)
    const file = await open(qaSessionPath, 'w', 0o600)
    try {
      await file.chmod(0o600)
      await file.writeFile(JSON.stringify(state))
    } finally {
      await file.close()
    }
    createdSessionState = true
    return qaSessionPath
  } finally {
    await browser.close()
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
    ...qaPostgresEnv,
    ...qaRuntimeEnv,
    LEAPVIEW_DEV_LOG_LINES: '0',
    LEAPVIEW_DEV_READY_ATTEMPTS: String(managedServerReadyAttempts),
    LEAPVIEW_DEV_SKIP_PUBLISH: '1',
  }, 'ignore')
  void devTask.exited.then((code) => {
    devTaskExitCode = code
  })

  const started = await waitForManagedServer()
  await deployManagedProject()
  await waitForProjectReady(started)
  return started
}

async function prepareManagedHome(): Promise<void> {
  await destroyManagedPostgres()
  await removeManagedHome()
  await mkdir(qaHome, { recursive: true })
}

async function destroyManagedPostgres(): Promise<void> {
  await run(['./scripts/postgres-dev.sh', 'destroy'], qaPostgresEnv)
}

async function removeManagedHome(): Promise<void> {
  const chmod = spawn(['chmod', '-R', 'u+w', qaHome], {}, 'ignore')
  await chmod.exited
  await rm(qaHome, { recursive: true, force: true })
}

async function deployManagedProject(): Promise<void> {
  // A disposable QA database has no pinned managed-data revision yet. The
  // ordinary dev:publish task skips data sync and is only safe after seeding.
  const command = ['./scripts/dev-server.sh', 'publish']
  let lastError: unknown
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      await run(command, { ...qaPostgresEnv, ...qaRuntimeEnv, LEAPVIEW_DEV_SKIP_DATA_SYNC: '0' })
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
    if (baseURL) {
      // The application root intentionally remains unavailable until a
      // project has been published. Probe process health first so the QA
      // harness can perform that initial publication instead of deadlocking
      // while it waits for the publication it owns.
      const healthURL = new URL('/healthz', baseURL).toString()
      if (await reachable(healthURL)) return baseURL
    }
    if (devTaskExitCode !== null) {
      throw new Error(`task dev exited before the managed server became reachable with status ${devTaskExitCode}`)
    }
    await sleep(200)
  }
  throw new Error('managed dev server did not become reachable')
}

async function waitForProjectReady(baseURL: string): Promise<void> {
  const exploreURL = new URL('/explore', baseURL).toString()
  for (let attempt = 0; attempt < managedServerReadyAttempts; attempt++) {
    if (await reachable(exploreURL)) return
    if (devTaskExitCode !== null) {
      throw new Error(`task dev exited before the published project became reachable with status ${devTaskExitCode}`)
    }
    await sleep(200)
  }
  throw new Error('published project did not become reachable')
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
  if (cleanedUp) return
  cleanedUp = true
  try {
    if (!startedServer) return
    await run(['task', 'dev:stop'])
  } finally {
    try {
      if (startedServer) {
        if (devTask && devTaskExitCode === null) {
          const exited = await Promise.race([
            devTask.exited.then(() => true),
            sleep(5000).then(() => false),
          ])
          if (!exited) devTask.kill()
        }
        try {
          await destroyManagedPostgres()
        } finally {
          await removeManagedHome()
        }
      }
    } finally {
      if (createdSessionState) await rm(qaSessionPath, { force: true })
    }
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
