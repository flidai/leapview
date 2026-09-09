import { createHash, randomUUID } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { cpus, release, totalmem } from 'node:os'
import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises'
import { normalize, relative, resolve } from 'node:path'
import { chromium, type Browser, type Page } from '@playwright/test'
import { escapeHTML, testDocument } from '../web/components/dashboard/dashboard-page-test-fixtures'
import {
  aggregateViewportQualification,
  validateViewportQualificationEvidence,
  validateViewportQualificationSample,
  viewportObservationError,
  viewportQualificationVisualCount,
  type ViewportQualificationSample,
  type ViewportQualificationVariant,
} from './viewport_qualification_contract'

const projectRoot = process.cwd()
const fixtureRoot = resolve('.tmp/dashboard-page-test')
const marginPixels = 600
const viewport = { width: 1280, height: 820 }
const measuredRepetitions = 5
const runID = `${new Date().toISOString().replaceAll(/[^0-9TZ-]/g, '')}-${process.pid}-${randomUUID().slice(0, 8)}`
const outputDirectory = resolve('.tmp/viewport-qualification', runID)

type BrowserObservation = { stage: string, visualID?: string }

function qualificationFixture(): string {
  return testDocument().replace(/data-signals="([^"]*)"/, (_match, attribute: string) => {
    const signals = JSON.parse(decodeAttribute(attribute))
    const sourceVisuals = Object.values(signals.visuals) as Array<Record<string, any>>
    if (sourceVisuals.length !== 3) throw new Error(`fixture requires three source visuals, found ${sourceVisuals.length}`)
    signals.visuals = {}
    signals.page.components = []
    signals.page.canvas.height = 2800
    signals.status = { ...signals.status, loading: false, error: '', progressPercent: 100 }
    signals.interactionSelections = []
    for (let index = 0; index < viewportQualificationVisualCount; index++) {
      const visual = structuredClone(sourceVisuals[index % sourceVisuals.length]!)
      visual.visualID = `qualification-${String(index + 1).padStart(2, '0')}`
      visual.spec.interactions = []
      visual.selection = []
      visual.highlights = []
      visual.status = { kind: 'ready' }
      visual.diagnostics = []
      signals.visuals[visual.visualID] = visual
      signals.page.components.push({
        id: `cell-${index + 1}`, kind: 'visual', visual: visual.visualID,
        // Keep rows away from the exact prefetch boundary. A zero-area edge
        // intersection has browser-specific threshold timing and is not a
        // representative readiness sample.
        x: 16 + (index % 3) * 336, y: 32 + Math.floor(index / 3) * 336,
        width: 320, height: 312,
      })
    }
    return `data-signals="${escapeHTML(JSON.stringify(signals))}"`
  })
}

function decodeAttribute(value: string): string {
  return value.replaceAll('&quot;', '"').replaceAll('&lt;', '<').replaceAll('&gt;', '>').replaceAll('&amp;', '&')
}

