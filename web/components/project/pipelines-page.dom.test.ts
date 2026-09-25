import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testDocument } from './project-page.dom.fixture'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/project-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(url.searchParams.get('root') ?? 'pipelines'))
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : root
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(file))
    } catch {
      response.writeHead(404)
      response.end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('pipeline catalog and run monitor expose URL-backed views and filters', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    await page.waitForFunction(() => customElements.get('lv-pipelines-page'))
    const catalog = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return { title: root.querySelector('h1')?.textContent?.trim(), eyebrow: root.querySelector('.page-eyebrow')?.textContent?.trim() ?? null, metrics: root.querySelectorAll('.metrics').length, tabs: root.querySelectorAll('.pipeline-tabs a').length, rows: root.querySelectorAll('lv-entity-list .entity-list-table-row').length,
        pipelineHref: root.querySelector('lv-entity-list .entity-list-identity')?.getAttribute('href'), description: root.querySelector('lv-entity-list .entity-list-description')?.textContent?.trim() ?? null,
        semanticModel: root.querySelector('[data-column="semanticModel"]')?.textContent?.replace(/^\s*Refreshes:\s*/, '').trim(), triggerHeader: root.querySelector('[data-column="trigger"] .entity-list-mobile-cell-label')?.textContent?.trim(), manualTriggerIsBlank: Boolean(root.querySelector('[data-column="trigger"] .pipeline-trigger-empty')), recentRunHeader: root.querySelector('[data-column="recentRuns"] .entity-list-mobile-cell-label')?.textContent?.trim(), recentRunHrefs: Array.from(root.querySelectorAll('[data-column="recentRuns"] a') as NodeListOf<Element>).map((link) => link.getAttribute('href')), recentRunStatuses: Array.from(root.querySelectorAll('[data-column="recentRuns"] a') as NodeListOf<Element>).map((link) => link.getAttribute('data-tone')), recentSlotOrder: Array.from(root.querySelectorAll('[data-column="recentRuns"] .recent-run') as NodeListOf<Element>).map((slot) => slot.getAttribute('data-tone') ?? '-'), emptySlots: root.querySelectorAll('[data-column="recentRuns"] .recent-run-empty').length,
        runLabel: root.querySelector('[data-column="actions"] button')?.getAttribute('aria-label'), runText: root.querySelector('[data-column="actions"] button')?.textContent?.trim(), activeTab: root.querySelector('.pipeline-tabs a[aria-current="page"]')?.textContent?.trim() }
})

    expect(catalog).toEqual({ title: 'Pipelines', eyebrow: null, metrics: 0, tabs: 2, rows: 1, pipelineHref: '/pipelines/pipeline:sales/details', description: null, semanticModel: 'Sales Semantic Model', triggerHeader: 'Trigger:', manualTriggerIsBlank: true, recentRunHeader: 'Recent runs:', recentRunHrefs: ['/pipelines/pipeline:sales/runs/run-failed', '/pipelines/pipeline:sales/runs/run-succeeded'], recentRunStatuses: ['danger', 'success'], recentSlotOrder: ['-', '-', '-', 'danger', 'success'], emptySlots: 3, runLabel: 'Run Sales refresh now', runText: '', activeTab: 'Pipelines' })
    const scheduled = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.pipelines[0].description = 'A description that must not replace the schedule'
      element.signals.page.pipelines[0].schedule = '0 * * * * · UTC'
      element.signals.page.pipelines[0].nextRun = '2026-09-23T14:00:00Z'
      element.requestUpdate()
      await element.updateComplete
      const trigger = element.shadowRoot.querySelector('[data-column="trigger"]')
      return { trigger: trigger?.textContent?.trim(), title: trigger?.getAttribute('title'), description: element.shadowRoot.querySelector('lv-entity-list .entity-list-description')?.textContent?.trim() ?? null }
    })
    expect(scheduled.trigger).toContain('Scheduled')
    expect(scheduled.title).toContain('0 * * * * · UTC')
    expect(scheduled.title).toContain('Next Sep 23, 2026, 2:00 PM UTC')
    expect(scheduled.description).toBeNull()

    const noRuns = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.pipelines[0].recentRuns = []
      element.requestUpdate()
      await element.updateComplete
      const cell = element.shadowRoot.querySelector('[data-column="recentRuns"]')
      return { placeholders: cell?.querySelectorAll('.recent-run-empty').length, links: cell?.querySelectorAll('a').length }
    })
    expect(noRuns).toEqual({ placeholders: 5, links: 0 })

    await page.getByRole('searchbox', { name: 'Search pipelines' }).fill('missing pipeline')
    expect(await page.getByRole('status', { name: '' }).filter({ hasText: 'No results match your search.' }).count()).toBe(1)
    await page.getByRole('searchbox', { name: 'Search pipelines' }).fill('sales')
    expect(await page.locator('lv-pipelines-page lv-entity-list .entity-list-table-row').count()).toBe(1)
    await page.locator('lv-pipelines-page').evaluate((element) => {
      element.addEventListener('lv-pipeline-command', (event: Event) => { (window as any).__pipelineCommand = (event as CustomEvent).detail }, { once: true })
    })
    await page.getByRole('button', { name: 'Run Sales refresh now' }).click()
    expect(await page.evaluate(() => (window as any).__pipelineCommand)).toEqual({ action: 'run', pipelineId: 'pipeline:sales', assetId: 'pipeline:sales', intentId: '', runId: '' })
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.pipelines[0].running = true
      element.requestUpdate()
      await element.updateComplete
    })
    expect(await page.getByRole('button', { name: 'Run Sales refresh now' }).isDisabled()).toBe(false)
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.pipelines[0].canRun = false
      element.requestUpdate()
      await element.updateComplete
    })
    expect(await page.getByRole('button', { name: 'Run Sales refresh now' }).count()).toBe(0)

    await page.goto(`${baseURL}/?root=runs`)
    await page.addStyleTag({ content: ':root { --base-text-weight-normal: 400; --base-text-weight-medium: 500; --base-text-weight-semibold: 600; }' })
    const monitor = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const form = root.querySelector('.run-toolbar') as HTMLFormElement
      return { title: root.querySelector('h1')?.textContent?.trim(), metrics: root.querySelectorAll('.metric').length, tabs: root.querySelectorAll('.pipeline-tabs a').length,
        activeTab: root.querySelector('.pipeline-tabs a[aria-current="page"]')?.textContent?.trim(),
        metricDetails: Array.from(root.querySelectorAll('.metric-detail') as NodeListOf<Element>).map((detail) => detail.textContent?.trim()),
        filters: root.querySelectorAll('.run-toolbar input, .run-toolbar select').length, action: form?.getAttribute('action'),
        range: (form?.querySelector('[name="range"]') as HTMLSelectElement)?.value,
        pipeline: (form?.querySelector('[name="pipeline"]') as HTMLSelectElement)?.value,
        status: (form?.querySelector('[name="status"]') as HTMLSelectElement)?.value,
        trigger: (form?.querySelector('[name="trigger"]') as HTMLSelectElement)?.value,
        clearHref: form?.querySelector('a')?.getAttribute('href'),
        pageLink: root.querySelector('.run-pagination a')?.getAttribute('href'),
        runHref: root.querySelector('.run-table .entity-list-identity')?.getAttribute('href'),
        failureReason: root.querySelector('.run-table [data-column="status"] .entity-list-cell-detail')?.textContent?.trim() ?? null,
        pipelineDetail: root.querySelector('.run-table [data-column="pipeline"] .entity-list-cell-detail')?.textContent?.trim() ?? null,
        cellWeights: Object.fromEntries(['time', 'pipeline', 'status', 'duration', 'trigger', 'name'].map((column) => {
          const cell = root.querySelector(`.run-table .entity-list-table-row [data-column="${column}"]`)
          const value = cell?.querySelector('.entity-list-column-link, .entity-list-status, .entity-list-title') ?? cell
          return [column, value ? getComputedStyle(value).fontWeight : null]
        })),
        headings: Array.from(root.querySelectorAll('.run-table .entity-list-table thead th') as NodeListOf<Element>).map((cell) => cell.textContent?.trim()),
        timeHref: root.querySelector('.run-table [data-column="time"] a[href="/pipelines/pipeline:sales/runs/run-failed"]')?.textContent?.trim(),
        pipelineHref: root.querySelector('.run-table a[href="/pipelines/pipeline:sales/details"]')?.getAttribute('href'),
        searchSubmission: (() => {
          (form?.querySelector('[name="q"]') as HTMLInputElement).value = 'revenue'
          return Object.fromEntries(new FormData(form!).entries())
        })() }
    })
    expect(monitor).toEqual({ title: 'Runs', metrics: 0, tabs: 2, activeTab: 'Runs', metricDetails: [], filters: 5, action: '/pipelines/runs', range: '7d', pipeline: 'pipeline:sales',
      status: 'failed', trigger: 'manual', searchSubmission: { q: 'revenue', range: '7d', pipeline: 'pipeline:sales', status: 'failed', trigger: 'manual' },
      clearHref: '/pipelines/runs',
      pageLink: '/pipelines/runs?q=sales&range=7d&pipeline=pipeline%3Asales&status=failed&trigger=manual&page=1',
      runHref: null, headings: ['Start time', 'Pipeline', 'Status', 'Duration', 'Trigger', 'Run / request ID', ''],
      timeHref: 'Sep 13, 12:00 UTC', pipelineHref: '/pipelines/pipeline:sales/details', failureReason: null, pipelineDetail: null,
      cellWeights: { time: '400', pipeline: '400', status: '400', duration: '400', trigger: '400', name: '400' } })
    expect(await page.locator('lv-pipelines-page lv-pipeline-runs-list').count()).toBe(1)
    expect(await page.locator('lv-pipelines-page lv-drawer').count()).toBe(0)
  } finally {
    await page.close()
  }
}, 15_000)

