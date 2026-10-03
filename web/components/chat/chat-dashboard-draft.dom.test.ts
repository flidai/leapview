import { afterAll, beforeAll, expect, setDefaultTimeout, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser, type Page } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

setDefaultTimeout(15_000)

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/chat-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
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
  server.closeAllConnections()
  await new Promise<void>((resolve, reject) => server.close((error: NodeJS.ErrnoException | undefined) => {
    if (error && error.code !== 'ERR_SERVER_NOT_RUNNING') reject(error)
    else resolve()
  }))
}, 15_000)

test('chat dashboard draft shows composed visuals, supports edits, and saves retries safely', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  const requests: Array<{ body: any; csrf?: string; key?: string; operation?: string }> = []
  try {
    await page.route('**/chats/c1/dashboard', async (route) => {
      const headers = route.request().headers()
      requests.push({ body: route.request().postDataJSON(), csrf: headers['x-csrf-token'], key: headers['idempotency-key'], operation: headers['x-leapview-operation-id'] })
      await route.fulfill(requests.length === 1
        ? { status: 503, json: { error: 'unavailable' } }
        : { status: 201, json: { dashboardId: 'dashboard:chat-draft', title: 'CFO review', pageId: 'overview', href: '/dashboards/dashboard:chat-draft/edit' } })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-page'))
    await publishDashboardDraft(page, 'revision-1', 'Revenue overview', [
      { id: 'visual_revenue', artifactId: 'artifact_revenue', title: 'Revenue by country' },
      { id: 'visual_margin', artifactId: 'artifact_margin', title: 'Gross margin trend' },
    ])

    const draft = page.locator('lv-chat-dashboard-draft')
    await draft.getByRole('heading', { name: 'Revenue overview' }).waitFor()
    expect(await draft.getByRole('heading', { name: 'Revenue overview' }).count()).toBe(1)
    expect(await draft.locator('.card').count()).toBe(2)
    expect(await draft.locator('lv-visualization-host').evaluateAll((hosts: any[]) => hosts.map((host) => host.envelope?.visualID))).toEqual(['artifact_revenue', 'artifact_margin'])
    expect(await page.locator('lv-chat-page').evaluate((element: any) => element.shadowRoot.querySelector('.route')?.classList.contains('dashboard-open'))).toBe(true)

    await draft.getByRole('button', { name: 'Back to chat', exact: true }).click()
    await draft.waitFor({ state: 'detached' })
    await page.locator('lv-chat-page').evaluate((host: any) => host.shadowRoot.querySelector('lv-chat-thread').dispatchEvent(new CustomEvent('lv-chat-visual-open', { detail: { artifactId: 'artifact_revenue' }, bubbles: true, composed: true })))
    await draft.getByRole('heading', { name: 'Revenue overview', exact: true }).waitFor()
    await draft.getByRole('button', { name: 'Ask about Revenue by country' }).click()
    const composer = page.locator('lv-chat-composer')
    expect(await composer.getByRole('combobox').inputValue()).toBe('Update the dashboard visual "Revenue by country" (draft visual id: visual_revenue): ')
    expect(await draft.locator('.card[data-draft-visual-id="visual_revenue"]').getAttribute('data-selected')).toBe('true')

    await draft.getByRole('button', { name: 'Ask to remove Gross margin trend' }).click()
    expect(await composer.getByRole('combobox').inputValue()).toBe('Remove the dashboard visual "Gross margin trend" (draft visual id: visual_margin).')
    expect(await draft.locator('.card').count()).toBe(2)

    await publishDashboardDraft(page, 'revision-2', 'Revenue overview', [
      { id: 'visual_revenue', artifactId: 'artifact_revenue', title: 'Revenue by country' },
    ])
    expect(await draft.locator('.card').count()).toBe(1)
    expect(await draft.locator('.card[data-draft-visual-id="visual_revenue"]').count()).toBe(1)

    await draft.getByRole('button', { name: 'Save dashboard', exact: true }).click()
    const dialog = draft.locator('dialog')
    await dialog.getByRole('textbox', { name: 'Dashboard name' }).waitFor()
    expect(await dialog.getAttribute('aria-describedby')).toBe('save-dashboard-description')
    expect(await draft.evaluate((element: any) => element.shadowRoot.activeElement?.getAttribute('name'))).toBe('title')
    await page.keyboard.press('Escape')
    await dialog.waitFor({ state: 'detached' })
    expect(await draft.evaluate((element: any) => element.shadowRoot.activeElement?.textContent?.replace(/\s+/g, ' ').trim())).toContain('Save dashboard')
    await draft.getByRole('button', { name: 'Save dashboard', exact: true }).click()
    await dialog.getByRole('textbox', { name: 'Dashboard name' }).fill('CFO review')
    await dialog.getByRole('button', { name: 'Save dashboard', exact: true }).click()
    await dialog.getByRole('alert').waitFor()
    await dialog.getByRole('button', { name: 'Save dashboard', exact: true }).click()
    await dialog.getByRole('heading', { name: 'Dashboard saved' }).waitFor()
    expect(requests).toHaveLength(2)
    expect(requests[0].body).toEqual({ revision: 'revision-2', title: 'CFO review' })
    expect(requests[1].body).toEqual(requests[0].body)
    expect(requests[1].key).toBe(requests[0].key)
    expect(requests[0].key).toMatch(/^[0-9a-f-]{14}7[0-9a-f-]{21}$/)
    expect(requests[0].csrf).toBe('test-csrf')
    expect(requests[0].operation).toBe('saveChatDashboardDraft')
    expect(await dialog.getByRole('link', { name: 'Open dashboard' }).getAttribute('href')).toBe('/dashboards/dashboard:chat-draft/edit')
    await dialog.getByRole('button', { name: 'Done' }).click()
    await dialog.waitFor({ state: 'detached' })
    expect(await draft.evaluate((element: any) => element.shadowRoot.activeElement?.textContent?.replace(/\s+/g, ' ').trim())).toBe('Open saved dashboard')

    await publishDashboardDraft(page, 'revision-3', 'Revenue overview', [
      { id: 'visual_revenue', artifactId: 'artifact_revenue', title: 'Revenue by country' },
      { id: 'visual_margin', artifactId: 'artifact_margin', title: 'Gross margin trend' },
    ])
    expect(await draft.getByRole('button', { name: 'Save as new dashboard' }).isEnabled()).toBe(true)
    expect(await draft.getByRole('status').textContent()).toContain('Save these changes as a new dashboard')
    await draft.getByRole('button', { name: 'Save as new dashboard' }).click()
    const copyDialog = draft.locator('dialog')
    await copyDialog.getByRole('heading', { name: 'Save a new dashboard copy' }).waitFor()
    expect(await copyDialog.getByRole('textbox', { name: 'Dashboard name' }).inputValue()).toBe('Revenue overview')
    expect(await draft.evaluate((element: any) => element.shadowRoot.activeElement?.getAttribute('name'))).toBe('title')
    await copyDialog.getByRole('button', { name: 'Save dashboard', exact: true }).click()
    await copyDialog.getByRole('heading', { name: 'Dashboard saved' }).waitFor()
    expect(requests).toHaveLength(3)
    expect(requests[2].body).toEqual({ revision: 'revision-3', title: 'Revenue overview' })
    expect(requests[2].key).not.toBe(requests[0].key)
  } finally {
    await page.close()
  }
}, 30_000)

for (const width of [1440, 360]) {
test(`visual Preview edits and closes safely at width ${width}`, async () => {
  const page = await browser.newPage({ viewport: { width, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-page'))
    await publishDashboardDraft(page, 'fixture', 'Sales', [{ id: 'sales', artifactId: 'artifact_sales', title: 'Sales by region' }])
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { dashboardDraft: null, transcript: [{ id: 'tool-sales', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { id: 'artifact_sales', type: 'bar' } }] } })
    })
    await page.waitForFunction(() => !document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-dashboard-draft'))
    await page.locator('lv-chat-page').evaluate((element: any) => element.shadowRoot.querySelector('lv-chat-thread').dispatchEvent(new CustomEvent('lv-chat-visual-open', { detail: { artifactId: 'artifact_sales', title: 'Sales by region' }, bubbles: true, composed: true })))
    const details = page.locator('lv-chat-visual-panel')
    await details.getByRole('button', { name: 'Preview', exact: true }).click()
    const preview = page.locator('lv-chat-dashboard-draft')
    await preview.getByRole('heading', { name: 'Dashboard preview', exact: true }).waitFor()
    expect(await page.getByRole('button', { name: 'Add to dashboard', exact: true }).count()).toBe(0)
    const layout = await page.locator('lv-chat-page').evaluate((host: any) => {
      const canvas = host.shadowRoot.querySelector('lv-chat-dashboard-draft').getBoundingClientRect()
      const agent = host.shadowRoot.querySelector('.main').getBoundingClientRect()
      return { canvasX: canvas.x, canvasWidth: canvas.width, agentX: agent.x, agentWidth: agent.width, overflow: document.documentElement.scrollWidth > innerWidth }
    })
    if (width > 768) {
      expect(layout.canvasX).toBeLessThan(layout.agentX)
      expect(layout.canvasWidth).toBeGreaterThan(layout.agentWidth)
    }
    expect(layout.overflow).toBe(false)
    await preview.getByRole('button', { name: 'Ask about Sales by region' }).click()
    expect(await page.locator('lv-chat-composer').getByRole('combobox').inputValue()).toContain('artifact_sales')
    if (width <= 768) expect(await page.locator('lv-chat-page').evaluate((host: any) => host.shadowRoot.querySelector('.main').inert)).toBe(false)
    const focus = await page.locator('lv-chat-page').evaluate((host: any) => {
      const event = new CustomEvent('lv-chat-submit', { detail: { input: 'Make it a donut', references: [] } as any, bubbles: true, composed: true })
      host.shadowRoot.querySelector('lv-chat-composer').dispatchEvent(event)
      return event.detail.previewArtifactId
    })
    expect(focus).toBe('artifact_sales')
    if (width <= 768) await page.locator('.mobile-dashboard-toggle').click()
    await preview.getByRole('button', { name: 'Back to chat', exact: true }).click()
    await details.getByRole('button', { name: 'Preview', exact: true }).waitFor()
    expect(await preview.count()).toBe(0)
  } finally { await page.close() }
})
}

