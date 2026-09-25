import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/project-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      const requestedSection = url.searchParams.get('section')
      const section = requestedSection === 'events' ? 'events' : requestedSection === 'details' ? 'details' : 'execution'
      const requestedStatus = url.searchParams.get('status')
      const status = requestedStatus === 'failed' ? 'failed' : requestedStatus === 'running' ? 'running' : 'prepared'
      response.setHeader('content-type', 'text/html')
      response.end(runDocument(section, status))
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

test('publishing remains a secondary detail while the primary run is Running', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?status=prepared`)
    const state = await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => {
      element.signals.page.execution.publicationOutcome = 'pending'
      element.requestUpdate()
      await element.updateComplete
      const root = element.shadowRoot!
      return {
        primary: root.querySelector('.run-status:first-child .run-status-value')?.textContent?.trim(),
        secondary: root.querySelector('.run-status:nth-child(2) .run-status-value')?.textContent?.trim(),
        evidence: root.querySelector('.run-status:nth-child(2) .run-status-detail')?.textContent?.trim(),
        progress: root.querySelector('.lifecycle-phase:nth-child(4) strong')?.textContent?.trim(),
        animation: getComputedStyle(root.querySelector('.run-status-progress')!).animationName,
      }
    })
    expect(state).toEqual({ primary: 'Running', secondary: 'Publishing', evidence: 'Activating the new data.', progress: 'Publishing', animation: 'run-status-spin' })
    await page.emulateMedia({ reducedMotion: 'reduce' })
    expect(await page.locator('lv-pipeline-run-page .run-status-progress').evaluate((indicator) => getComputedStyle(indicator).animationName)).toBe('none')
  } finally {
    await page.close()
  }
}, 15_000)

test('run investigation uses Primer status colors for each execution outcome', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?status=running`)
    const colors = await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => {
      element.style.setProperty('--lv-fg-warning', '#d29922')
      element.style.setProperty('--lv-fg-success', '#3fb950')
      element.style.setProperty('--lv-fg-danger', '#f85149')
      const result: Record<string, string> = {}
      for (const status of ['queued', 'running', 'prepared', 'succeeded', 'failed']) {
        element.signals.page.status = status
        element.signals.page.statusLabel = status.charAt(0).toUpperCase() + status.slice(1)
        element.requestUpdate()
        await element.updateComplete
        result[status] = getComputedStyle(element.shadowRoot.querySelector('.run-status:first-child .run-status-value')).color
      }
      return result
    })
    expect(colors).toEqual({ queued: 'rgb(210, 153, 34)', running: 'rgb(210, 153, 34)', prepared: 'rgb(210, 153, 34)', succeeded: 'rgb(63, 185, 80)', failed: 'rgb(248, 81, 73)' })
  } finally {
    await page.close()
  }
})

