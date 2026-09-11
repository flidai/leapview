import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testVisualizationEnvelopes } from '../dashboard-page-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const fixtureRoot = join(projectRoot, '.tmp/visualization-host-test')

function testDocument(): string {
  // Exercise the real host and adapters without requiring dashboard/Datastar
  // bootstrap. Route-level behavior stays covered by the dashboard suites.
  return `<!doctype html><html><body>
    <script type="module">
      import '/visualization-host-under-test.js';
      const envelopes = ${JSON.stringify(testVisualizationEnvelopes())};
      const sources = Object.fromEntries(Object.entries(envelopes).map(([id, envelope]) => [id, { envelope }]));
      const eager = document.createElement('lv-visualization-host');
      eager.envelope = envelopes.orders_kpi;
      document.body.append(eager);
      sources.orders_kpi = eager;
      window.__lvSourceHosts = sources;
    </script>
  </body></html>`
}

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : fixtureRoot
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) { response.writeHead(404); response.end('not found'); return }
    try {
      response.setHeader('content-type', file.endsWith('.css') ? 'text/css' : 'text/javascript')
      response.end(await readFile(file))
    } catch { response.writeHead(404); response.end('not found') }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('deferred hosts retain the latest valid envelope and mount once on eligibility', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      const observers: Array<{ callback: IntersectionObserverCallback; target?: Element; disconnected: boolean; root: Element | null; rootMargin: string; scrollMargin: string }> = []
      class DeferredIntersectionObserver {
        readonly record: { callback: IntersectionObserverCallback; target?: Element; disconnected: boolean; root: Element | null; rootMargin: string; scrollMargin: string }
        readonly scrollMargin: string
        constructor(callback: IntersectionObserverCallback, options?: IntersectionObserverInit) {
          this.scrollMargin = (options as IntersectionObserverInit & { scrollMargin?: string })?.scrollMargin ?? ''
          this.record = { callback, disconnected: false, root: options?.root instanceof Element ? options.root : null, rootMargin: options?.rootMargin ?? '', scrollMargin: this.scrollMargin }
          observers.push(this.record)
        }
        observe(target: Element): void { this.record.target = target }
        unobserve(): void {}
        disconnect(): void { this.record.disconnected = true }
        takeRecords(): IntersectionObserverEntry[] { return [] }
      }
      Object.defineProperty(window, 'IntersectionObserver', { configurable: true, value: DeferredIntersectionObserver })
      Object.defineProperty(window, '__lvIntersectionObservers', { configurable: true, value: observers })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)

    const state = await page.evaluate(async () => {
      const source = (window as any).__lvSourceHosts.orders_kpi
      const deferred = document.createElement('lv-visualization-host') as any
      deferred.deferMount = true
      deferred.envelope = JSON.parse(JSON.stringify(source.envelope))
      const canvas = document.createElement('lv-report-canvas') as any
      const viewport = document.createElement('div')
      viewport.append(document.createElement('slot'))
      canvas.attachShadow({ mode: 'open' }).append(viewport)
      canvas.append(deferred)
      document.body.append(canvas)
      await deferred.updateComplete

      const observers = (window as any).__lvIntersectionObservers as Array<{ callback: IntersectionObserverCallback; target?: Element; disconnected: boolean; root: Element | null; rootMargin: string; scrollMargin: string }>
      const record = observers.find((candidate) => candidate.target === (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')) as any
      const before = (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0

      const latest = JSON.parse(JSON.stringify(deferred.envelope))
      latest.dataRevision = 2
      latest.dataState.dataRevision = 2
      for (const dataset of latest.dataState.datasets ?? []) dataset.dataRevision = 2
      deferred.envelope = latest
      const loading = JSON.parse(JSON.stringify(latest))
      loading.status = { kind: 'loading', message: 'Refreshing' }
      deferred.envelope = loading
      const errored = JSON.parse(JSON.stringify(loading))
      errored.status = { kind: 'error', message: 'Refresh failed' }
      deferred.envelope = errored
      const stale = JSON.parse(JSON.stringify(latest))
      stale.dataRevision = 1
      stale.dataState.dataRevision = 1
      for (const dataset of stale.dataState.datasets ?? []) dataset.dataRevision = 1
      deferred.envelope = stale
      const invalid = JSON.parse(JSON.stringify(latest))
      invalid.dataRevision = -1
      deferred.envelope = invalid
      await deferred.updateComplete

      const target = record.target!
      const observerEntry = (isIntersecting: boolean): IntersectionObserverEntry => ({ isIntersecting, target } as IntersectionObserverEntry)
      record.callback([observerEntry(false)], {} as IntersectionObserver)
      window.dispatchEvent(new Event('beforeprint'))
      await Promise.resolve()
      const beforeEligibility = (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0
      record.callback([observerEntry(true)], {} as IntersectionObserver)
      const deadline = Date.now() + 2_000
      while (((deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0) === 0 && Date.now() < deadline) {
        await new Promise<void>((resolve) => setTimeout(resolve, 0))
      }
      const controllerBefore = (deferred as any).controller
      const mounted = (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0
      const mountedRevision = (deferred as any).controller?.envelope?.dataRevision
      const snapshotText = await (await deferred.snapshot()).text()
      record.callback([observerEntry(true)], {} as IntersectionObserver)
      await deferred.ensureMounted()
      const controllerAfter = (deferred as any).controller

      deferred.remove()
      await new Promise<void>((resolve) => queueMicrotask(() => queueMicrotask(resolve)))
      record.callback([observerEntry(true)], {} as IntersectionObserver)
      await Promise.resolve()
      return {
        before,
        beforeEligibility,
        retainedRevision: deferred.envelope?.dataRevision,
        retainedStatus: deferred.envelope?.status.kind,
        mounted,
        mountedRevision,
        snapshotText,
        mountedOnce: controllerBefore === controllerAfter,
        disconnected: record.disconnected,
        observerCount: observers.length,
        implicitRoot: record.root === null,
        rootMargin: record.rootMargin,
        scrollMargin: record.scrollMargin,
        disposed: (deferred as any).controller === undefined,
        resurrected: Boolean((deferred as any).controller),
      }
    })

    expect(state).toMatchObject({
      before: 0,
      beforeEligibility: 0,
      retainedRevision: 2,
      retainedStatus: 'error',
      mountedRevision: 2,
      mountedOnce: true,
      disconnected: true,
      observerCount: 1,
      implicitRoot: true,
      rootMargin: '600px 0px',
      scrollMargin: '600px 0px',
      disposed: true,
      resurrected: false,
    })
    expect(state.mounted).toBeGreaterThan(0)
    expect(state.snapshotText).toContain('Orders')
  } finally {
    await page.close()
  }
})

test('mounted deferred hosts retain current renderer, shell, and actions after stale signals', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (window as any).__lvSourceHosts)
    const result = await page.evaluate(async () => {
      const host = document.createElement('lv-visualization-host') as any
      host.deferMount = true
      const current = structuredClone((window as any).__lvSourceHosts.orders_chart.envelope)
      current.dataRevision = 3
      current.dataState.dataRevision = 3
      current.status = { kind: 'ready' }
      for (const dataset of current.dataState.datasets) dataset.dataRevision = 3
      host.envelope = current
      document.body.append(host)
      await host.ensureMounted()
      const stale = structuredClone(current)
      stale.dataRevision = 2
      stale.dataState.dataRevision = 2
      for (const dataset of stale.dataState.datasets) dataset.dataRevision = 2
      stale.status = { kind: 'error', message: 'Stale failure' }
      stale.dataState.datasets[0].rows = [['obsolete', 999]]
      host.envelope = stale
      await host.updateComplete
      await host.ensureMounted()
      let action: any
      host.addEventListener('lv-visual-action', (event: CustomEvent) => { action = event.detail });
      (host.shadowRoot as ShadowRoot).querySelector<HTMLElement>('.visual-options summary')!.click();
      (host.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.visual-options button')!.click()
      const state = {
        signalRevision: host.envelope.dataRevision,
        rendererRevision: host.controller.envelope.dataRevision,
        alert: (host.shadowRoot as ShadowRoot).querySelector('[role="alert"]')?.textContent ?? '',
        fallback: (host.shadowRoot as ShadowRoot).querySelector<HTMLElement>('#visualization-fallback')!.textContent,
        actionRows: JSON.stringify(action?.rows),
      }
      host.remove()
      return state
    })
    expect(result.signalRevision).toBe(3)
    expect(result.rendererRevision).toBe(3)
    expect(result.alert).toBe('')
    expect(result.fallback).not.toContain('Stale failure')
    expect(result.actionRows).toContain('delivered')
    expect(result.actionRows).not.toContain('obsolete')
  } finally {
    await page.close()
  }
})

test('queued signals are announced only after their own renderer apply completes', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (window as any).__lvSourceHosts)
    const result = await page.evaluate(async () => {
      const host = document.createElement('lv-visualization-host') as any
      host.deferMount = true
      const initial = structuredClone((window as any).__lvSourceHosts.orders_chart.envelope)
      initial.spec.accessibility = { ...initial.spec.accessibility, announceChanges: true }
      host.envelope = initial
      document.body.append(host)
      await host.ensureMounted()
      const releases: Record<number, () => void> = {}
      const entered: Record<number, () => void> = {}
      const entry = Object.fromEntries([2, 3].map((revision) => [revision, new Promise<void>((resolve) => { entered[revision] = resolve })]))
      const apply = host.controller.apply.bind(host.controller)
      host.controller.apply = async (envelope: any, context: any) => {
        const paused = new Promise<void>((resolve) => { releases[envelope.dataRevision] = resolve })
        entered[envelope.dataRevision]!()
        await paused
        await apply(envelope, context)
      }
      const update = (revision: number) => {
        const next = structuredClone(initial)
        next.dataRevision = revision
        next.dataState.dataRevision = revision
        for (const dataset of next.dataState.datasets) dataset.dataRevision = revision
        host.envelope = next
      }
      update(2)
      await entry[2]
      update(3)
      await host.updateComplete
      releases[2]!()
      await entry[3]
      const during = { announcement: host.announcement, applying: host.applying }
      releases[3]!()
      await host.waitForApply()
      const after = { announcement: host.announcement, revision: host.controller.envelope.dataRevision, applying: host.applying }
      host.remove()
      return { during, after }
    })
    expect(result.during.announcement).toBe('')
    expect(result.during.applying).toBe(true)
    expect(result.after.announcement).toContain('updated')
    expect(result.after.revision).toBe(3)
    expect(result.after.applying).toBe(false)
  } finally {
    await page.close()
  }
})