test('active runs spin while queued runs remain static and reduced motion is respected', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    const recent = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.pipelines[0].recentRuns = [
        { id: 'queued', status: 'queued', href: '/queued' },
        { id: 'running', status: 'running', href: '/running' },
        { id: 'publishing', status: 'prepared', href: '/publishing' },
      ]
      element.requestUpdate()
      await element.updateComplete
      const root = element.shadowRoot!
      const animation = (status: string) => getComputedStyle(root.querySelector(`[data-status="${status}"] svg`)!).animationName
      return { queued: animation('queued'), running: animation('running'), publishing: animation('prepared'), publishingLabel: root.querySelector('[data-status="prepared"]')?.getAttribute('aria-label') }
    })
    expect(recent.queued).toBe('none')
    expect(recent.running).toBe('recent-run-spin')
    expect(recent.publishing).toBe('recent-run-spin')
    expect(recent.publishingLabel).toContain('Running run')
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => { element.streamState = 'reconnecting'; await element.updateComplete })
    expect(await page.locator('lv-pipelines-page [data-status="running"] svg').evaluate((icon) => getComputedStyle(icon).animationName)).toBe('none')
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => { element.streamState = 'live'; await element.updateComplete })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    expect(await page.locator('lv-pipelines-page [data-status="running"] svg').evaluate((icon) => getComputedStyle(icon).animationName)).toBe('none')

    await page.goto(`${baseURL}/?root=runs`)
    const listed = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.runsTable = { ...element.signals.page.runsTable, rows: [{ ...element.signals.page.runsTable.rows[0], status_value: 'prepared' }] }
      element.requestUpdate()
      await element.updateComplete
      const root = element.shadowRoot!
      await (root.querySelector('lv-pipeline-runs-list') as any).updateComplete
      return { label: root.querySelector('.entity-list-status')?.textContent?.trim(), filterOptions: [...root.querySelectorAll('select[name="status"] option')].map((option) => option.textContent?.trim()) }
    })
    expect(listed.label).toBe('Running')
    expect(listed.filterOptions).not.toContain('Finalizing')
    expect(listed.filterOptions.filter((label) => label === 'Running')).toHaveLength(1)
    expect(await page.locator('lv-pipelines-page .entity-list-status.is-running svg').evaluate((icon) => getComputedStyle(icon).animationName)).toBe('none')
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    expect(await page.locator('lv-pipelines-page .entity-list-status.is-running svg').evaluate((icon) => getComputedStyle(icon).animationName)).toBe('entity-list-status-spin')
    await page.setViewportSize({ width: 390, height: 844 })
    expect(await page.locator('lv-pipelines-page [data-column="time"] .run-time-status svg').evaluate((icon) => getComputedStyle(icon).animationName)).toBe('entity-list-status-spin')
    await page.locator('lv-pipelines-page lv-pipeline-runs-list').evaluate(async (element: any) => { element.live = false; await element.updateComplete })
    expect(await page.locator('lv-pipelines-page .entity-list-status.is-running svg').evaluate((icon) => getComputedStyle(icon).animationName)).toBe('none')
    expect(await page.locator('lv-pipelines-page [data-column="time"] .run-time-status svg').evaluate((icon) => getComputedStyle(icon).animationName)).toBe('none')
  } finally {
    await page.close()
  }
}, 15_000)

