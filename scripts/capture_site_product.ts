import { chromium } from '@playwright/test'
import { existsSync, realpathSync } from 'node:fs'
import { mkdir, rm } from 'node:fs/promises'
import { createServer } from 'node:net'
import { join } from 'node:path'

const root = process.cwd()
const captureRoot = join(root, '.tmp', 'site-product-capture')
const home = join(captureRoot, 'home')
const binary = join(captureRoot, 'leapview')
const extensionSupplyPath = join(captureRoot, 'extension-supply.json')
const olistData = realpathSync(join(root, '.data', 'olist'))
const dashboardPath = '/dashboards/dashboard:visual-showcase/pages/overview'
const viewport = { width: 1440, height: 900 }

await removeCaptureRoot()
await mkdir(join(home, 'managed-data'), { recursive: true })
await mkdir(join(home, 'duckdb'), { recursive: true })
await mkdir(join(home, 'ducklake'), { recursive: true })

await run(['go', 'run', './internal/app/tools/ducklakeprepare', '--supply-out', extensionSupplyPath])
const extensionSupplySHA256 = (await Bun.file(`${extensionSupplyPath}.sha256`).text()).trim().split(/\s+/)[0]
if (!extensionSupplySHA256) throw new Error('DuckDB extension supply preparation did not return a digest')
await run(['go', 'build', '-tags=duckdb_arrow', '-o', binary, './cmd/leapview'])

const port = await availablePort()
const origin = `http://127.0.0.1:${port}`
const server = Bun.spawn([binary], {
  cwd: root,
  env: {
    ...process.env,
    LEAPVIEW_ADDR: `127.0.0.1:${port}`,
    LEAPVIEW_DEV_AUTH_BYPASS: 'true',
    LEAPVIEW_HOME: home,
    LEAPVIEW_MANAGED_DATA_DIR: join(home, 'managed-data'),
    LEAPVIEW_DUCKDB_DIR: join(home, 'duckdb'),
    LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH: extensionSupplyPath,
    LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_SHA256: extensionSupplySHA256,
  },
  stdout: 'pipe',
  stderr: 'pipe',
})
const serverStdout = new Response(server.stdout).text()
const serverStderr = new Response(server.stderr).text()
let captureFailure: unknown

try {
  await waitForServer(`${origin}/`, server)
  const syncOutput = await run([
    binary,
    'data',
    'sync',
    '--project',
    'dashboards/leapview.yaml',
    '--connection',
    'olist',
    '--from',
    olistData,
    '--target',
    origin,
    '--token',
    'dev',
  ])
  const revision = syncOutput.match(/^staged (sha256:[0-9a-f]{64})$/m)?.[1]
  if (!revision) throw new Error(`managed data sync did not return a revision:\n${syncOutput}`)

  const devOutput = await run([
    binary,
    'dev',
    '--once',
    '--project',
    'dashboards/leapview.yaml',
    '--target',
    origin,
    '--token',
    'dev',
  ])
  const candidateID = devOutput.match(/^candidate (cand_[A-Za-z0-9_-]+)\b/m)?.[1]
  if (!candidateID) throw new Error(`development sync did not return a candidate ID:\n${devOutput}`)
  await run([
    binary,
    'publish',
    candidateID,
    '--token',
    'dev',
  ])

  const browser = await chromium.launch()
  try {
    for (const mode of ['light', 'dark'] as const) {
      const context = await browser.newContext({
        viewport,
        colorScheme: mode,
        deviceScaleFactor: 1,
      })
      try {
        await context.addInitScript((theme) => {
          localStorage.setItem('leapview-color-mode', theme)
        }, mode)
        const page = await context.newPage()
        await page.goto(`${origin}${dashboardPath}`, { waitUntil: 'domcontentloaded' })
        await page.getByRole('navigation', { name: 'Breadcrumb' }).getByText('Visual Showcase', { exact: true }).waitFor()
        await page.waitForFunction(() => {
          const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement & {
            signals?: { status?: { loading?: boolean } }
            shadowRoot: ShadowRoot
          }
          if (!dashboard?.shadowRoot || dashboard.signals?.status?.loading !== false) return false
          const charts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as Array<HTMLElement & {
            envelope?: { status?: { kind?: string }; dataState?: { kind?: string; datasets?: Array<{ rows?: unknown[] }>; availableRows?: number } }
          }>
          return charts.length >= 4 && charts.every((chart) => {
            const envelope = chart.envelope
            if (!envelope || (envelope.status?.kind !== 'ready' && envelope.status?.kind !== 'partial')) return false
            return envelope.dataState?.kind === 'windowed'
              ? (envelope.dataState.availableRows ?? 0) > 0
              : (envelope.dataState?.datasets?.[0]?.rows?.length ?? 0) > 0
          })
        })
        await page.waitForTimeout(250)
        await page.addStyleTag({ content: 'datastar-inspector { display: none !important; }' })
        await page.evaluate(() => {
          document.querySelectorAll('datastar-inspector').forEach((inspector) => inspector.remove())
        })
        await page.screenshot({
          path: join(root, 'site', 'static', `product-dashboard-${mode}.png`),
          type: 'png',
          animations: 'disabled',
        })
      } finally {
        await context.close()
      }
    }
  } finally {
    await browser.close()
  }
} catch (error) {
  captureFailure = error
  throw error
} finally {
  server.kill()
  await server.exited
  const output = `${await serverStdout}\n${await serverStderr}`.trim()
  if ((captureFailure || (server.exitCode && server.exitCode !== 143)) && output) {
    process.stderr.write(`${output}\n`)
  }
  await removeCaptureRoot()
}

async function availablePort(): Promise<number> {
  const listener = createServer()
  await new Promise<void>((resolve, reject) => {
    listener.once('error', reject)
    listener.listen(0, '127.0.0.1', resolve)
  })
  const address = listener.address()
  if (!address || typeof address === 'string') throw new Error('capture server did not receive a TCP port')
  await new Promise<void>((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()))
  return address.port
}

async function removeCaptureRoot(): Promise<void> {
  if (!existsSync(captureRoot)) return
  await run(['chmod', '-R', 'u+w', captureRoot])
  await rm(captureRoot, { recursive: true, force: true })
}

async function waitForServer(url: string, process: Bun.Subprocess): Promise<void> {
  for (let attempt = 0; attempt < 300; attempt++) {
    if (process.exitCode !== null) throw new Error(`capture server exited with code ${process.exitCode}`)
    try {
      const response = await fetch(url)
      if (response.ok) return
    } catch {
      // The server is still starting.
    }
    await Bun.sleep(100)
  }
  throw new Error(`capture server did not become ready at ${url}`)
}

async function run(command: string[]): Promise<string> {
  const process = Bun.spawn(command, { cwd: root, stdout: 'pipe', stderr: 'pipe' })
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(process.stdout).text(),
    new Response(process.stderr).text(),
    process.exited,
  ])
  if (exitCode !== 0) {
    throw new Error(`${command.join(' ')} failed with exit code ${exitCode}:\n${stderr || stdout}`)
  }
  return stdout
}