test('explicit visual preview on reload takes precedence over an existing composed draft', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?preview=artifact_selected`)
    await page.waitForFunction(() => customElements.get('lv-chat-page'))
    await publishDashboardDraft(page, 'composition', 'Whole dashboard', [{ id: 'selected', artifactId: 'artifact_selected', title: 'Selected visual' }])
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { transcript: [{ id: 'query', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { id: 'artifact_selected', type: 'bar' } }] } })
    })
    const preview = page.locator('lv-chat-dashboard-draft')
    await preview.getByRole('heading', { name: 'Dashboard preview', exact: true }).waitFor()
    expect(await preview.evaluate((host: any) => host.sourceArtifactId)).toBe('artifact_selected')
    expect(await page.locator('lv-chat-page').evaluate((host: any) => host.agent.dashboardDraft.title)).toBe('Whole dashboard')
  } finally { await page.close() }
})

test('chat dashboard draft works as a mobile modal and returns visual requests to the composer', async () => {
  const page = await browser.newPage({ viewport: { width: 360, height: 820 } })
  try {
    await page.route('**/chats/c1/dashboard', async (route) => {
      await route.fulfill({ status: 201, json: { dashboardId: 'dashboard:mobile', title: 'Mobile dashboard', pageId: 'overview', href: '/dashboards/dashboard:mobile/edit' } })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-page'))
    await publishDashboardDraft(page, 'mobile-revision', 'Mobile dashboard', [
      { id: 'visual_mobile', artifactId: 'artifact_mobile', title: 'Orders by region' },
    ])
    const draft = page.locator('lv-chat-dashboard-draft')
    await draft.getByRole('heading', { name: 'Mobile dashboard' }).waitFor()
    await page.waitForFunction(() => Boolean(document.querySelector('lv-chat-page')?.shadowRoot?.querySelector<HTMLElement>('.main')?.inert))
    const modal = await draft.evaluate((element: any) => {
      const rect = element.getBoundingClientRect()
      return {
        x: rect.x,
        width: rect.width,
        modal: element.hasAttribute('modal'),
        role: element.shadowRoot.querySelector('aside')?.getAttribute('role'),
        ariaModal: element.shadowRoot.querySelector('aside')?.getAttribute('aria-modal'),
        focusedClose: element.shadowRoot.activeElement?.getAttribute('aria-label'),
        documentWidth: document.documentElement.scrollWidth,
      }
    })
    expect(modal).toEqual({ x: 0, width: 360, modal: true, role: 'dialog', ariaModal: 'true', focusedClose: 'Back to chat', documentWidth: 360 })
    await page.keyboard.press('Escape')
    await page.waitForFunction(() => !document.querySelector('lv-chat-page')?.shadowRoot?.querySelector<HTMLElement>('.main')?.inert)
    expect(await page.locator('lv-chat-page').locator('lv-chat-dashboard-draft').count()).toBe(0)
    expect(await page.locator('lv-chat-composer').getByRole('combobox').evaluate((element: HTMLElement) => element === (element.getRootNode() as ShadowRoot).activeElement)).toBe(true)
    await page.locator('lv-chat-page').locator('.mobile-dashboard-toggle').click()
    await draft.getByRole('button', { name: 'Ask about Orders by region' }).click()
    await page.waitForFunction(() => !document.querySelector('lv-chat-page')?.shadowRoot?.querySelector<HTMLElement>('.main')?.inert)
    expect(await page.locator('lv-chat-composer').getByRole('combobox').inputValue()).toBe('Update the dashboard visual "Orders by region" (draft visual id: visual_mobile): ')
    expect(await page.locator('lv-chat-page').locator('lv-chat-dashboard-draft').count()).toBe(0)
    await publishDashboardDraft(page, 'mobile-revision-2', 'Mobile dashboard', [
      { id: 'visual_mobile', artifactId: 'artifact_mobile', title: 'Orders by region' },
    ])
    await draft.getByRole('button', { name: 'Save dashboard', exact: true }).click()
    const saveDialog = draft.locator('dialog')
    await saveDialog.getByRole('button', { name: 'Save dashboard', exact: true }).click()
    await saveDialog.getByRole('heading', { name: 'Dashboard saved' }).waitFor()
    const afterSave = await draft.evaluate((element: any) => ({
      documentWidth: document.documentElement.scrollWidth,
      hostWidth: element.clientWidth,
      headerWidth: element.shadowRoot.querySelector('.header').clientWidth,
      headerScrollWidth: element.shadowRoot.querySelector('.header').scrollWidth,
    }))
    expect(afterSave.documentWidth).toBe(360)
    expect(afterSave.headerScrollWidth).toBeLessThanOrEqual(afterSave.headerWidth)
    await saveDialog.getByRole('button', { name: 'Done' }).click()
    await saveDialog.waitFor({ state: 'detached' })
    await publishDashboardDraft(page, 'mobile-revision-3', 'Mobile dashboard', [
      { id: 'visual_mobile', artifactId: 'artifact_mobile', title: 'Orders by region' },
      { id: 'visual_extra', artifactId: 'artifact_extra', title: 'Orders by month' },
    ])
    const afterEdit = await draft.evaluate((element: any) => ({
      documentWidth: document.documentElement.scrollWidth,
      hostWidth: element.clientWidth,
      headerWidth: element.shadowRoot.querySelector('.header').clientWidth,
      headerScrollWidth: element.shadowRoot.querySelector('.header').scrollWidth,
      savedLinkWraps: getComputedStyle(element.shadowRoot.querySelector('.saved-link')).whiteSpace,
    }))
    expect(afterEdit.documentWidth).toBe(360)
    expect(afterEdit.headerScrollWidth).toBeLessThanOrEqual(afterEdit.headerWidth)
    expect(afterEdit.savedLinkWraps).toBe('normal')
    expect(await draft.getByRole('button', { name: 'Save as new dashboard' }).isEnabled()).toBe(true)
  } finally {
    await page.close()
  }
}, 30_000)

test('chat visual panel preserves hidden and none proportional legends', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const legends = await page.evaluate(async () => {
      await customElements.whenDefined('lv-chat-visual-panel')
      const displayLegend = async (legend: string) => {
        const panel = document.createElement('lv-chat-visual-panel') as any
        panel.payload = { spec: { kind: 'proportional', mark: 'donut', presentation: { legend, labelPosition: 'outside' } } }
        document.body.append(panel)
        await panel.updateComplete
        return panel.shadowRoot.querySelector('lv-visual-artifact')?.payload?.spec.presentation.legend
      }
      return [await displayLegend('hidden'), await displayLegend('none')]
    })
    expect(legends).toEqual(['hidden', 'none'])
  } finally {
    await page.close()
  }
})

test('chat proportional panels move top and default visible legends below the chart and preserve labels', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const states = await page.evaluate(async () => {
      await customElements.whenDefined('lv-chat-visual-panel')
      const display = async (legend?: string) => {
        const panel = document.createElement('lv-chat-visual-panel') as any
        panel.payload = {
          spec: {
            kind: 'proportional', mark: 'donut',
            presentation: {
              ...(legend === undefined ? {} : { legend }),
              labelPosition: 'outside', legendTitle: 'Country',
              legendItems: [{ value: 'fr', label: 'France' }],
            },
          },
        }
        document.body.append(panel)
        await panel.updateComplete
        const displayed = panel.shadowRoot.querySelector('lv-visual-artifact')?.payload
        return {
          savedLegend: panel.payload.spec.presentation.legend,
          displayedLegend: displayed?.spec.presentation.legend,
          labelPosition: displayed?.spec.presentation.labelPosition,
          legendTitle: displayed?.spec.presentation.legendTitle,
          legendItems: displayed?.spec.presentation.legendItems,
        }
      }
      return [await display('top'), await display()]
    })
    expect(states).toEqual([
      { savedLegend: 'top', displayedLegend: 'bottom', labelPosition: 'outside', legendTitle: 'Country', legendItems: [{ value: 'fr', label: 'France' }] },
      { savedLegend: undefined, displayedLegend: 'bottom', labelPosition: 'outside', legendTitle: 'Country', legendItems: [{ value: 'fr', label: 'France' }] },
    ])
  } finally {
    await page.close()
  }
})

test('chat dashboard draft moves side legends below the chart without hiding the chart title', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const state = await page.evaluate(async () => {
      await customElements.whenDefined('lv-chat-dashboard-draft')
      const draft = document.createElement('lv-chat-dashboard-draft') as any
      const display = draft.normalizedVisualPayload({
        spec: {
          kind: 'proportional',
          mark: 'donut',
          titleVisible: true,
          presentation: { legend: 'right', labelPosition: 'outside' },
        },
      })
      return {
        legend: display.spec.presentation.legend,
        titleVisible: display.spec.titleVisible,
      }
    })
    expect(state).toEqual({ legend: 'bottom', titleVisible: true })
  } finally {
    await page.close()
  }
})

async function publishDashboardDraft(page: Page, revision: string, title: string, visuals: Array<{ id: string; artifactId: string; title: string }>): Promise<void> {
  await page.evaluate(async ({ revision, title, visuals }) => {
    const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
    const envelopeFor = (visualID: string, visualTitle: string) => {
      const specRevision = `sha256:${'2'.repeat(64)}`
      const field = (id: string, role: string) => ({ id, role, dataType: role === 'metric' ? 'decimal' : 'string', nullable: false, label: id })
      return {
        schemaVersion: 14,
        visualID,
        rendererID: 'echarts',
        specRevision,
        dataRevision: 1,
        spec: {
          kind: 'cartesian', mark: 'bar', title: visualTitle,
          datasets: [{ id: 'primary', fields: [field('label', 'dimension'), field('value', 'metric')] }],
          dataBudget: { maxRows: 50, requiredCompleteness: 'complete' },
          accessibility: { title: visualTitle, description: visualTitle },
          interactions: [], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }],
          presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false },
        },
        dataState: { kind: 'inline', specRevision, dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision, dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['France', 42]], completeness: 'complete' }] },
        selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
      }
    }
    const envelopeByID = Object.fromEntries(visuals.map((visual) => [visual.artifactId, envelopeFor(visual.artifactId, visual.title)]))
    window.setTimeout(() => mergePatch({
      agent: { dashboardDraft: { revision, title, visuals } },
      visuals: envelopeByID,
    }), 0)
  }, { revision, title, visuals })
  await page.waitForFunction((expectedRevision) => {
    const draft = document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-dashboard-draft') as any
    return draft?.draft?.revision === expectedRevision
  }, revision)
  await page.locator('lv-chat-page').evaluate((element: any) => element.updateComplete)
  if (visuals.length > 0) {
    await page.waitForFunction(() => Boolean(document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-dashboard-draft')?.shadowRoot?.querySelector('.card')))
  }
}

function testDocument(): string {
  const page = { kind: 'chat', view: 'conversation', title: 'Chats', description: 'Ask about governed BI or make authorized dashboard changes.' }
  const agent = {
    conversations: [
      { id: 'c1', title: 'Revenue check', href: '/chats/c1', updatedAt: '2026-01-02T10:00:00Z' },
      { id: 'c2', title: 'Inventory status', href: '/chats/c2', updatedAt: '2026-01-03T10:00:00Z' },
    ],
    activeConversationId: 'c1',
    transcript: [{ role: 'assistant', content: 'Ready.' }],
    status: { enabled: true, running: false },
    composer: { value: '', disabled: false, placeholder: 'Ask about dashboards, metrics, or models...' },
  }
  return `
    <!doctype html>
    <html>
      <head>
        <meta name="csrf-token" content="test-csrf">
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-panel: #fff; --lv-bg-control: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-bg-hover: #eff2f5; --lv-bg-accent-muted: #ddf4ff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-accent: #0969da; --lv-accent-fg: #fff; --lv-line-default: #d0d7de; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-line-accent-muted: #54aeff; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-border-transparent: 1px solid transparent; --lv-border-width-focus: 2px; --lv-radius-default: 6px; --lv-radius-tight: 4px; --lv-radius-large: 12px; --base-size-4: 4px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-36: 36px; --lv-space-2xs: 2px; --lv-space-xs: 4px; --lv-space-sm: 8px; --lv-space-md: 12px; --lv-space-lg: 16px; --lv-space-control: 10px; --control-medium-size: 32px; --control-large-size: 40px; --control-medium-paddingInline-spacious: 16px; --lv-control-medium: 32px; --button-primary-bgColor-rest: #0969da; --button-primary-bgColor-hover: #0757b3; --button-primary-fgColor-rest: #fff; --lv-chat-stack-width: 760px; --lv-chat-thread-padding: 16px; --lv-chat-thread-padding-compact: 12px; --lv-transition-fast: 160ms ease; --lv-transition-medium: 260ms ease; --shadow-resting-small: 0 1px 2px rgb(0 0 0 / .08); --lv-shadow-floating-sm: 0 8px 24px rgb(0 0 0 / .12); --duration-fast: 160ms; --ease-lv: ease; }
          lv-chat-page { min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="${escapeHTML(JSON.stringify({ page, agent, visuals: {}, tables: {} }))}">
          <lv-chat-page></lv-chat-page>
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/chat-page-under-test.js"></script>
      </body>
    </html>
  `
}

function escapeHTML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}