async function runSample(browser: Browser, baseURL: string, variant: ViewportQualificationVariant, repetition: number): Promise<ViewportQualificationSample> {
  const context = await browser.newContext({ viewport, reducedMotion: 'reduce', locale: 'en-US', timezoneId: 'UTC' })
  const page = await context.newPage()
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(`page error: ${error.message}`))
  page.on('requestfailed', (request) => errors.push(`request failed: ${request.failure()?.errorText ?? 'unknown'} ${request.url()}`))
  page.on('response', (response) => { if (response.status() >= 400) errors.push(`HTTP ${response.status()} ${response.url()}`) })
  await installQualificationProbe(page, variant === 'deferred')
  const session = await context.newCDPSession(page)
  try {
    await session.send('Performance.enable')
    await page.goto(`${baseURL}/${variant}/`, { waitUntil: 'load' })
    try {
      await page.waitForFunction(({ count, margin, deferred }) => {
        const dashboard = document.querySelector('lv-dashboard-page')
        const hosts = [...(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? [])] as any[]
        if (hosts.length !== count) return false
        const root = dashboard?.shadowRoot?.querySelector('lv-report-canvas')?.shadowRoot?.querySelector('.viewport')
        if (!root) return false
        const bounds = root.getBoundingClientRect()
        const near = hosts.filter((host) => {
          const rect = host.getBoundingClientRect()
          return rect.bottom >= bounds.top - margin && rect.top <= bounds.bottom + margin
            && rect.right >= bounds.left && rect.left <= bounds.right
        })
        if (near.length === 0 || near.length >= count) return false
        const required = deferred ? near : hosts
        return required.every((host) => (host.shadowRoot?.querySelector('.renderer')?.childElementCount ?? 0) > 0
          && !host.shadowRoot?.querySelector('[data-visualization-loading]'))
      }, { count: viewportQualificationVisualCount, margin: marginPixels, deferred: variant === 'deferred' }, { timeout: 30_000 })
    } catch (error) {
      const state = await page.evaluate(() => {
        const dashboard = document.querySelector('lv-dashboard-page') as any
        const hosts = [...(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? [])] as any[]
        return hosts.map((host) => ({
          visualID: host.envelope?.visualID,
          rendererChildren: host.shadowRoot?.querySelector('.renderer')?.childElementCount ?? 0,
          loading: Boolean(host.shadowRoot?.querySelector('[data-visualization-loading]')),
          error: host.shadowRoot?.querySelector('[role="alert"]')?.textContent?.trim() ?? '',
          top: Math.round(host.getBoundingClientRect().top),
        }))
      })
      throw new Error(`initial readiness failed for ${variant} repetition ${repetition}: ${error instanceof Error ? error.message : String(error)}; browser errors=${JSON.stringify(errors)}; hosts=${JSON.stringify(state)}`)
    }
    await page.evaluate(() => new Promise<void>((resolveFrame) => requestAnimationFrame(() => requestAnimationFrame(() => resolveFrame()))))

    const initial = await page.evaluate((margin) => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = [...dashboard.shadowRoot.querySelectorAll('lv-visualization-host')] as any[]
      const root = dashboard.shadowRoot.querySelector('lv-report-canvas')?.shadowRoot?.querySelector('.viewport')
      if (!root) throw new Error('dashboard visualization intersection root is unavailable')
      const bounds = root.getBoundingClientRect()
      const observations = (window as any).__viewportQualificationObservations as BrowserObservation[]
      const ids = (entries: BrowserObservation[], stage: string) => entries.filter((entry) => entry.stage === stage && entry.visualID).map((entry) => entry.visualID!)
      return {
        expectedVisualIDs: hosts.map((host) => host.envelope?.visualID).filter(Boolean),
        nearViewportVisualIDs: hosts.filter((host) => {
          const rect = host.getBoundingClientRect()
          return rect.bottom >= bounds.top - margin && rect.top <= bounds.bottom + margin
            && rect.right >= bounds.left && rect.left <= bounds.right
        }).map((host) => host.envelope?.visualID).filter(Boolean),
        shellVisualIDs: hosts.filter((host) => host.shadowRoot?.querySelector('#visualization-fallback')).map((host) => host.envelope?.visualID).filter(Boolean),
        initialMountVisualIDs: ids(observations, 'mount'),
        readinessMs: performance.now(),
      }
    }, marginPixels)
    const metrics = (await session.send('Performance.getMetrics')).metrics
    const metric = (name: string): number => metrics.find((entry) => entry.name === name)?.value ?? Number.NaN

    // Exercise real later intersections before snapshot() can force mounting.
    const scrolled = await page.evaluate(async () => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = [...dashboard.shadowRoot.querySelectorAll('lv-visualization-host')] as any[]
      const observations = (window as any).__viewportQualificationObservations as BrowserObservation[]
      const before = observations.length
      const settle = () => new Promise<void>((resolveFrame) => requestAnimationFrame(() => requestAnimationFrame(() => resolveFrame())))
      for (const host of hosts) {
        host.scrollIntoView({ block: 'center', inline: 'nearest' })
        const deadline = performance.now() + 30_000
        await settle()
        while (!(host.shadowRoot?.querySelector('.renderer')?.childElementCount > 0)
          || host.shadowRoot?.querySelector('[data-visualization-loading]')) {
          if (performance.now() >= deadline) throw new Error(`scroll did not mount ${host.envelope?.visualID}`)
          await settle()
        }
      }
      hosts[0]?.scrollIntoView()
      await settle()
      return {
        mounts: observations.filter((entry) => entry.stage === 'mount' && entry.visualID).map((entry) => entry.visualID!),
        newMounts: observations.slice(before).filter((entry) => entry.stage === 'mount' && entry.visualID).map((entry) => entry.visualID!),
        disposals: observations.filter((entry) => entry.stage === 'dispose' && entry.visualID).map((entry) => entry.visualID!),
      }
    })

    const forced = await page.evaluate(async () => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = [...dashboard.shadowRoot.querySelectorAll('lv-visualization-host')] as any[]
      const snapshots = await Promise.all(hosts.map(async (host) => {
        await host.snapshot()
        return host.envelope?.visualID as string
      }))
      const observations = (window as any).__viewportQualificationObservations as BrowserObservation[]
      return {
        snapshots,
        mounts: observations.filter((entry) => entry.stage === 'mount' && entry.visualID).map((entry) => entry.visualID!),
      }
    })

    const teardownDisposeVisualIDs = await page.evaluate(async () => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = [...dashboard.shadowRoot.querySelectorAll('lv-visualization-host')] as any[]
      const disposals: string[] = []
      for (const host of hosts) {
        host.addEventListener('lv-visualization-observation', (event: CustomEvent<BrowserObservation>) => {
          if (event.detail.stage === 'dispose' && event.detail.visualID) disposals.push(event.detail.visualID)
        })
      }
      hosts.forEach((host) => host.remove())
      await Promise.resolve()
      await new Promise<void>((resolveFrame) => requestAnimationFrame(() => resolveFrame()))
      return disposals
    })

    const rawObservations: unknown[] = await page.evaluate(() => (window as any).__viewportQualificationObservations)
    rawObservations.forEach((observation, index) => {
      const error = viewportObservationError(observation)
      if (error) errors.push(`malformed observation ${index}: ${error}`)
    })

    const sample: ViewportQualificationSample = {
      variant, repetition, warmup: repetition === 0,
      ...initial,
      forcedSnapshotVisualIDs: forced.snapshots,
      forcedMountVisualIDs: forced.mounts,
      scrollMountVisualIDs: scrolled.mounts,
      scrollNewMountVisualIDs: scrolled.newMounts,
      scrollDisposeVisualIDs: scrolled.disposals,
      teardownDisposeVisualIDs,
      taskDurationSeconds: metric('TaskDuration'),
      jsHeapUsedBytes: metric('JSHeapUsedSize'),
      errors,
    }
    const evidenceErrors = validateViewportQualificationSample(sample)
    if (evidenceErrors.length) throw new Error(evidenceErrors.join('\n'))
    return sample
  } finally {
    await context.close()
  }
}

