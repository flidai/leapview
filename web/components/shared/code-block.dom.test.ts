import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/code-block-test')

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

test('code block highlights product and documentation languages with GitHub themes', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-code-block'))

    const state = await page.evaluate(async () => {
      const waitFor = async (predicate: () => boolean, timeoutMs = 5000): Promise<void> => {
        const started = performance.now()
        while (!predicate()) {
          if (performance.now() - started > timeoutMs) throw new Error('timed out waiting for condition')
          await new Promise((resolve) => setTimeout(resolve, 20))
        }
      }

      document.documentElement.style.colorScheme = 'light'
      const json = document.createElement('lv-code-block') as any
      json.language = 'json'
      json.code = '{\n  "ok": true,\n  "count": 3\n}'
      document.body.append(json)
      await waitFor(() => Boolean(json.querySelector('.shiki')))

      const toon = document.createElement('lv-code-block') as any
      toon.language = 'toon'
      toon.code = 'items[2]{id,title}:\n  1,Sales\n  2,Ops\ncount: 2'
      document.body.append(toon)
      await waitFor(() => Boolean(toon.querySelector('.shiki')))

      const yaml = document.createElement('lv-code-block') as any
      yaml.language = 'yaml'
      yaml.code = 'apiVersion: leapview.dev/v1\nkind: Project'
      yaml.highlightedLines = [2]
      yaml.copy = true
      yaml.toolbar = true
      document.body.append(yaml)
      await waitFor(() => Boolean(yaml.querySelector('.shiki.github-light')))
      yaml.focusLines([2])
      const yamlFocusedLines = [...yaml.querySelectorAll('.line.code-block-focused-line')].map((line) => line.textContent?.trim())
      yaml.clearFocusedLines()

      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
      document.execCommand = (command) => {
        document.documentElement.dataset.copiedCode = (document.activeElement as HTMLTextAreaElement | null)?.value ?? ''
        return command === 'copy'
      }
      ;(yaml.querySelector('.code-block-copy') as HTMLButtonElement).click()
      await waitFor(() => yaml.querySelector('.code-block-copy')?.getAttribute('aria-label') === 'Code copied')

      const shell = document.createElement('lv-code-block') as any
      shell.language = 'sh'
      shell.code = 'leapview validate --project dashboards/leapview.yaml'
      document.body.append(shell)
      await waitFor(() => Boolean(shell.querySelector('.shiki')))

      const formattedSQL = document.createElement('lv-code-block') as any
      formattedSQL.language = 'sql'
      formattedSQL.code = 'select status from orders'
      formattedSQL.format = true
      document.body.append(formattedSQL)
      await formattedSQL.updateComplete

      const text = document.createElement('lv-code-block') as any
      text.language = 'text'
      text.code = 'plain text'
      document.body.append(text)
      await text.updateComplete

      const unknown = document.createElement('lv-code-block') as any
      unknown.language = 'made-up'
      unknown.code = 'fallback text'
      document.body.append(unknown)
      await unknown.updateComplete

      const compact = document.createElement('lv-code-block') as any
      compact.compact = true
      compact.copy = true
      compact.language = 'json'
      compact.code = '{"compact":true}'
      document.body.append(compact)
      await compact.updateComplete
      document.documentElement.style.colorScheme = 'dark'
      document.dispatchEvent(new CustomEvent('leapview-theme-applied'))
      await waitFor(() => Boolean(yaml.querySelector('.shiki.github-dark')))
      const compactPre = compact.querySelector('pre') as HTMLElement

      return {
        jsonHighlighted: Boolean(json.querySelector('.shiki')),
        toonHighlighted: Boolean(toon.querySelector('.shiki')),
        yamlHighlighted: Boolean(yaml.querySelector('.shiki.github-dark')),
        shellHighlighted: Boolean(shell.querySelector('.shiki')),
        formattedSQL: formattedSQL.querySelector('code')?.textContent || '',
        jsonText: json.textContent || '',
        toonText: toon.textContent || '',
        yamlText: yaml.textContent || '',
        yamlLanguage: yaml.querySelector('.code-block-language')?.textContent || '',
        yamlHighlightedLines: [...yaml.querySelectorAll('.line.code-block-highlighted-line')].map((line) => line.textContent?.trim()),
        yamlFocusedLines,
        yamlFocusCleared: !yaml.hasAttribute('data-line-focus') && !yaml.querySelector('.code-block-focused-line'),
        yamlHighlightMarkerWidth: getComputedStyle(yaml.querySelector('.line.code-block-highlighted-line')!, '::before').width,
        yamlCopyLabel: yaml.querySelector('.code-block-copy')?.getAttribute('aria-label') || '',
        copiedCode: document.documentElement.dataset.copiedCode || '',
        textFallback: Boolean(text.querySelector('.code-block-fallback')),
        textError: Boolean(text.querySelector('.code-block-error')),
        unknownFallback: Boolean(unknown.querySelector('.code-block-fallback')),
        unknownError: Boolean(unknown.querySelector('.code-block-error')),
        compactAttr: compact.hasAttribute('compact'),
        compactWhiteSpace: getComputedStyle(compactPre).whiteSpace,
        compactOverflowX: getComputedStyle(compactPre).overflowX,
        compactPaddingTop: getComputedStyle(compactPre).paddingTop,
      }
    })

    expect(state.jsonHighlighted).toBe(true)
    expect(state.toonHighlighted).toBe(true)
    expect(state.yamlHighlighted).toBe(true)
    expect(state.shellHighlighted).toBe(true)
    expect(state.formattedSQL).toContain('SELECT')
    expect(state.formattedSQL).toMatch(/\nFROM\n\s+orders/)
    expect(state.jsonText).toContain('"ok"')
    expect(state.toonText).toContain('items[2]{id,title}:')
    expect(state.yamlText).toContain('apiVersion: leapview.dev/v1')
    expect(state.yamlLanguage).toBe('YAML')
    expect(state.yamlHighlightedLines).toEqual(['kind: Project'])
    expect(state.yamlFocusedLines).toEqual(['kind: Project'])
    expect(state.yamlFocusCleared).toBe(true)
    expect(state.yamlHighlightMarkerWidth).toBe('4px')
    expect(state.yamlCopyLabel).toBe('Code copied')
    expect(state.copiedCode).toBe('apiVersion: leapview.dev/v1\nkind: Project')
    expect(state.textFallback).toBe(true)
    expect(state.textError).toBe(false)
    expect(state.unknownFallback).toBe(true)
    expect(state.unknownError).toBe(false)
    expect(state.compactAttr).toBe(true)
    expect(state.compactWhiteSpace).toBe('pre')
    expect(state.compactOverflowX).toBe('auto')
    expect(state.compactPaddingTop).toBe('8px')
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
          body { --fontStack-monospace: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; --base-text-weight-medium: 500; --base-text-lineHeight-tight: 1.25; --base-text-lineHeight-normal: 1.5; --lv-type-caption: 400 12px/1.25 system-ui; --lv-type-body: 400 14px/1.5 system-ui; --lv-type-code-block: 400 13px/1.5 ui-monospace; --lv-bg-panel-muted: #f6f8fa; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-border-muted: 1px solid #d8dee4; --borderRadius-medium: 6px; --base-size-4: 4px; --base-size-8: 8px; --base-size-12: 12px; --base-size-16: 16px; }
          lv-code-block { display: block; width: 760px; margin: 24px; }
        </style>
      </head>
      <body>
        <script type="module" src="/code-block-under-test.js"></script>
      </body>
    </html>
  `
}
