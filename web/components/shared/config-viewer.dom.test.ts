import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/config-viewer-test')

beforeAll(async () => {
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

test('configuration viewer toggles between outline and authored YAML/JSON', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-config-viewer'))

    const state = await page.locator('lv-config-viewer').evaluate(async (element: any) => {
      element.configuration = 'kind: Model\nspec:\n  displayName: Customers\n'
      element.language = 'yaml'
      await element.updateComplete
      const root = element.shadowRoot!
      const waitFor = async (predicate: () => boolean): Promise<void> => {
        const started = performance.now()
        while (!predicate()) {
          if (performance.now() - started > 5000) throw new Error('timed out waiting for condition')
          await new Promise((resolve) => setTimeout(resolve, 20))
        }
      }
      await waitFor(() => Boolean(root.querySelector('.tree')))

      const initial = {
        outline: Boolean(root.querySelector('.tree')),
        raw: Boolean(root.querySelector('lv-code-block[toolbar]')),
        rawLabel: root.querySelector<HTMLButtonElement>('button[data-mode="raw"]')?.textContent?.trim(),
        expandLabel: root.querySelector<HTMLButtonElement>('.icon-tool[aria-label="Expand all"]')?.title,
        collapseLabel: root.querySelector<HTMLButtonElement>('.icon-tool[aria-label="Collapse all"]')?.title,
      }
      root.querySelector<HTMLButtonElement>('button[data-mode="raw"]')!.click()
      await element.updateComplete
      await waitFor(() => Boolean(root.querySelector('lv-code-block[toolbar] .shiki')))
      const rawBlock = root.querySelector('lv-code-block[toolbar]') as any
      const yaml = {
        outline: Boolean(root.querySelector('.tree')),
        code: rawBlock.code,
        language: rawBlock.language,
        label: root.querySelector('.viewer')?.getAttribute('aria-label'),
      }

      element.configuration = '{"kind":"Model","count":2}'
      element.language = 'json'
      await element.updateComplete
      await waitFor(() => (root.querySelector('lv-code-block[toolbar]') as any)?.language === 'json')
      const jsonBlock = root.querySelector('lv-code-block[toolbar]') as any
      const json = {
        code: jsonBlock.code,
        language: jsonBlock.language,
        rawLabel: root.querySelector<HTMLButtonElement>('button[data-mode="raw"]')?.textContent?.trim(),
      }

      root.querySelector<HTMLButtonElement>('button[data-mode="outline"]')!.click()
      await element.updateComplete
      return { initial, yaml, json, outlineAfterToggle: Boolean(root.querySelector('.tree')) }
    })

    expect(state.initial).toEqual({ outline: true, raw: false, rawLabel: 'Source', expandLabel: 'Expand all', collapseLabel: 'Collapse all' })
    expect(state.yaml).toEqual({
      outline: false,
      code: 'kind: Model\nspec:\n  displayName: Customers\n',
      language: 'yaml',
      label: 'Source YAML configuration',
    })
    expect(state.json).toEqual({ code: '{"kind":"Model","count":2}', language: 'json', rawLabel: 'Source' })
    expect(state.outlineAfterToggle).toBe(true)
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #fff; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-accent: #0969da; --lv-fg-warning: #9a6700; --fgColor-attention: #9a6700; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --lv-radius-small: 4px; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --control-medium-size: 32px; --focus-outline: 2px solid #0969da; }
        </style>
      </head>
      <body>
        <lv-config-viewer></lv-config-viewer>
        <script type="module" src="/config-viewer-under-test.js"></script>
      </body>
    </html>
  `
}