async function installQualificationProbe(page: Page, deferred: boolean): Promise<void> {
  await page.addInitScript((useDeferred) => {
    ;(window as any).__viewportQualificationObservations = []
    document.addEventListener('lv-visualization-observation', (event: Event) => {
      ;(window as any).__viewportQualificationObservations.push({ ...(event as CustomEvent).detail })
    })
    if (useDeferred) return
    const registry = window.customElements
    const originalDefine = registry.define.bind(registry)
    Object.defineProperty(registry, 'define', {
      configurable: true,
      value(name: string, constructor: CustomElementConstructor, options?: ElementDefinitionOptions) {
        if (name !== 'lv-visualization-host') return originalDefine(name, constructor, options)
        delete (registry as unknown as Record<string, unknown>).define
        const Base = constructor as unknown as { new(): HTMLElement & { deferMount: boolean; connectedCallback(): void } }
        class QualificationEagerHost extends Base {
          // Override the production attribute before the host starts its lifecycle.
          connectedCallback(): void { this.deferMount = false; super.connectedCallback() }
        }
        return originalDefine(name, QualificationEagerHost, options)
      },
    })
  }, deferred)
}

async function run(): Promise<void> {
  await mkdir(outputDirectory, { recursive: true })
  try {
    const dirtyEntries = git(['status', '--porcelain', '--untracked-files=all']).trim().split('\n').filter(Boolean)
    if (dirtyEntries.length) throw new Error(`qualification requires a clean worktree; found: ${dirtyEntries.join(', ')}`)
    execFileSync(process.execPath, ['scripts/build_test_assets.ts', 'dashboard-page'], { cwd: projectRoot, stdio: 'inherit' })
    const fixture = qualificationFixture()
    const expectedBundle = resolve(fixtureRoot, 'dashboard-page-under-test.js')
    try { await readFile(expectedBundle) } catch { throw new Error(`missing generated dashboard fixture ${expectedBundle}; run bun scripts/build_test_assets.ts dashboard-page`) }
    const server = Bun.serve({ hostname: '127.0.0.1', port: 0, async fetch(request) {
      const pathname = new URL(request.url).pathname
      if (pathname === '/eager/' || pathname === '/deferred/') {
        return new Response(fixture, { headers: { 'content-type': 'text/html; charset=utf-8' } })
      }
      const base = pathname.startsWith('/static/vendor/') ? projectRoot : fixtureRoot
      const path = normalize(resolve(base, pathname.slice(1)))
      const escaped = relative(base, path).startsWith('..') || relative(base, path).startsWith('/')
      if (escaped) return new Response('not found', { status: 404 })
      const file = Bun.file(path)
      if (!await file.exists()) return new Response('not found', { status: 404 })
      return new Response(file)
    } })
    const browser = await chromium.launch()
    const samples: ViewportQualificationSample[] = []
    try {
      for (let repetition = 0; repetition <= measuredRepetitions; repetition++) {
        const order: ViewportQualificationVariant[] = repetition % 2 === 0 ? ['eager', 'deferred'] : ['deferred', 'eager']
        for (const variant of order) samples.push(await runSample(browser, `http://127.0.0.1:${server.port}`, variant, repetition))
      }
      const evidenceErrors = validateViewportQualificationEvidence(samples)
      if (evidenceErrors.length) throw new Error(evidenceErrors.join('\n'))
      const summary = aggregateViewportQualification(samples)
      const report = {
        schemaVersion: 2,
        status: 'pass',
        generatedAt: new Date().toISOString(),
        identity: {
          commit: git(['rev-parse', 'HEAD']).trim(),
          dirty: false,
          files: {
            harnessSha256: await digestFile('scripts/viewport_qualification.ts'),
            contractSha256: await digestFile('scripts/viewport_qualification_contract.ts'),
            fixtureSha256: digest(fixture),
            generatedDashboardBundleSha256: await digestDirectory(fixtureRoot),
            lockfileSha256: await digestFile('bun.lock'),
            hostSha256: await digestFile('web/components/dashboard/visualization/host.ts'),
            hostStylesSha256: await digestFile('web/components/dashboard/visualization/host-styles.ts'),
            dashboardPageSha256: await digestFile('web/components/dashboard/dashboard-page.ts'),
          },
          variants: {
            eager: 'same bundle with deferMount forced false before host connection by the hashed qualification harness',
            deferred: 'current production dashboard behavior with defer-mount enabled on visualization hosts',
          },
        },
        fixture: {
          id: 'synthetic-long-dashboard-24-v1', visualCount: viewportQualificationVisualCount,
          kinds: { kpi: 8, cartesian: 8, table: 8 }, viewport, rootMargin: `${marginPixels}px 0px`,
        },
        environment: {
          platform: process.platform, architecture: process.arch, kernel: release(),
          cpu: cpus()[0]?.model ?? 'unknown', logicalCPUs: cpus().length,
          totalMemoryBytes: totalmem(), bun: Bun.version, chromium: browser.version(),
        },
        protocol: {
          warmupsPerVariant: 1, measuredRepetitionsPerVariant: measuredRepetitions,
          order: 'alternating eager/deferred order by repetition', cache: 'fresh browser context per sample; warm machine and browser process',
          readiness: 'all hosts within the viewport plus the 600px vertical margin have completed renderer mount; eager control additionally waits for all hosts',
          aggregation: 'median and nearest-rank p95 over five measured samples; warmups excluded',
          lifecycle: 'initial readiness, sequential real scrolling through all hosts, return to first host, snapshots, teardown; scroll mount delta excludes initial mounts',
          variance: 'paired local evidence only; no cross-hardware timing limit or normalization is asserted',
        },
        scope: 'Synthetic local browser qualification, not production traffic or an installed runtime-image measurement. Timing and memory values are observations, not budgets.',
        summary,
        samples,
      }
      const reportPath = resolve(outputDirectory, 'report.json')
      await writeFile(reportPath, `${JSON.stringify(report, null, 2)}\n`)
      console.log(`Viewport qualification passed: ${reportPath}`)
    } finally {
      await browser.close()
      server.stop(true)
    }
  } catch (error) {
    const failurePath = resolve(outputDirectory, 'failure.json')
    await writeFile(failurePath, `${JSON.stringify({ schemaVersion: 1, status: 'failed', generatedAt: new Date().toISOString(), error: error instanceof Error ? error.message : String(error) }, null, 2)}\n`)
    console.error(`Viewport qualification failed: ${failurePath}`)
    throw error
  }
}

function git(args: string[]): string { return execFileSync('git', args, { cwd: projectRoot, encoding: 'utf8' }) }
function digest(value: string | Uint8Array): string { return createHash('sha256').update(value).digest('hex') }
async function digestFile(path: string): Promise<string> { return digest(await readFile(resolve(projectRoot, path))) }
async function digestDirectory(root: string): Promise<string> {
  const files: string[] = []
  async function walk(directory: string): Promise<void> {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const path = resolve(directory, entry.name)
      if (entry.isDirectory()) await walk(path)
      else if (entry.isFile()) files.push(path)
    }
  }
  await walk(root)
  const hash = createHash('sha256')
  for (const path of files.sort()) {
    const name = relative(root, path)
    const contents = await readFile(path)
    hash.update(`${name}\0${contents.byteLength}\0`)
    hash.update(contents)
  }
  return hash.digest('hex')
}

if (import.meta.main) await run()