test('run detail duration ticks until the persisted finish arrives', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?status=running`)
    const host = page.locator('lv-pipeline-run-page')
    await host.evaluate(async (element: any) => {
      element.signals.page.execution.startedAt = new Date(Date.now() - 3200).toISOString()
      element.signals.page.execution.duration = undefined
      element.requestUpdate()
      await element.updateComplete
    })
    const detail = page.locator('lv-pipeline-run-page .run-status:first-child .run-status-detail')
    const seconds = async () => Number.parseInt((await detail.innerText()).trim(), 10)
    const initial = await seconds()
    expect(initial).toBeGreaterThanOrEqual(3)
    await page.locator('lv-pipeline-run-page lv-asset-lineage-graph').evaluate((graph: any) => { (window as any).__durationGraph = graph.graph })
    await page.waitForTimeout(1200)
    expect(await seconds()).toBeGreaterThan(initial)
    expect(await page.locator('lv-pipeline-run-page lv-asset-lineage-graph').evaluate((graph: any) => graph.graph === (window as any).__durationGraph)).toBe(true)

    await host.evaluate(async (element: any) => {
      element.signals.page.status = 'succeeded'
      element.signals.page.statusLabel = 'Succeeded'
      element.signals.page.execution.finishedAt = new Date().toISOString()
      element.signals.page.execution.duration = '5s'
      element.requestUpdate()
      await element.updateComplete
    })
    expect((await detail.innerText()).trim().startsWith('5s ·')).toBe(true)
    await page.waitForTimeout(1200)
    expect((await detail.innerText()).trim().startsWith('5s ·')).toBe(true)
  } finally {
    await page.close()
  }
}, 15_000)

test('pipeline run investigation separates execution and publication and exposes diagnostics truthfully', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/`)
    await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => element.updateComplete)
    const snapshot = await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return {
        breadcrumbs: [...root.querySelectorAll('nav[aria-label="Breadcrumb"] a')].map((item) => item.getAttribute('href')),
        breadcrumbLabels: [...root.querySelectorAll('nav[aria-label="Breadcrumb"] .breadcrumb-item')].map((item) => item.textContent?.trim()),
        pipelineIcon: Boolean(root.querySelector('nav[aria-label="Breadcrumb"] a[href="/pipelines/pipeline:sales/details"] .breadcrumb-glyph svg')),
        redundantHeading: Boolean(root.querySelector('.run-heading-line')),
        liveIndicator: Boolean(root.querySelector('.run-live-status')),
        executionStatus: root.querySelector('.run-status-grid .run-status:nth-child(1) .run-status-value')?.textContent?.trim(),
        publicationStatus: root.querySelector('.run-status-grid .run-status:nth-child(2) .run-status-value')?.textContent?.trim(),
        finishStatus: root.querySelector('.facts .fact:nth-child(2) strong')?.textContent?.trim(),
        selectedModel: root.querySelector('.model-list button[aria-pressed="true"] .model-name')?.textContent?.trim(),
        diagnostic: root.querySelector('.diagnostic-error')?.textContent?.trim(),
        modelOutcome: root.querySelector('.model-list .model-outcome')?.textContent?.trim(),
        modelDetailHref: root.querySelector('.model-open')?.getAttribute('href'),
        modelTiming: root.querySelector('.model-list .model-timing')?.textContent?.trim(),
        graphModelEvidence: (root.querySelector('lv-asset-lineage-graph') as any)?.graph?.nodes?.find((node: any) => node.id === 'model:failed')?.meta,
        executionNotes: [...root.querySelectorAll('.model-diagnostic p')].map((item) => item.textContent?.trim()),
        queueEvidence: root.querySelector('.lifecycle-phase:nth-child(1) small')?.textContent?.trim(),
        validationEvidence: root.querySelector('.lifecycle-phase:nth-child(3) strong')?.textContent?.trim(),
        queueWait: root.querySelector('.lifecycle-phase:nth-child(1) strong')?.textContent?.trim(),
        runTimesCarryUTC: root.querySelector('.run-status-grid .run-status:first-child .run-status-detail')?.textContent?.includes('UTC'),
        unavailable: [...root.querySelectorAll('.graph-message[role="status"]')].map((item) => item.textContent?.trim()),
        sectionLinks: [...root.querySelectorAll('.tabs a')].map((item) => item.getAttribute('href')),
      }
    })

    expect(snapshot).toEqual({
      breadcrumbs: ['/pipelines', '/pipelines/pipeline:sales/details', '/pipelines/pipeline%3Asales/runs'],
      breadcrumbLabels: ['Pipelines', 'Sales refresh', 'Runs', '# run:latest'],
      pipelineIcon: true,
      redundantHeading: false,
      liveIndicator: false,
      executionStatus: 'Running',
      publicationStatus: 'Unverified',
      finishStatus: undefined,
      selectedModel: 'Failed model',
      diagnostic: 'Model query failed',
      modelOutcome: 'Failed',
      modelDetailHref: '/models/model:failed/details',
      modelTiming: 'Duration 5s',
      graphModelEvidence: 'model:failed',
      executionNotes: ['Model query failed'],
      queueEvidence: undefined,
      validationEvidence: 'Not recorded',
      queueWait: 'Waited 12s',
      runTimesCarryUTC: true,
      unavailable: [],
      sectionLinks: [
        '/pipelines/pipeline%3Asales/runs/run%3Alatest?section=execution',
        '/pipelines/pipeline%3Asales/runs/run%3Alatest?section=events',
        '/pipelines/pipeline%3Asales/runs/run%3Alatest?section=details',
      ],
    })

    await page.goto(`${baseURL}/?section=execution&status=failed`)
    await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => element.updateComplete)
    await page.goto(`${baseURL}/?section=details&status=failed`)
    await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => element.updateComplete)
    const terminalMissingFinish = await page.locator('lv-pipeline-run-page .facts .fact').filter({ hasText: 'Finished' }).locator('strong').textContent()
    expect(terminalMissingFinish?.trim()).toBe('Not recorded')
    expect(await page.getByRole('region', { name: 'Attempts' }).getByText('Attempt 1 · Failed').count()).toBe(1)

    await page.goto(`${baseURL}/?section=events`)
    await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => element.updateComplete)
    const eventUnavailable = await page.locator('lv-pipeline-run-page .graph-message').textContent()
    expect(eventUnavailable?.trim()).toBe('Run events could not be loaded.')
  } finally {
    await page.close()
  }
}, 15_000)

