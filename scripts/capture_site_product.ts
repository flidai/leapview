import { chromium } from '@playwright/test'
import { existsSync } from 'node:fs'
import { readFile, rm } from 'node:fs/promises'
import { createServer } from 'node:net'
import { join } from 'node:path'

const root = process.cwd()
const captureRoot = join(root, '.tmp', 'site-product-capture')
const home = join(captureRoot, 'home')
const dashboardPath = '/dashboards/dashboard:visual-showcase/pages/overview'
const viewport = { width: 1440, height: 900 }

await removeCaptureRoot()
await run(['task', 'postgres:dev:up'])

const port = await availablePort()
const origin = `http://127.0.0.1:${port}`
await rm(join(root, '.tmp', 'dev-server.port'), { force: true })
await rm(join(root, '.tmp', 'dev-server.pid'), { force: true })
const server = Bun.spawn(['./scripts/dev-server.sh', 'start', 'dashboards/leapview.yaml', 'olist', '.data/olist'], {
  cwd: root,
  env: {
    ...process.env,
    PORT: String(port),
    LEAPVIEW_ADDR: `127.0.0.1:${port}`,
    LEAPVIEW_DEV_AUTH_BYPASS: 'true',
    LEAPVIEW_DEV_RESTART: '1',
    LEAPVIEW_ENVIRONMENT: 'dev',
    LEAPVIEW_PRODUCTION: 'false',
    LEAPVIEW_HOME: home,
  },
  stdout: 'pipe',
  stderr: 'pipe',
})
const serverStdout = new Response(server.stdout).text()
const serverStderr = new Response(server.stderr).text()
let captureFailure: unknown

try {
  await waitForServer(`${origin}/`, server, port)

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
  await run(['./scripts/dev-server.sh', 'stop']).catch(() => undefined)
  if (server.exitCode === null) server.kill()
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

async function waitForServer(url: string, process: Bun.Subprocess, port: number): Promise<void> {
  for (let attempt = 0; attempt < 300; attempt++) {
    if (process.exitCode !== null) throw new Error(`capture server exited with code ${process.exitCode}`)
    try {
      const readyPort = (await readFile(join(root, '.tmp', 'dev-server.port'), 'utf8')).trim()
      if (readyPort === String(port)) {
        const response = await fetch(url)
        if (response.ok) return
      }
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
