import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let browser: Browser
let baseURL = ''

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/agent-settings-test')

beforeAll(async () => {
  await Bun.$`rm -rf ${root}`.quiet()
  const built = await Bun.build({
    entrypoints: ['web/components/admin/agent-settings.ts'],
    target: 'browser',
    format: 'esm',
    outdir: root,
  })
  if (!built.success) throw new Error('failed to build agent settings test bundle')
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const file = normalize(join(root, url.pathname))
    if (!file.startsWith(root)) {
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

test('agent settings keeps instructions and tools in a focused tabbed surface', async () => {
  const page = await browser.newPage({ viewport: { width: 1120, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-agent-settings'))
    const state = await page.evaluate(async () => {
      const element = document.querySelector('lv-agent-settings') as any
      element.agent = {
        enabled: true,
        model: 'fake-model',
        systemPrompt: 'Signal prompt',
        canWrite: true,
        updatePath: '/admin/agent/config',
        tools: [{
          name: 'query_visual',
          description: 'Query visual data.',
          effect: 'read',
          tags: ['analytics', 'visualization'],
          defaults: { mode: 'summary' },
          inputSchema: { type: 'object', required: ['dashboardId'], properties: { dashboardId: { type: 'string' } } },
          outputSchema: { type: 'object', properties: { rows: { type: 'array' } } },
        }, {
          name: 'publish_report',
          description: 'Publish a report.',
          effect: 'write',
          tags: ['dashboard', 'authoring'],
          defaults: {},
          inputSchema: { type: 'object', properties: { reportId: { type: 'string' } } },
          outputSchema: {},
        }],
      }
      element.prompt = 'Signal prompt'
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const overviewText = root.querySelector('.overview')?.textContent?.replace(/\s+/g, ' ').trim()
      const initialTabs = Array.from(root.querySelectorAll<HTMLButtonElement>('[role="tab"]')).map((button) => ({ text: button.textContent?.trim(), selected: button.getAttribute('aria-selected') }))
      const promptEditor = root.querySelector('lv-agent-prompt-editor') as any
      await promptEditor.updateComplete
      const promptRoot = promptEditor.shadowRoot as ShadowRoot
      const headerText = promptRoot.querySelector('.prompt-header')?.textContent?.replace(/\s+/g, ' ').trim()
      const modeLabels = Array.from(promptRoot.querySelectorAll<HTMLButtonElement>('.mode-toggle button')).map((button) => button.textContent?.replace(/\s+/g, ' ').trim())

      let command: unknown = null
      element.addEventListener('lv-agent-system-prompt-save', (event: CustomEvent) => { command = event.detail })
      promptRoot.querySelector<HTMLButtonElement>('.mode-toggle button[aria-label="Edit"]')?.click()
      await promptEditor.updateComplete
      const codeEditor = promptRoot.querySelector('lv-code-editor') as any
      await codeEditor.updateComplete
      codeEditor.dispatchEvent(new CustomEvent('lv-code-editor-change', {
        bubbles: true,
        composed: true,
        detail: { value: 'Changed prompt' },
      }))
      await promptEditor.updateComplete
      const dirtyState = {
        status: promptRoot.querySelector('.prompt-status')?.textContent?.trim(),
        hasDiscard: Boolean(promptRoot.querySelector('.discard-button')),
        hasSave: Boolean(promptRoot.querySelector('.save-button')),
      }
      promptRoot.querySelector<HTMLButtonElement>('.discard-button')?.click()
      await promptEditor.updateComplete
      const discardedState = {
        status: promptRoot.querySelector('.prompt-status')?.textContent?.trim() ?? '',
        hasDiscard: Boolean(promptRoot.querySelector('.discard-button')),
        editorValue: (promptRoot.querySelector('lv-code-editor') as any).value,
      }
      const codeEditorAfterDiscard = promptRoot.querySelector('lv-code-editor') as any
      codeEditorAfterDiscard.dispatchEvent(new CustomEvent('lv-code-editor-change', {
        bubbles: true,
        composed: true,
        detail: { value: 'Saved prompt' },
      }))
      await promptEditor.updateComplete
      promptRoot.querySelector<HTMLButtonElement>('.save-button')?.click()
      await promptEditor.updateComplete

      const toolsTab = Array.from(root.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find((button) => button.textContent?.trim() === 'Tools')!
      toolsTab.click()
      await element.updateComplete
      const tools = root.querySelector('lv-agent-tools') as any
      await tools.updateComplete
      const toolsRoot = tools.shadowRoot as ShadowRoot
      const list = toolsRoot.querySelector('lv-entity-list') as HTMLElement & { updateComplete: Promise<unknown> }
      await list.updateComplete
      const rows = Array.from(list.querySelectorAll<HTMLElement>('.entity-list-table-row'))
      const groups = Array.from(list.querySelectorAll('.entity-list-group-label')).map((label) => label.textContent?.trim())
      const firstHeader = list.querySelector<HTMLElement>('.entity-list-table thead th:first-child')
      const firstCell = list.querySelector<HTMLElement>('.entity-list-table-row th[scope="row"]')
      const firstColumnPositions = [firstHeader, firstCell].map((cell) => cell ? getComputedStyle(cell).position : '')
      rows[0]?.click()
      await tools.updateComplete
      const drawer = toolsRoot.querySelector('lv-drawer') as any
      const schemaTabs = Array.from(toolsRoot.querySelectorAll<HTMLButtonElement>('.tabs [role="tab"]')).map((button) => button.textContent?.trim())
      const search = list.querySelector<HTMLInputElement>('input[type="search"]')!
      search.value = 'publish'
      search.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await list.updateComplete
      const filteredTools = Array.from(list.querySelectorAll('.entity-list-table-row')).map((row) => row.querySelector('.entity-list-title')?.textContent?.trim())

      return {
        overviewText,
        initialTabs,
        headerText,
        modeLabels,
        dirtyState,
        discardedState,
        command,
        hasSharedList: Boolean(list),
        firstColumnPositions,
        groups,
        impacts: rows.map((row) => row.querySelectorAll('td')[1]?.textContent?.trim()),
        hasRowIcons: Boolean(list.querySelector('.entity-list-icon')),
        drawerOpen: Boolean(drawer),
        drawerModal: drawer?.modal,
        schemaTabs,
        filteredTools,
      }
    })

    expect(state.overviewText).toContain('Enabled')
    expect(state.overviewText).toContain('fake-model')
    expect(state.overviewText).toContain('Tools 2')
    expect(state.overviewText).toContain('Editable')
    expect(state.initialTabs).toEqual([{ text: 'Instructions', selected: 'true' }, { text: 'Tools', selected: 'false' }])
    expect(state.headerText).toContain('Guide the agent')
    expect(state.modeLabels).toEqual(['Preview', 'Edit'])
    expect(state.dirtyState).toEqual({ status: 'Unsaved changes', hasDiscard: true, hasSave: true })
    expect(state.discardedState).toEqual({ status: '', hasDiscard: false, editorValue: 'Signal prompt' })
    expect(state.command).toEqual({ systemPrompt: 'Saved prompt' })
    expect(state.hasSharedList).toBe(true)
    expect(state.firstColumnPositions).toEqual(['static', 'static'])
    expect(state.groups).toEqual(['Data & queries', 'Dashboards'])
    expect(state.impacts).toEqual(['Read-only', 'Changes draft'])
    expect(state.hasRowIcons).toBe(false)
    expect(state.drawerOpen).toBe(true)
    expect(state.drawerModal).toBe(false)
    expect(state.schemaTabs).toEqual(['Fields', 'Input JSON', 'Output'])
    expect(state.filteredTools).toEqual(['publish_report'])
  } finally {
    await page.close()
  }
})

test('agent settings makes deployment ownership and mobile tools layout explicit', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-agent-settings'))
    const state = await page.evaluate(async () => {
      const element = document.querySelector('lv-agent-settings') as any
      element.agent = { enabled: false, model: '', systemPrompt: '', canWrite: false, updatePath: '', tools: [{ name: 'read_data', description: '', effect: 'read', tags: ['analytics'], defaults: {}, inputSchema: {}, outputSchema: {} }] }
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const promptEditor = root.querySelector('lv-agent-prompt-editor') as any
      await promptEditor.updateComplete
      const managedBadge = (promptEditor.shadowRoot as ShadowRoot).querySelector('.managed-badge')?.textContent?.trim()
      const toolsTab = Array.from(root.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find((button) => button.textContent?.trim() === 'Tools')!
      toolsTab.click()
      await element.updateComplete
      const tools = root.querySelector('lv-agent-tools') as any
      await tools.updateComplete
      const toolsRoot = tools.shadowRoot as ShadowRoot
      const list = toolsRoot.querySelector('lv-entity-list') as HTMLElement & { updateComplete: Promise<unknown> }
      await list.updateComplete
      list.querySelector<HTMLElement>('.entity-list-table-row')?.click()
      await tools.updateComplete
      const drawer = toolsRoot.querySelector('lv-drawer') as any
      await drawer.updateComplete
      return {
        text: root.textContent?.replace(/\s+/g, ' ').trim(),
        managedBadge,
        hasSharedList: Boolean(list),
        drawerModal: drawer.modal,
        drawerWidth: Math.round((drawer.shadowRoot as ShadowRoot).querySelector('.drawer')!.getBoundingClientRect().width),
      }
    })
    expect(state.text).toContain('Read-only')
    expect(state.text).toContain('Deployment managed')
    expect(state.managedBadge).toBe('Deployment managed')
    expect(state.hasSharedList).toBe(true)
    expect(state.drawerModal).toBe(false)
    expect(state.drawerWidth).toBe(390)
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  return `<!doctype html><html><head><style>body { ${typographyTestTokens} }</style></head><body><lv-agent-settings></lv-agent-settings><script type="module" src="/agent-settings.js"></script></body></html>`
}