test('snapshot explicitly mounts a deferred host without an intersection callback', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      const observers: Array<{ callback: IntersectionObserverCallback; target?: Element; disconnected: boolean }> = []
      class DeferredIntersectionObserver {
        readonly record: { callback: IntersectionObserverCallback; target?: Element; disconnected: boolean }
        constructor(callback: IntersectionObserverCallback) {
          this.record = { callback, disconnected: false }
          observers.push(this.record)
        }
        observe(target: Element): void { this.record.target = target }
        disconnect(): void { this.record.disconnected = true }
      }
      Object.defineProperty(window, 'IntersectionObserver', { configurable: true, value: DeferredIntersectionObserver })
      Object.defineProperty(window, '__lvIntersectionObservers', { configurable: true, value: observers })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)

    const state = await page.evaluate(async () => {
      const source = (window as any).__lvSourceHosts.orders_kpi
      const deferred = document.createElement('lv-visualization-host') as any
      deferred.deferMount = true
      deferred.envelope = JSON.parse(JSON.stringify(source.envelope))
      document.body.append(deferred)
      await deferred.updateComplete
      const record = ((window as any).__lvIntersectionObservers as Array<{ target?: Element; disconnected: boolean }>).find((candidate) => candidate.target === (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')) as any
      const before = (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0
      const snapshotText = await (await deferred.snapshot()).text()
      return { before, mounted: (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0, snapshotText, disconnected: record.disconnected }
    })
    expect(state.before).toBe(0)
    expect(state.mounted).toBeGreaterThan(0)
    expect(state.snapshotText).toContain('Orders')
    expect(state.disconnected).toBe(true)
  } finally {
    await page.close()
  }
})

test('eligibility before data waits for the later valid envelope', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      const observers: Array<{ callback: IntersectionObserverCallback; target?: Element }> = []
      class DeferredIntersectionObserver {
        readonly record: { callback: IntersectionObserverCallback; target?: Element }
        constructor(callback: IntersectionObserverCallback) {
          this.record = { callback }
          observers.push(this.record)
        }
        observe(target: Element): void { this.record.target = target }
        disconnect(): void {}
      }
      Object.defineProperty(window, 'IntersectionObserver', { configurable: true, value: DeferredIntersectionObserver })
      Object.defineProperty(window, '__lvIntersectionObservers', { configurable: true, value: observers })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)

    const mounted = await page.evaluate(async () => {
      const source = (window as any).__lvSourceHosts.orders_kpi
      const deferred = document.createElement('lv-visualization-host') as any
      deferred.deferMount = true
      document.body.append(deferred)
      await deferred.updateComplete
      const record = ((window as any).__lvIntersectionObservers as Array<{ callback: IntersectionObserverCallback; target?: Element }>).find((candidate) => candidate.target === (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')) as any
      const observerEntry = (isIntersecting: boolean): IntersectionObserverEntry => ({ isIntersecting, target: record.target! } as IntersectionObserverEntry)
      record.callback([observerEntry(true)], {} as IntersectionObserver)
      await Promise.resolve()
      const beforeEnvelope = (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0
      const ensureBeforeEnvelope = await deferred.ensureMounted().then(() => 'resolved', (error: unknown) => error instanceof Error ? error.message : String(error))
      const snapshotBeforeEnvelope = await deferred.snapshot().then(() => 'resolved', (error: unknown) => error instanceof Error ? error.message : String(error))
      deferred.envelope = JSON.parse(JSON.stringify(source.envelope))
      await deferred.updateComplete
      const deadline = Date.now() + 2_000
      while (((deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0) === 0 && Date.now() < deadline) {
        await new Promise<void>((resolve) => setTimeout(resolve, 0))
      }
      return {
        beforeEnvelope,
        ensureBeforeEnvelope,
        snapshotBeforeEnvelope,
        mounted: (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0,
        revision: deferred.envelope?.dataRevision,
      }
    })
    expect(mounted.beforeEnvelope).toBe(0)
    expect(mounted.ensureBeforeEnvelope).toBe('visualization has no envelope')
    expect(mounted.snapshotBeforeEnvelope).toBe('visualization has no envelope')
    expect(mounted.mounted).toBeGreaterThan(0)
    expect(mounted.revision).toBe(1)
  } finally {
    await page.close()
  }
})

test('eager invalid and unknown-renderer envelopes remain visible errors', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)

    const errors = await page.evaluate(async () => {
      const source = (window as any).__lvSourceHosts.orders_kpi
      const invalid = document.createElement('lv-visualization-host') as any
      const invalidEnvelope = JSON.parse(JSON.stringify(source.envelope))
      invalidEnvelope.dataRevision = -1
      invalid.envelope = invalidEnvelope
      document.body.append(invalid)
      const unknown = document.createElement('lv-visualization-host') as any
      const unknownEnvelope = JSON.parse(JSON.stringify(source.envelope))
      unknownEnvelope.rendererID = 'missing-renderer'
      unknown.envelope = unknownEnvelope
      document.body.append(unknown)
      const deadline = Date.now() + 2_000
      while ((!(invalid.shadowRoot as ShadowRoot).querySelector('[role="alert"]') || !(unknown.shadowRoot as ShadowRoot).querySelector('[role="alert"]')) && Date.now() < deadline) {
        await new Promise<void>((resolve) => setTimeout(resolve, 0))
      }
      return {
        invalid: (invalid.shadowRoot as ShadowRoot).querySelector('[role="alert"]')?.textContent?.trim(),
        unknown: (unknown.shadowRoot as ShadowRoot).querySelector('[role="alert"]')?.textContent?.trim(),
      }
    })
    expect(errors.invalid).toContain('invalid visualization envelope')
    expect(errors.unknown).toContain('unknown visualization renderer')
  } finally {
    await page.close()
  }
})