test('run history colors status icons only, with a blue queued icon', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const colors = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.style.setProperty('--lv-fg-default', '#c9d1d9')
      element.style.setProperty('--lv-fg-accent', '#58a6ff')
      element.style.setProperty('--lv-fg-warning', '#d29922')
      element.style.setProperty('--lv-fg-success', '#3fb950')
      element.style.setProperty('--lv-fg-danger', '#f85149')
      const template = element.signals.page.runsTable.rows[0]
      element.signals.page.runsTable.rows = ['queued', 'running', 'succeeded', 'failed'].map((status) => ({
        ...template, id: `run-${status}`, run_id: `run-${status}`, status_value: status,
      }))
      element.requestUpdate()
      await element.updateComplete
      await element.shadowRoot.querySelector('lv-pipeline-runs-list').updateComplete
      await element.shadowRoot.querySelector('lv-entity-list').updateComplete
      return Object.fromEntries([...element.shadowRoot.querySelectorAll('.entity-list-status')].map((status: Element) => [
        status.textContent?.trim(), {
          text: getComputedStyle(status).color,
          icon: getComputedStyle(status.querySelector('.entity-list-status-icon')!).color,
        },
      ]))
    })
    expect(colors).toEqual({
      Queued: { text: 'rgb(201, 209, 217)', icon: 'rgb(88, 166, 255)' },
      Running: { text: 'rgb(201, 209, 217)', icon: 'rgb(210, 153, 34)' },
      Succeeded: { text: 'rgb(201, 209, 217)', icon: 'rgb(63, 185, 80)' },
      Failed: { text: 'rgb(201, 209, 217)', icon: 'rgb(248, 81, 73)' },
    })
  } finally {
    await page.close()
  }
})

