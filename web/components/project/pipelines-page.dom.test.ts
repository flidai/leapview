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
        semanticModel: root.querySelector('[data-column="semanticModel"]')?.textContent?.replace(/^\s*Refreshes:\s*/, '').trim(), schedule: root.querySelector('[data-column="schedule"]')?.textContent?.replace(/^\s*Schedule:\s*/, '').trim(), statusHref: root.querySelector('[data-column="status"] a')?.getAttribute('href'),
        runLabel: root.querySelector('[data-column="actions"] button')?.getAttribute('aria-label'), runText: root.querySelector('[data-column="actions"] button')?.textContent?.trim(), activeTab: root.querySelector('.pipeline-tabs a[aria-current="page"]')?.textContent?.trim() }
    })
    expect(catalog).toEqual({ title: 'Pipelines', eyebrow: null, metrics: 0, tabs: 2, rows: 1, pipelineHref: '/pipelines/pipeline:sales/details', description: null, semanticModel: 'Sales Semantic Model', schedule: 'Manual', statusHref: '/pipelines/pipeline:sales/runs/run-succeeded', runLabel: 'Run Sales refresh now', runText: '', activeTab: 'Pipelines' })
    const schedule = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.pipelines[0].description = 'A description that must not replace the schedule'
      element.signals.page.pipelines[0].schedule = '0 * * * * · UTC'
      element.signals.page.pipelines[0].nextRun = '2026-09-23T14:00:00Z'
      element.requestUpdate()
      await element.updateComplete
      return { schedule: element.shadowRoot.querySelector('[data-column="schedule"]')?.textContent?.trim(), description: element.shadowRoot.querySelector('lv-entity-list .entity-list-description')?.textContent?.trim() ?? null }
    })
    expect(schedule.schedule).toContain('0 * * * * · UTC')
    expect(schedule.schedule).toContain('Next Sep 23, 2026, 2:00 PM UTC')
    expect(schedule.description).toBeNull()

    await page.getByRole('searchbox', { name: 'Search pipelines' }).fill('missing pipeline')
    expect(await page.getByRole('status', { name: '' }).filter({ hasText: 'No results match your search.' }).count()).toBe(1)
    await page.getByRole('searchbox', { name: 'Search pipelines' }).fill('sales')
    expect(await page.locator('lv-pipelines-page lv-entity-list .entity-list-table-row').count()).toBe(1)
    await page.locator('lv-pipelines-page').evaluate((element) => {
      element.addEventListener('lv-pipeline-command', (event: Event) => { (window as any).__pipelineCommand = (event as CustomEvent).detail }, { once: true })
    })
    await page.getByRole('button', { name: 'Run Sales refresh now' }).click()
    expect(await page.evaluate(() => (window as any).__pipelineCommand)).toEqual({ action: 'run', pipelineId: 'pipeline:sales', assetId: 'pipeline:sales', runId: '' })
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.pipelines[0].running = true
      element.requestUpdate()
      await element.updateComplete
    })
    expect(await page.getByRole('button', { name: 'Run Sales refresh now' }).isDisabled()).toBe(true)
    await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.signals.page.pipelines[0].canRun = false
      element.requestUpdate()
      await element.updateComplete
    })
    expect(await page.getByRole('button', { name: 'Run Sales refresh now' }).count()).toBe(0)

    await page.goto(`${baseURL}/?root=runs`)
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
        failureReason: root.querySelector('.run-table .entity-list-cell-detail')?.textContent?.trim(),
        startedHref: root.querySelector('.run-table .entity-list-cell a[href="/pipelines/pipeline:sales/runs/run-failed"]')?.textContent?.trim(),
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
      runHref: null, startedHref: 'Sep 13, 12:00 UTC', pipelineHref: '/pipelines/pipeline:sales/details', failureReason: 'Source unavailable' })
    expect(await page.locator('lv-pipelines-page lv-drawer').count()).toBe(0)
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
      const schedule = root.querySelector('[data-column="schedule"]') as HTMLElement
      const publication = root.querySelector('[data-column="lastPublished"]') as HTMLElement
      const action = root.querySelector('[data-column="actions"] button') as HTMLElement
      const name = root.querySelector('.entity-list-table-row th') as HTMLElement
      return {
        documentWidth: document.documentElement.scrollWidth,
        viewportWidth: document.documentElement.clientWidth,
        tableWidth: wrap.scrollWidth,
        tableClientWidth: wrap.clientWidth,
        semanticDisplay: getComputedStyle(semantic).display,
        schedule: schedule.textContent?.replace(/\s+/g, ' ').trim(),
        actionTop: action.getBoundingClientRect().top,
        nameTop: name.getBoundingClientRect().top,
        name: root.querySelector('lv-entity-list .entity-list-identity')?.textContent?.trim(),
        status: root.querySelector('[data-column="status"] .entity-list-status')?.textContent?.trim(),
        publication: publication.textContent?.replace(/\s+/g, ' ').trim(),
        publicationLabel: publication.querySelector('.entity-list-mobile-cell-label')?.textContent?.trim(),
      }
    })
    expect(state.documentWidth).toBeLessThanOrEqual(state.viewportWidth)
    expect(state.tableWidth).toBeLessThanOrEqual(state.tableClientWidth)
    expect(state.semanticDisplay).toBe('block')
    expect(state.schedule).toContain('Manual')
    expect(Math.abs(state.actionTop - state.nameTop)).toBeLessThan(20)
    expect(state.name).toContain('Sales refresh')
    expect(state.status).toBe('Succeeded')
    expect(state.publicationLabel).toContain('Last published')
    expect(state.publication).not.toBe('')
    expect(await page.getByRole('button', { name: 'Run Sales refresh now' }).isVisible()).toBe(true)
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