test('pending renderer loads reject stale mount promises after detach and reattach', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  let releaseRenderer!: () => void
  let rendererRequested!: () => void
  const rendererBlocked = new Promise<void>((resolve) => { releaseRenderer = resolve })
  const rendererRequest = new Promise<void>((resolve) => { rendererRequested = resolve })
  try {
    await page.route('**/chunks/echarts-*.js', async (route) => {
      rendererRequested()
      await rendererBlocked
      await route.continue()
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)

    await page.evaluate(async () => {
      const source = (window as any).__lvSourceHosts.orders_chart
      const deferred = document.createElement('lv-visualization-host') as any
      deferred.deferMount = true
      deferred.envelope = JSON.parse(JSON.stringify(source.envelope))
      document.body.append(deferred)
      await deferred.updateComplete
      const transient = document.createElement('lv-visualization-host') as any
      transient.deferMount = true
      transient.envelope = JSON.parse(JSON.stringify(source.envelope))
      document.body.append(transient)
      await transient.updateComplete
      const transientMount = transient.ensureMounted().then(() => 'resolved', (error: unknown) => `rejected:${error instanceof Error ? error.message : String(error)}`)
      await Promise.resolve()
      const transientController = transient.controller
      const transientHolder = document.createElement('section')
      document.body.append(transientHolder)
      transientHolder.append(transient)
      await transient.updateComplete
      const stale = deferred.ensureMounted().then(() => 'resolved', (error: unknown) => `rejected:${error instanceof Error ? error.message : String(error)}`)
      const race = { stale, staleSettled: false, transientMount, transientController, transient, fresh: Promise.resolve('pending'), reattached: false }
      stale.then(() => { race.staleSettled = true })
      ;(window as any).__lvMountRace = { deferred, race }
      await Promise.resolve()
      deferred.remove()
      await new Promise<void>((resolve) => queueMicrotask(() => queueMicrotask(resolve)))
      document.body.append(deferred)
      await deferred.updateComplete
      race.fresh = deferred.ensureMounted().then(() => 'resolved', (error: unknown) => `rejected:${error instanceof Error ? error.message : String(error)}`)
      race.reattached = true
    })
    await rendererRequest
    const beforeRelease = await page.evaluate(() => (window as any).__lvMountRace.race.staleSettled)
    expect(beforeRelease).toBe(false)
    releaseRenderer()
    const result = await page.evaluate(async () => {
      const { deferred, race } = (window as any).__lvMountRace
      return {
        stale: await race.stale,
        transient: await race.transientMount,
        transientControllerRetained: race.transient.controller === race.transientController,
        fresh: await race.fresh,
        mounted: (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0,
        controller: Boolean(deferred.controller),
      }
    })
    expect(result.stale).toContain('rejected:visualization mount superseded')
    expect(result.transient).toBe('resolved')
    expect(result.transientControllerRetained).toBe(true)
    expect(result.fresh).toBe('resolved')
    expect(result.mounted).toBeGreaterThan(0)
    expect(result.controller).toBe(true)
  } finally {
    releaseRenderer?.()
    await page.close()
  }
})

test('existing and authoring hosts stay eager by default, including with no intersection', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      const observers: unknown[] = []
      class FailingIntersectionObserver {
        constructor() { observers.push(this) }
        observe(): void { throw new Error('intersection observe should not be reached for eager hosts') }
        disconnect(): void {}
      }
      Object.defineProperty(window, 'IntersectionObserver', { configurable: true, value: FailingIntersectionObserver })
      Object.defineProperty(window, '__lvIntersectionObservers', { configurable: true, value: observers })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)

    const state = await page.evaluate(async () => {
      const source = (window as any).__lvSourceHosts.orders_kpi
      await source.updateComplete
      const authoring = document.createElement('lv-visualization-host') as any
      authoring.deferMount = true
      authoring.authoring = true
      authoring.envelope = JSON.parse(JSON.stringify(source.envelope))
      document.body.append(authoring)
      await authoring.updateComplete
      await new Promise<void>((resolve) => setTimeout(resolve, 0))
      return {
        defaultMounted: ((source.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0) > 0,
        authoringMounted: ((authoring.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0) > 0,
        observerCount: ((window as any).__lvIntersectionObservers as unknown[]).length,
      }
    })
    expect(state).toEqual({ defaultMounted: true, authoringMounted: true, observerCount: 0 })
  } finally {
    await page.close()
  }
})