test('run duration counts live, pauses on disconnect, and yields to recorded completion', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const host = page.locator('lv-pipelines-page')
    await host.evaluate(async (element: any) => {
      const startedAt = new Date(Date.now() - 3200).toISOString()
      element.signals.page.runsTable = { ...element.signals.page.runsTable, rows: [{ ...element.signals.page.runsTable.rows[0], status_value: 'running', started_at: startedAt, finished_at: '-', duration: '-' }] }
      element.requestUpdate()
      await element.updateComplete
      await element.shadowRoot.querySelector('lv-pipeline-runs-list').updateComplete
    })
    const duration = page.locator('lv-pipelines-page .entity-list-table-row [data-column="duration"]')
    const seconds = async () => Number.parseInt((await duration.innerText()).trim(), 10)
    const initial = await seconds()
    expect(initial).toBeGreaterThanOrEqual(3)
    await page.waitForTimeout(1200)
    expect(await seconds()).toBeGreaterThan(initial)

    await host.evaluate(async (element: any) => {
      element.streamState = 'reconnecting'
      await element.updateComplete
      await element.shadowRoot.querySelector('lv-pipeline-runs-list').updateComplete
    })
    const frozen = await duration.innerText()
    await page.waitForTimeout(1200)
    expect(await duration.innerText()).toBe(frozen)

    await host.evaluate(async (element: any) => {
      element.streamState = 'live'
      await element.updateComplete
      await element.shadowRoot.querySelector('lv-pipeline-runs-list').updateComplete
    })
    expect(await seconds()).toBeGreaterThan(Number.parseInt(frozen.trim(), 10))
    await host.evaluate(async (element: any) => {
      element.signals.page.runsTable = { ...element.signals.page.runsTable, rows: [{ ...element.signals.page.runsTable.rows[0], status_value: 'succeeded', finished_at: new Date().toISOString(), duration: '6s' }] }
      element.requestUpdate()
      await element.updateComplete
      await element.shadowRoot.querySelector('lv-pipeline-runs-list').updateComplete
    })
    expect((await duration.innerText()).trim()).toBe('6s')
    await page.waitForTimeout(1200)
    expect((await duration.innerText()).trim()).toBe('6s')
  } finally {
    await page.close()
  }
}, 15_000)

test('pipeline rows stay compact and readable on a narrow mobile viewport', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    const row = page.locator('lv-pipelines-page lv-entity-list .entity-list-table-row')
    if (await row.count() !== 1) throw new Error('expected one pipeline row on mobile')
    const state = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const wrap = root.querySelector('.entity-list-table-wrap') as HTMLElement
      const semantic = root.querySelector('[data-column="semanticModel"]') as HTMLElement
      const trigger = root.querySelector('[data-column="trigger"]') as HTMLElement
      const publication = root.querySelector('[data-column="lastPublished"]') as HTMLElement
      const action = root.querySelector('[data-column="actions"] button') as HTMLElement
      const name = root.querySelector('.entity-list-table-row th') as HTMLElement
      const recentCell = root.querySelector('[data-column="recentRuns"]') as HTMLElement
      const lastSlot = recentCell.querySelector('.recent-run:last-child') as HTMLElement
      return {
        documentWidth: document.documentElement.scrollWidth,
        viewportWidth: document.documentElement.clientWidth,
        tableWidth: wrap.scrollWidth,
        tableClientWidth: wrap.clientWidth,
        semanticDisplay: getComputedStyle(semantic).display,
        triggerDisplay: getComputedStyle(trigger).display,
        actionTop: action.getBoundingClientRect().top,
        nameTop: name.getBoundingClientRect().top,
        name: root.querySelector('lv-entity-list .entity-list-identity')?.textContent?.trim(),
        recentRuns: root.querySelectorAll('[data-column="recentRuns"] .recent-run').length,
        recentCellRight: recentCell.getBoundingClientRect().right,
        lastSlotRight: lastSlot.getBoundingClientRect().right,
        publication: publication.textContent?.replace(/\s+/g, ' ').trim(),
        publicationLabel: publication.querySelector('.entity-list-mobile-cell-label')?.textContent?.trim(),
      }
    })
    expect(state.documentWidth).toBeLessThanOrEqual(state.viewportWidth)
    expect(state.tableWidth).toBeLessThanOrEqual(state.tableClientWidth)
    expect(state.semanticDisplay).toBe('block')
    expect(state.triggerDisplay).toBe('none')
    expect(Math.abs(state.actionTop - state.nameTop)).toBeLessThan(20)
    expect(state.name).toContain('Sales refresh')
    expect(state.recentRuns).toBe(5)
    expect(state.recentCellRight - state.lastSlotRight).toBeLessThanOrEqual(12)
    expect(state.publicationLabel).toContain('Last published')
    expect(state.publication).not.toBe('')
    expect(await page.getByRole('button', { name: 'Run Sales refresh now' }).isVisible()).toBe(true)
  } finally {
    await page.close()
  }
}, 15_000)

