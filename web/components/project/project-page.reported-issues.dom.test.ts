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
      response.end(testDocument(url.searchParams.get('root') ?? 'project'))
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

test('source Fields search stays labeled, keyboard safe, and usable on narrow screens', async () => {
  const page = await browser.newPage({ viewport: { width: 960, height: 720 } })
  try {
    await page.goto(`${baseURL}/?root=source-definition`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const desktop = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot! as ShadowRoot
      const form = root.querySelector<HTMLFormElement>('.semantic-object-search-wrap')!
      const input = root.querySelector<HTMLInputElement>('.semantic-object-search')!
      const header = root.querySelector<HTMLElement>('.semantic-object-list-header')!
      input.focus()
      input.value = 'customer_city'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, composed: true }))
      form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }))
      await element.updateComplete
      return {
        ariaLabel: input.getAttribute('aria-label'),
        focused: root.activeElement === input,
        rows: (root.querySelector('lv-record-table') as any)?.table?.rows?.length,
        url: window.location.search,
        formWidth: Math.round(form.getBoundingClientRect().width),
        headerWidth: Math.round(header.getBoundingClientRect().width),
      }
    })
    expect(desktop).toEqual({ ariaLabel: 'Search fields', focused: true, rows: 1, url: '?root=source-definition', formWidth: expect.any(Number), headerWidth: expect.any(Number) })
    expect(desktop.formWidth).toBeLessThanOrEqual(desktop.headerWidth)

    await page.setViewportSize({ width: 320, height: 720 })
    const narrow = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot! as ShadowRoot
      const form = root.querySelector<HTMLElement>('.semantic-object-search-wrap')!
      const header = root.querySelector<HTMLElement>('.semantic-object-list-header')!
      const heading = root.querySelector<HTMLElement>('.semantic-object-list-header h2')!
      const formBox = form.getBoundingClientRect()
      const headerBox = header.getBoundingClientRect()
      return {
        formWidth: Math.round(formBox.width),
        headerWidth: Math.round(headerBox.width),
        headingBottom: Math.round(heading.getBoundingClientRect().bottom),
        formTop: Math.round(formBox.top),
      }
    })
    expect(narrow.formWidth).toBeLessThanOrEqual(narrow.headerWidth)
    expect(narrow.formTop).toBeGreaterThanOrEqual(narrow.headingBottom)
  } finally {
    await page.close()
  }
})


test('pipeline Lineage renders dependency and explicit loading, empty, and error states', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipeline-lineage`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page') && customElements.get('lv-asset-lineage-graph'))
    const ready = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot! as ShadowRoot
      const graph = root.querySelector('lv-asset-lineage-graph') as any
      const uses = root.querySelector('lv-record-table') as any
      await graph?.updateComplete
      await uses?.updateComplete
      return {
        graphNodes: graph?.graph?.nodes?.length,
        graphEdges: graph?.graph?.edges?.length,
        usesRows: uses?.table?.rows?.length,
        state: root.querySelector('.lineage-state')?.textContent?.trim() ?? '',
      }
    })
    expect(ready).toEqual({ graphNodes: 2, graphEdges: 1, usesRows: 1, state: '' })

    for (const [rootName, expected] of [['pipeline-lineage-loading', 'Loading lineage…'], ['pipeline-lineage-empty', 'No lineage dependencies are available for this asset.'], ['pipeline-lineage-error', 'Lineage could not be loaded. Try again.']]) {
      await page.goto(`${baseURL}/?root=${rootName}`)
      await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
      const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
        await element.updateComplete
        const root = element.shadowRoot! as ShadowRoot
        const table = root.querySelector('lv-record-table') as any
        await table?.updateComplete
        return {
          status: root.querySelector('[role="status"]')?.textContent?.trim() ?? '',
          alert: root.querySelector('[role="alert"]')?.textContent?.trim() ?? '',
          graph: Boolean(root.querySelector('lv-asset-lineage-graph')),
          empty: table?.table?.empty ?? '',
        }
      })
      if (rootName.endsWith('error')) {
        expect(state.alert).toBe(expected)
      } else {
        expect(state.status).toBe(expected)
      }
      expect(state.graph).toBe(false)
      if (rootName.endsWith('empty')) expect(state.empty).toBe('This pipeline does not reference other assets.')
    }
  } finally {
    await page.close()
  }
})