test('a deferred host can be switched back to eager mounting without leaking its observer', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      const observers: Array<{ target?: Element; disconnected: boolean }> = []
      class DeferredIntersectionObserver {
        readonly record: { target?: Element; disconnected: boolean }
        constructor() {
          this.record = { disconnected: false }
          observers.push(this.record)
        }
        observe(target: Element): void { this.record.target = target }
        disconnect(): void { this.record.disconnected = true }
      }
      Object.defineProperty(window, 'IntersectionObserver', { configurable: true, value: DeferredIntersectionObserver })
      Object.defineProperty(window, '__lvIntersectionObservers', { configurable: true, value: observers })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)

    const state = await page.evaluate(async () => {
      const source = (window as any).__lvSourceHosts.orders_kpi
      const deferred = document.createElement('lv-visualization-host') as any
      deferred.deferMount = true
      deferred.envelope = JSON.parse(JSON.stringify(source.envelope))
      document.body.append(deferred)
      await deferred.updateComplete
      const record = ((window as any).__lvIntersectionObservers as Array<{ target?: Element; disconnected: boolean }>).find((candidate) => candidate.target === (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')) as any
      const before = (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0
      deferred.deferMount = false
      await deferred.updateComplete
      const deadline = Date.now() + 2_000
      while (((deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0) === 0 && Date.now() < deadline) {
        await new Promise<void>((resolve) => setTimeout(resolve, 0))
      }
      return { before, mounted: (deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0, disconnected: record.disconnected }
    })
    expect(state.before).toBe(0)
    expect(state.mounted).toBeGreaterThan(0)
    expect(state.disconnected).toBe(true)
  } finally {
    await page.close()
  }
})

test('dashboard hosts fall back to eager mounting when nested scroll margins are unsupported', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      const records: Array<{ disconnected: boolean }> = []
      class LegacyIntersectionObserver {
        readonly record = { disconnected: false }
        constructor() { records.push(this.record) }
        observe(): void {}
        disconnect(): void { this.record.disconnected = true }
      }
      Object.defineProperty(window, 'IntersectionObserver', { configurable: true, value: LegacyIntersectionObserver })
      Object.defineProperty(window, '__lvIntersectionObservers', { configurable: true, value: records })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)
    const state = await page.evaluate(async () => {
      const source = (window as any).__lvSourceHosts.orders_kpi
      const canvas = document.createElement('lv-report-canvas')
      const deferred = document.createElement('lv-visualization-host') as any
      deferred.deferMount = true
      deferred.envelope = JSON.parse(JSON.stringify(source.envelope))
      canvas.append(deferred)
      document.body.append(canvas)
      await deferred.updateComplete
      await new Promise<void>((resolve) => setTimeout(resolve, 0))
      return {
        mounted: ((deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0) > 0,
        disconnected: (window as any).__lvIntersectionObservers[0]?.disconnected,
      }
    })
    expect(state).toEqual({ mounted: true, disconnected: true })
  } finally {
    await page.close()
  }
})