test('pipeline collection keeps publication and run action visible at a 1207px desktop viewport', async () => {
  const page = await browser.newPage({ viewport: { width: 1207, height: 900 } })
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    const state = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const wrap = root.querySelector('.entity-list-table-wrap') as HTMLElement
      const action = root.querySelector('[data-column="actions"] button') as HTMLElement
      const publication = root.querySelector('[data-column="lastPublished"]') as HTMLElement
      const recentCell = root.querySelector('[data-column="recentRuns"]') as HTMLElement
      const lastSlot = recentCell.querySelector('.recent-run:last-child') as HTMLElement
      return { tableWidth: wrap.scrollWidth, availableWidth: wrap.clientWidth, actionRight: action.getBoundingClientRect().right, viewportRight: document.documentElement.clientWidth, publication: publication.textContent?.trim(), recentCellRight: recentCell.getBoundingClientRect().right, lastSlotRight: lastSlot.getBoundingClientRect().right }
    })
    expect(state.tableWidth).toBeLessThanOrEqual(state.availableWidth)
    expect(state.actionRight).toBeLessThanOrEqual(state.viewportRight)
    expect(state.publication).toContain('UTC')
    expect(state.recentCellRight - state.lastSlotRight).toBeLessThanOrEqual(12)
  } finally {
    await page.close()
  }
}, 15_000)

test('queued manual requests use recent-run slots on the collection and remain actionable in Runs', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    const collection = page.locator('lv-pipelines-page')
    await collection.evaluate(async (element: any) => {
      element.signals.page.waitingIntents = [
        { intentId: 'request:queued', pipelineId: 'pipeline:sales', status: 'waiting', createdAt: '2026-09-24T09:00:00Z', queuePosition: 2, cancelAllowed: true },
        { intentId: 'request:attached', pipelineId: 'pipeline:sales', status: 'attached', createdAt: '2026-09-24T08:00:00Z', queuePosition: 1, runId: 'run-succeeded', cancelAllowed: false },
      ]
      element.requestUpdate()
      await element.updateComplete
      await (element.shadowRoot.querySelector('lv-entity-list') as any).updateComplete
    })
    const desktop = await collection.evaluate((element: any) => {
      const root = element.shadowRoot as ShadowRoot
      const cell = root.querySelector('[data-column="recentRuns"]') as HTMLElement
      const wrap = root.querySelector('.entity-list-table-wrap') as HTMLElement
      return {
        queuedColumn: root.querySelectorAll('[data-column="waitingRequest"]').length,
        slots: cell.querySelectorAll('.recent-run').length,
        queuedSlots: cell.querySelectorAll('.recent-run[data-status="queued"]').length,
        queuedHref: cell.querySelector('.recent-run[data-status="queued"]')?.getAttribute('href'),
        queuedLabel: cell.querySelector('.recent-run[data-status="queued"]')?.getAttribute('aria-label'),
        tableFits: wrap.scrollWidth <= wrap.clientWidth,
      }
    })
    expect(desktop).toEqual({ queuedColumn: 0, slots: 5, queuedSlots: 1, queuedHref: '/pipelines/pipeline%3Asales/runs', queuedLabel: 'Queued request request:queued for Sales refresh', tableFits: true })

    await page.setViewportSize({ width: 390, height: 844 })
    const mobile = await collection.evaluate(async (element: any) => {
      await element.updateComplete
      await (element.shadowRoot.querySelector('lv-entity-list') as any).updateComplete
      const root = element.shadowRoot as ShadowRoot
      const row = root.querySelector('.entity-list-table-row') as HTMLElement
      const cell = row.querySelector('[data-column="recentRuns"]') as HTMLElement
      return {
        documentFits: document.documentElement.scrollWidth <= document.documentElement.clientWidth,
        rowVisible: row.getBoundingClientRect().width > 0,
        queuedColumn: root.querySelectorAll('[data-column="waitingRequest"]').length,
        queuedVisible: Boolean(cell.querySelector('.recent-run[data-status="queued"]')?.getBoundingClientRect().width),
      }
    })
    expect(mobile.documentFits).toBe(true)
    expect(mobile.rowVisible).toBe(true)
    expect(mobile.queuedColumn).toBe(0)
    expect(mobile.queuedVisible).toBe(true)

    await page.goto(`${baseURL}/?root=runs`)
    const runs = page.locator('lv-pipelines-page')
    await runs.evaluate(async (element: any) => {
      element.signals.page.runMonitor = { ...element.signals.page.runMonitor, query: '', range: 'all', status: '', page: 1 }
      element.signals.page.waitingIntents = [
        { intentId: 'request:queued', pipelineId: 'pipeline:sales', status: 'waiting', createdAt: '2026-09-24T09:00:00Z', queuePosition: 2, cancelAllowed: true },
        { intentId: 'request:attached', pipelineId: 'pipeline:sales', status: 'attached', createdAt: '2026-09-24T08:00:00Z', queuePosition: 1, runId: 'run-failed', cancelAllowed: false },
      ]
      element.requestUpdate()
      await element.updateComplete
      await (element.shadowRoot.querySelector('lv-pipeline-runs-list') as any).updateComplete
    })
    expect(await runs.locator('.waiting-requests').count()).toBe(0)
    const rows = runs.locator('.run-table .entity-list-table-row')
    expect(await rows.count()).toBe(2)
    expect(await rows.first().locator('[data-column="status"]').textContent()).toContain('Queued')
    expect(await rows.first().locator('[data-column="status"] .entity-list-cell-detail').count()).toBe(0)
    expect(await rows.first().locator('[data-column="time"]').innerText()).toBe('')
    expect(await rows.first().getByRole('button', { name: 'Cancel queued request request:queued' }).count()).toBe(1)
    expect(await rows.nth(1).textContent()).toContain('run-failed')
    await runs.evaluate((element: any) => {
      element.addEventListener('lv-pipeline-command', (event: CustomEvent) => { (window as any).__runsCancelIntent = event.detail }, { once: true })
    })
    await rows.first().getByRole('button', { name: 'Cancel queued request request:queued' }).click()
    expect(await page.evaluate(() => (window as any).__runsCancelIntent)).toEqual({ action: 'cancel-intent', pipelineId: 'pipeline:sales', assetId: 'pipeline:sales', intentId: 'request:queued', runId: '' })
  } finally {
    await page.close()
  }
}, 15_000)