test('run graph shows recorded model states and only an active model moves', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?status=running`)
    await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => element.updateComplete)
    const graph = page.locator('lv-pipeline-run-page lv-asset-lineage-graph')
    await graph.locator('.asset-lineage-node-selected').waitFor()
    expect(await graph.locator('.react-flow__node').count()).toBe(1)
    expect((await page.locator('lv-pipeline-run-page .graph-panel h3').textContent())?.trim()).toBe('Dependencies')
    await graph.getByRole('button', { name: 'Show full run graph' }).click()
    await graph.locator('.asset-lineage-node-run-animated').waitFor()
    expect(await graph.locator('.asset-lineage-node-run-running').count()).toBe(2)
    expect(await graph.locator('.asset-lineage-node-run-animated').count()).toBe(1)
    expect(await graph.locator('.asset-lineage-node-run-failed').count()).toBe(1)
    expect(await graph.locator('.asset-lineage-node-unrelated').first().evaluate((node) => getComputedStyle(node).opacity)).toBe('1')
    expect((await graph.locator('.asset-lineage-node-run-animated .asset-lineage-node-run-status').textContent())?.trim()).toBe('Running')
    const activeAnimation = await graph.locator('.asset-lineage-node-run-animated .asset-lineage-node-run-status').evaluate((node) => getComputedStyle(node).animationName)
    expect(activeAnimation).not.toBe('none')
    const failedAnimation = await graph.locator('.asset-lineage-node-run-failed .asset-lineage-node-run-status').evaluate((node) => getComputedStyle(node).animationName)
    expect(failedAnimation).toBe('none')
    const graphHelp = page.locator('lv-pipeline-run-page .graph-instruction')
    expect(await graphHelp.count()).toBe(0)
    await page.setViewportSize({ width: 390, height: 844 })
    expect(await graphHelp.count()).toBe(0)
    const lifecycle = page.locator('lv-pipeline-run-page details[aria-label="Lifecycle evidence"]')
    expect(await lifecycle.locator('.lifecycle-summary').isVisible()).toBe(true)
    expect(await lifecycle.locator('.lifecycle-summary').textContent()).toContain('Progress')
    await lifecycle.locator('.lifecycle-grid').waitFor({ state: 'hidden' })
    expect(await lifecycle.locator('.lifecycle-grid').isVisible()).toBe(false)
    expect(await page.getByRole('region', { name: 'Model diagnostics' }).isVisible()).toBe(true)
    await page.getByRole('group', { name: 'Execution view' }).getByRole('button', { name: 'Graph' }).click()
    expect(await graph.isVisible()).toBe(true)
    await graph.getByRole('button', { name: /Model Prepared model, Running/ }).click()
    await page.locator('lv-pipeline-run-page .model-list button[aria-pressed="true"]').filter({ hasText: 'Prepared model' }).waitFor()
    expect(await page.getByRole('group', { name: 'Execution view' }).getByRole('button', { name: 'Models' }).getAttribute('aria-pressed')).toBe('true')
    await page.getByRole('group', { name: 'Execution view' }).getByRole('button', { name: 'Graph' }).click()
    await graph.locator('.asset-lineage-node-selected').filter({ hasText: 'Prepared model' }).waitFor()
    await graph.getByRole('button', { name: 'Show focused path' }).click()
    await page.waitForFunction(() => {
      const root = document.querySelector('lv-pipeline-run-page')?.shadowRoot
      const nodes = Array.from(root?.querySelectorAll<HTMLElement>('lv-asset-lineage-graph .asset-lineage-node-selected') ?? [])
      const node = nodes.find((candidate) => candidate.textContent?.includes('Prepared model'))
      const flow = node?.closest('.react-flow')
      if (!node || !flow) return false
      const nodeBounds = node.getBoundingClientRect()
      const flowBounds = flow.getBoundingClientRect()
      return nodeBounds.top >= flowBounds.top && nodeBounds.bottom <= flowBounds.bottom
        && nodeBounds.left < flowBounds.right && nodeBounds.right > flowBounds.left
    }, undefined, { timeout: 5000 })
    const selectedMobile = await graph.locator('.asset-lineage-node-selected').evaluate((node) => {
      const graph = node.closest('.react-flow')!
      const selectedBounds = node.getBoundingClientRect()
      const graphBounds = graph.getBoundingClientRect()
      const title = node.querySelector('.asset-lineage-node-title')!
      const scale = Number((graph.querySelector('.react-flow__viewport') as HTMLElement).style.transform.match(/scale\(([-\d.]+)\)/)?.[1])
      return {
        visible: selectedBounds.top >= graphBounds.top && selectedBounds.bottom <= graphBounds.bottom
          && selectedBounds.left < graphBounds.right && selectedBounds.right > graphBounds.left,
        renderedTitlePixels: Number.parseFloat(getComputedStyle(title).fontSize) * scale,
      }
    })
    expect(selectedMobile.visible).toBe(true)
    expect(selectedMobile.renderedTitlePixels).toBeGreaterThanOrEqual(12)

    await page.locator('lv-pipeline-run-page').evaluate((element: any) => {
      element.signals.page.execution.models[1].status = 'succeeded'
      element.signals.page.execution.models[1].statusLabel = 'Succeeded'
    })
    await graph.locator('.asset-lineage-node-run-succeeded').waitFor()
    expect(await graph.locator('.asset-lineage-node-run-succeeded').count()).toBe(1)
    expect(await graph.locator('.asset-lineage-node-run-running').count()).toBe(0)
    expect(await graph.locator('.asset-lineage-node-run-animated').count()).toBe(0)
    expect(await page.locator('lv-pipeline-run-page .run-status-grid .run-status:nth-child(2) .run-status-value').textContent()).toBe('Unverified')

    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.locator('lv-pipeline-run-page').evaluate((element: any) => {
      element.signals.page.execution.models[1].status = 'running'
      element.signals.page.execution.models[1].statusLabel = 'Running'
    })
    await graph.locator('.asset-lineage-node-run-animated').waitFor()
    const reducedAnimation = await graph.locator('.asset-lineage-node-run-animated .asset-lineage-node-run-status').evaluate((node) => getComputedStyle(node).animationName)
    expect(reducedAnimation).toBe('none')
  } finally {
    await page.close()
  }
}, 15_000)

test('run graph keeps scope separate from Fit and expansion', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?status=running`)
    await page.locator('lv-pipeline-run-page').evaluate(async (element: any) => element.updateComplete)
    const graph = page.locator('lv-pipeline-run-page lv-asset-lineage-graph')
    const heading = page.locator('lv-pipeline-run-page .graph-panel h3')
    await graph.locator('.asset-lineage-node-selected').waitFor()
    expect((await heading.textContent())?.trim()).toBe('Dependencies')
    expect(await graph.locator('.react-flow__node').count()).toBe(1)
    expect(await page.locator('lv-pipeline-run-page .graph-instruction').count()).toBe(0)

    await graph.getByRole('button', { name: 'Show full run graph' }).click()
    await graph.locator('.react-flow__node').filter({ hasText: 'Prepared model' }).waitFor()
    expect((await heading.textContent())?.trim()).toBe('Dependencies')
    expect(await graph.locator('.react-flow__node').count()).toBe(3)

    await graph.getByRole('button', { name: 'Expand graph' }).click()
    expect((await graph.locator('.asset-lineage-dialog-title').textContent())?.trim()).toBe('Sales refresh · Full run graph · run:latest')
    expect(await graph.locator('dialog').getAttribute('aria-label')).toBe('Sales refresh · Full run graph · run:latest')
    await graph.getByRole('button', { name: 'Close graph' }).click()

    await graph.getByRole('button', { name: 'Fit', exact: true }).click()
    expect(await graph.locator('.react-flow__node').count()).toBe(3)
    expect(await graph.getByRole('button', { name: 'Show focused path' }).count()).toBe(1)

    await graph.getByRole('button', { name: /Model Prepared model, Running/ }).click()
    await page.locator('lv-pipeline-run-page .model-list button[aria-pressed="true"]').filter({ hasText: 'Prepared model' }).waitFor()
    expect((await heading.textContent())?.trim()).toBe('Dependencies')
    expect(await graph.locator('.react-flow__node').count()).toBe(3)
    await graph.getByRole('button', { name: 'Show focused path' }).click()
    expect(await graph.locator('.react-flow__node').count()).toBe(1)
    expect(await graph.locator('.asset-lineage-node-selected').textContent()).toContain('Prepared model')
  } finally {
    await page.close()
  }
}, 15_000)