for (const failureMode of ['missing', 'constructor', 'observe'] as const) {
  test(`deferred hosts fall back to eager mounting when IntersectionObserver ${failureMode}`, async () => {
    const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
    try {
      await page.addInitScript((mode) => {
        if (mode === 'missing') {
          Object.defineProperty(window, 'IntersectionObserver', { configurable: true, value: undefined })
          return
        }
        class FailingIntersectionObserver {
          constructor() { if (mode === 'constructor') throw new Error('intersection constructor failed') }
          observe(): void { if (mode === 'observe') throw new Error('intersection observe failed') }
          disconnect(): void {}
        }
        Object.defineProperty(window, 'IntersectionObserver', { configurable: true, value: FailingIntersectionObserver })
      }, failureMode)
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-visualization-host') && (window as any).__lvSourceHosts)

      const mounted = await page.evaluate(async () => {
        const source = (window as any).__lvSourceHosts.orders_kpi
        const deferred = document.createElement('lv-visualization-host') as any
        deferred.deferMount = true
        deferred.envelope = JSON.parse(JSON.stringify(source.envelope))
        document.body.append(deferred)
        await deferred.updateComplete
        await new Promise<void>((resolve) => setTimeout(resolve, 0))
        return ((deferred.shadowRoot as ShadowRoot).querySelector('.renderer')?.childElementCount ?? 0) > 0
      })
      expect(mounted).toBe(true)
    } finally {
      await page.close()
    }
  })
}