test('active rows stay above history in queue order without claiming a start-time sort', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const host = page.locator('lv-pipelines-page')
    await host.evaluate(async (element: any) => {
      const template = element.signals.page.runsTable.rows[0]
      element.signals.page.runMonitor = { ...element.signals.page.runMonitor, query: '', range: 'all', pipeline: '', status: '', trigger: '', page: 1 }
      element.signals.page.runsTable = { ...element.signals.page.runsTable, rows: [
        { ...template, run_id: 'run-old', run: 'run-old', status_value: 'succeeded', started_at: '2026-09-24T08:00:00Z', created_at: '2026-09-24T07:59:00Z' },
        { ...template, run_id: 'run-new', run: 'run-new', status_value: 'succeeded', started_at: '2026-09-24T09:00:00Z', created_at: '2026-09-24T08:59:00Z' },
        { ...template, run_id: 'run-active', run: 'run-active', status_value: 'running', started_at: '2026-09-24T10:00:00Z', created_at: '2026-09-24T09:59:00Z' },
      ] }
      element.signals.page.waitingIntents = [
        { intentId: '01a0d497-aaaa-4aaa-8aaa-000000000001', pipelineId: 'pipeline:sales', status: 'waiting', createdAt: '2026-09-24T09:00:00Z', queuePosition: 1, cancelAllowed: true },
        { intentId: '01a0d497-bbbb-4bbb-8bbb-000000000002', pipelineId: 'pipeline:sales', status: 'waiting', createdAt: '2026-09-24T09:01:00Z', queuePosition: 2, cancelAllowed: true },
      ]
      element.requestUpdate()
      await element.updateComplete
      await (element.shadowRoot.querySelector('lv-pipeline-runs-list') as any).updateComplete
    })
    const rows = host.locator('.run-table .entity-list-table-row')
    expect((await rows.locator('[data-column="name"] .entity-list-title').allInnerTexts()).map((value) => value.trim())).toEqual([
      'Request 01a0d497…000002', 'Request 01a0d497…000001', 'run-active', 'run-new', 'run-old',
    ])
    expect((await rows.locator('[data-column="time"]').allInnerTexts()).map((value) => value.trim())).toEqual(['', '', 'Sep 24, 10:00 UTC', 'Sep 24, 09:00 UTC', 'Sep 24, 08:00 UTC'])
    expect(await rows.locator('[data-column="status"] .entity-list-cell-detail').count()).toBe(0)
    const timeHeader = host.locator('.run-table .entity-list-table thead th').first()
    expect(await timeHeader.getAttribute('aria-sort')).toBe('none')
    await timeHeader.getByRole('button', { name: /Sort by Start time/ }).click()
    expect(await timeHeader.getAttribute('aria-sort')).toBe('ascending')
    expect((await rows.locator('[data-column="time"]').allInnerTexts()).map((value) => value.trim())).toEqual([
      'Sep 24, 08:00 UTC', 'Sep 24, 09:00 UTC', 'Sep 24, 10:00 UTC', '', '',
    ])
    await timeHeader.getByRole('button', { name: /Sort by Start time/ }).click()
    expect(await timeHeader.getAttribute('aria-sort')).toBe('descending')
    expect((await rows.locator('[data-column="time"]').allInnerTexts()).map((value) => value.trim())).toEqual([
      'Sep 24, 10:00 UTC', 'Sep 24, 09:00 UTC', 'Sep 24, 08:00 UTC', '', '',
    ])
  } finally {
    await page.close()
  }
}, 15_000)

test('stale manual requests appear as table outcomes without becoming runs', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const host = page.locator('lv-pipelines-page')
    await host.evaluate(async (element: any) => {
      element.signals.page.runMonitor = { ...element.signals.page.runMonitor, query: '', range: 'all', status: '', page: 1 }
      element.signals.page.waitingIntents = [
        { intentId: 'request:stale', pipelineId: 'pipeline:sales', status: 'stale', createdAt: '2026-09-24T09:00:00Z', cancelAllowed: false },
      ]
      element.requestUpdate()
      await element.updateComplete
      await (element.shadowRoot.querySelector('lv-pipeline-runs-list') as any).updateComplete
    })
    expect(await host.locator('.waiting-requests').count()).toBe(0)
    const rows = host.locator('.run-table .entity-list-table-row')
    expect(await rows.count()).toBe(2)
    expect(await rows.first().locator('[data-column="status"]').textContent()).toContain('Stale request')
    expect(await rows.first().locator('[data-column="status"]').textContent()).toContain('Pipeline definition changed while waiting')
    expect(await rows.first().textContent()).toContain('Pipeline definition changed while waiting; start a new request')
    expect(await rows.first().getByRole('button').count()).toBe(0)
    expect(await rows.nth(1).textContent()).toContain('run-failed')
  } finally {
    await page.close()
  }
}, 15_000)