test('identical run and model failures appear once and point to the failed model', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?status=failed`)
    const host = page.locator('lv-pipeline-run-page')
    await host.evaluate(async (element: any) => element.updateComplete)
    expect(await host.locator('.run-error').textContent()).toContain('Model query failed')
    expect(await host.locator('.model-diagnostic .diagnostic-error').count()).toBe(0)
    expect(await host.locator('.run-status-value').allTextContents()).toEqual(['Failed', 'Unverified'])
    await host.getByRole('button', { name: 'View failed model' }).click()
    expect(await host.locator('.model-list button[aria-pressed="true"] .model-name').textContent()).toBe('Failed model')
  } finally {
    await page.close()
  }
}, 15_000)

test('run update indicator follows only its page stream and recovers on a fresh page patch', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?status=running`)
    const host = page.locator('lv-pipeline-run-page')
    await host.evaluate(async (element: any) => element.updateComplete)
    const graph = host.locator('lv-asset-lineage-graph')
    await graph.getByRole('button', { name: 'Show full run graph' }).click()
    const indicator = host.locator('.run-live-status')
    const animated = host.locator('.asset-lineage-node-run-animated')
    expect(await indicator.count()).toBe(0)
    await animated.waitFor({ state: 'attached' })
    await page.evaluate(() => {
      const main = document.querySelector('main[data-init]')!
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retrying', el: main } }))
    })
    await page.waitForFunction(() => document.querySelector('lv-pipeline-run-page')?.shadowRoot?.querySelector('.run-live-status')?.textContent?.trim() === 'Reconnecting')
    expect(await animated.count()).toBe(0)
    await page.evaluate(() => {
      document.dispatchEvent(new CustomEvent('datastar-signal-patch', { detail: { runtime: { streamInstanceId: 'reconnected-stream' } } }))
    })
    await page.waitForFunction(() => !document.querySelector('lv-pipeline-run-page')?.shadowRoot?.querySelector('.run-live-status'))
    await animated.waitFor({ state: 'attached' })
    await page.evaluate(() => {
      const main = document.querySelector('main[data-init]')!
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retries-failed', el: main } }))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retrying', el: document.createElement('button') } }))
    })
    await page.waitForFunction(() => document.querySelector('lv-pipeline-run-page')?.shadowRoot?.querySelector('.run-live-status')?.textContent?.trim() === 'Connection lost')
  } finally {
    await page.close()
  }
}, 15_000)