test('request rows follow run filters and stay within the mobile table', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const host = page.locator('lv-pipelines-page')
    await host.evaluate(async (element: any) => {
      element.signals.page.runMonitor = { ...element.signals.page.runMonitor, query: '', range: 'all', pipeline: '', status: '', trigger: '', page: 1 }
      element.signals.page.waitingIntents = [{ intentId: 'request:filtered', pipelineId: 'pipeline:sales', status: 'waiting', createdAt: new Date().toISOString(), queuePosition: 1, cancelAllowed: true }]
      element.requestUpdate()
      await element.updateComplete
      await (element.shadowRoot.querySelector('lv-pipeline-runs-list') as any).updateComplete
    })
    const request = host.locator('.run-table .entity-list-table-row').first()
    expect(await request.textContent()).toContain('Request request:')
    expect(await request.getAttribute('class')).not.toContain('is-actionable')
    const mobileStatus = request.locator('[data-column="time"] .run-time-status')
    expect(await mobileStatus.count()).toBe(1)
    expect(await mobileStatus.evaluate((icon) => getComputedStyle(icon).display)).toBe('inline-flex')
    expect(await request.locator('[data-column="time"]').innerText()).toBe('')
    expect(await request.locator('[data-column="time"]').evaluate((cell) => cell.scrollWidth <= cell.clientWidth)).toBe(true)
    const mobile = await host.evaluate((element: any) => {
      const wrap = element.shadowRoot.querySelector('.run-table .entity-list-table-wrap') as HTMLElement
      return { documentFits: document.documentElement.scrollWidth <= document.documentElement.clientWidth, tableScrolls: wrap.scrollWidth > wrap.clientWidth }
    })
    expect(mobile).toEqual({ documentFits: true, tableScrolls: true })

    for (const filter of [{ status: 'running' }, { trigger: 'schedule' }, { pipeline: 'pipeline:other' }, { query: 'no-match' }, { page: 2 }]) {
      await host.evaluate(async (element: any, patch: any) => {
        element.signals.page.runMonitor = { ...element.signals.page.runMonitor, query: '', pipeline: '', status: '', trigger: '', page: 1, ...patch }
        element.requestUpdate()
        await element.updateComplete
        await (element.shadowRoot.querySelector('lv-pipeline-runs-list') as any).updateComplete
      }, filter)
      expect(await host.locator('.run-table .entity-list-title').filter({ hasText: 'Request request:' }).count()).toBe(0)
    }
  } finally {
    await page.close()
  }
}, 15_000)

test('shared run history preserves global actions and mobile table containment', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const state = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      element.signals.page.runsTable.rows[0].actions = [{ label: 'Cancel run', action: 'cancel', icon: 'cancel' }]
      element.requestUpdate()
      await element.updateComplete
      const list = element.shadowRoot!.querySelector('lv-pipeline-runs-list') as any
      await list.updateComplete
      const table = list.querySelector('lv-entity-list') as any
      await table.updateComplete
      const wrap = list.querySelector('.entity-list-table-wrap') as HTMLElement
      return {
        actionLabel: list.querySelector('.entity-list-row-action')?.getAttribute('aria-label'),
        documentWidth: document.documentElement.scrollWidth,
        viewportWidth: document.documentElement.clientWidth,
        tableWidth: wrap.scrollWidth,
        tableClientWidth: wrap.clientWidth,
      }
    })
    expect(state.actionLabel).toBe('Cancel run')
    expect(state.documentWidth).toBeLessThanOrEqual(state.viewportWidth)
    expect(state.tableWidth).toBeGreaterThan(state.tableClientWidth)
    await page.locator('lv-pipelines-page').evaluate((element: any) => {
      element.addEventListener('lv-pipeline-command', (event: CustomEvent) => { (window as any).__runCommand = event.detail }, { once: true })
      element.shadowRoot!.querySelector('.entity-list-row-action').click()
    })
    expect(await page.evaluate(() => (window as any).__runCommand)).toEqual({ action: 'cancel', pipelineId: 'pipeline:sales', assetId: 'pipeline:sales', intentId: '', runId: 'run-failed' })
  } finally {
    await page.close()
  }
}, 15_000)

test('run history omits the run-again action from terminal rows', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const host = page.locator('lv-pipelines-page')
    await host.evaluate(async (element: any) => {
      element.signals.page.runsTable.rows[0].actions = [
        { label: 'View run details', action: 'detail', icon: 'details' },
        { label: 'Run again', action: 'run', icon: 'refresh' },
      ]
      element.requestUpdate()
      await element.updateComplete
      await (element.shadowRoot.querySelector('lv-pipeline-runs-list') as any).updateComplete
    })
    expect(await host.getByRole('button', { name: 'Run again' }).count()).toBe(0)
    expect(await host.locator('.run-table .entity-list-row-action').count()).toBe(0)
  } finally {
    await page.close()
  }
}, 15_000)

test('run timestamps identify UTC even when the browser uses another timezone', async () => {
  const page = await browser.newPage({ timezoneId: 'America/Los_Angeles' })
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const started = page.locator('lv-pipelines-page .run-table .entity-list-cell a[href="/pipelines/pipeline:sales/runs/run-failed"]')
    expect((await started.textContent())?.trim()).toBe('Sep 13, 12:00 UTC')
  } finally {
    await page.close()
  }
}, 15_000)

test('run monitor stops claiming live updates while its page stream reconnects', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=runs`)
    const host = page.locator('lv-pipelines-page')
    await host.evaluate(async (element: any) => element.updateComplete)
    const eyebrow = host.locator('.page-eyebrow')
    expect(await eyebrow.count()).toBe(0)
    await page.evaluate(() => {
      const main = document.querySelector('main')!
      main.setAttribute('data-init', '0')
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retrying', el: main } }))
    })
    await page.getByRole('alert').filter({ hasText: 'Reconnecting to run updates' }).waitFor()
    await page.evaluate(() => {
      document.dispatchEvent(new CustomEvent('datastar-signal-patch', { detail: { runtime: { streamInstanceId: 'reconnected-monitor' } } }))
    })
    await page.waitForFunction(() => !document.querySelector('lv-pipelines-page')?.shadowRoot?.querySelector('[role="alert"]'))
    await page.evaluate(() => {
      const main = document.querySelector('main')!
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retries-failed', el: main } }))
    })
    await page.getByRole('alert').filter({ hasText: 'Run updates disconnected' }).waitFor()
  } finally {
    await page.close()
  }
}, 15_000)

test('pipeline command acknowledgement uses a dismissible shared toast, not a persistent banner', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    await page.getByRole('button', { name: 'Run Sales refresh now' }).click()
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.pipelineCommandStatus.loading = true
      await element.updateComplete
    })
    expect(await page.locator('lv-pipelines-page .command-feedback').count()).toBe(0)
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.waitingIntents = [{ intentId: 'request:queued', pipelineId: 'pipeline:sales', status: 'waiting', createdAt: new Date().toISOString(), queuePosition: 1, cancelAllowed: true }]
      element.signals.pipelineCommandStatus.loading = false
      element.signals.pipelineCommandStatus.message = 'Pipeline command accepted.'
      await element.updateComplete
    })
    const toast = page.locator('lv-toast-region lv-toast')
    await toast.waitFor()
    expect((await toast.locator('.message').textContent())?.trim()).toBe('Run queued')
    expect(await page.locator('lv-pipelines-page [data-column="recentRuns"] .recent-run[data-status="queued"]').count()).toBe(1)
    expect(await page.locator('lv-pipelines-page .command-feedback').count()).toBe(0)
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => { element.requestUpdate(); await element.updateComplete })
    expect(await toast.count()).toBe(1)
    await toast.getByRole('button', { name: 'Dismiss notification' }).click()
    expect(await toast.count()).toBe(0)
    await page.getByRole('button', { name: 'Run Sales refresh now' }).click()
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.pipelineCommandStatus.loading = true
      await element.updateComplete
      element.signals.pipelineCommandStatus.loading = false
      await element.updateComplete
    })
    expect((await toast.locator('.message').textContent())?.trim()).toBe('Run queued')
  } finally {
    await page.close()
  }
})

test('shared toasts pause on hover and keep actions available until dismissed', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 700 } })
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    await page.evaluate(() => {
      const region = document.createElement('lv-toast-region') as any
      region.id = 'toast-test-region'
      document.body.append(region)
      region.show({ message: 'Brief update', durationMs: 500 })
    })
    const region = page.locator('#toast-test-region')
    const brief = region.locator('lv-toast').filter({ hasText: 'Brief update' })
    await brief.hover()
    await page.waitForTimeout(650)
    expect(await brief.count()).toBe(1)
    await page.mouse.move(0, 500)
    await page.waitForFunction(() => !document.querySelector('#toast-test-region')?.shadowRoot?.querySelector('lv-toast'))
    await page.evaluate(() => {
      ;(window as any).__toastAction = false
      ;(document.querySelector('#toast-test-region') as any).show({ message: 'Review run', durationMs: 50, action: { label: 'Open', onClick: () => { (window as any).__toastAction = true } } })
    })
    await page.waitForTimeout(150)
    const action = region.locator('lv-toast').filter({ hasText: 'Review run' })
    expect(await action.count()).toBe(1)
    const bounds = await action.boundingBox()
    expect(bounds?.x ?? -1).toBeGreaterThanOrEqual(0)
    expect((bounds?.x ?? 0) + (bounds?.width ?? 0)).toBeLessThanOrEqual(390)
    await action.getByRole('button', { name: 'Open' }).click()
    expect(await page.evaluate(() => (window as any).__toastAction)).toBe(true)
    expect(await action.count()).toBe(0)
  } finally {
    await page.close()
  }
})