function runDocument(activeTab: string, status: string): string {
  const statusLabels: Record<string, string> = { prepared: 'Running', failed: 'Failed', running: 'Running' }
  const page = {
    kind: 'pipeline_run_detail', title: 'Run run:latest', description: 'Run investigation', activeTab,
    environment: 'dev', pipelineId: 'pipeline:sales', pipelineTitle: 'Sales refresh', pipelineHref: '/pipelines/pipeline:sales/details',
    runId: 'run:latest', status, statusLabel: statusLabels[status] || status, runError: status === 'failed' ? 'Model query failed' : undefined, execution: {
      createdAt: '2026-09-21T13:00:00Z', startedAt: '2026-09-21T13:00:12Z', publicationOutcome: 'unverified', validationOutcome: 'unknown', validationTimingAvailable: false,
      attempts: [{ number: 1, status: 'failed', claimedAt: '2026-09-21T13:00:10Z', startedAt: '2026-09-21T13:00:12Z', finishedAt: '2026-09-21T13:00:17Z', duration: '5s', error: 'Root run failed' }],
      graph: { nodes: [
        { id: 'pipeline:sales', kind: 'refresh_pipeline', label: 'Sales refresh', rank: 0, side: 'selected' },
        { id: 'model:failed', kind: 'model', label: 'Failed model', meta: 'model:failed', href: '/models/model:failed/details', rank: 1, side: 'downstream' },
        { id: 'model:prepared', kind: 'model', label: 'Prepared model', rank: 2, side: 'downstream' },
      ], edges: [] },
      models: [
        { modelId: 'failed', status: 'failed', statusLabel: 'Failed', duration: '5s', error: 'Model query failed', attempts: [{ number: 1, status: 'failed', claimedAt: '2026-09-21T13:00:12Z', duration: '5s', error: 'Model query failed' }] },
        { modelId: 'prepared', status: status === 'running' ? 'running' : 'prepared', statusLabel: status === 'running' ? 'Running' : 'Ready to publish', attempts: [] },
      ],
    },
    events: [], eventsUnavailable: true, eventsTruncated: false,
    details: {
      trigger: 'manual', servingStateId: 'generation:old', planDigest: 'digest:plan', semanticModelId: 'semantic-model:sales',
      materializationScope: ['failed', 'prepared'], matchingScheduleIds: [], historicalPipelineVersionAvailable: false,
    },
  }
  const signals = JSON.stringify({ page }).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
  return `<!doctype html><html><body><main data-init="0" data-signals="${signals}"><lv-pipeline-run-page></lv-pipeline-run-page></main><script type="module" src="/asset-lineage-graph.js"></script><script type="module" src="/project-page-under-test.js"></script></body></html>`
}
