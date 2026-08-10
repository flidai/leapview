import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/code-editor-test')

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
      response.setHeader('content-type', file.endsWith('.css') ? 'text/css' : 'text/javascript')
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

test('code editor initializes Monaco, syncs values, emits changes, and disposes', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-code-editor'))

    const state = await page.evaluate(async () => {
      const waitFor = async (predicate: () => boolean, timeoutMs = 5000): Promise<void> => {
        const started = performance.now()
        while (!predicate()) {
          if (performance.now() - started > timeoutMs) throw new Error('timed out waiting for condition')
          await new Promise((resolve) => setTimeout(resolve, 20))
        }
      }
      const element = document.createElement('lv-code-editor') as any
      element.value = '# Initial prompt'
      element.language = 'markdown'
      element.ariaLabel = 'System prompt'
      const changes: unknown[] = []
      element.addEventListener('lv-code-editor-change', (event: CustomEvent) => changes.push(event.detail))
      document.body.append(element)
      await element.updateComplete
      await waitFor(() => Boolean(element.editor))
      await waitFor(() => Boolean(element.shadowRoot.querySelector('.view-line')))

      const root = element.shadowRoot
      const stylesheetLoaded = Boolean(root.querySelector<HTMLLinkElement>('link[data-monaco-styles]')?.sheet)
      const monacoSurface = root.querySelector('.monaco-editor')
      const monacoBackground = getComputedStyle(monacoSurface!).backgroundColor
      const monacoFontSize = getComputedStyle(root.querySelector('.view-line')!).fontSize
      const overflowGuardPosition = getComputedStyle(root.querySelector('.overflow-guard')!).position
      const gutterWidth = root.querySelector('.margin')!.getBoundingClientRect().width
      const cursorBackground = getComputedStyle(root.querySelector('.cursors-layer > .cursor')!).backgroundColor
      const cursorWidth = getComputedStyle(root.querySelector('.cursors-layer > .cursor')!).width
      const activeLineNumberColor = getComputedStyle(root.querySelector('.line-numbers.active-line-number')!).color
      document.documentElement.style.colorScheme = 'dark'
      document.dispatchEvent(new CustomEvent('leapview-theme-applied', { detail: { mode: 'dark', resolvedMode: 'dark' } }))
      await waitFor(() => getComputedStyle(monacoSurface!).backgroundColor === 'rgb(13, 17, 23)')
      const darkMonacoBackground = getComputedStyle(monacoSurface!).backgroundColor
      const darkGutterBackground = getComputedStyle(root.querySelector('.margin')!).backgroundColor
      const darkCursorBackground = getComputedStyle(root.querySelector('.cursors-layer > .cursor')!).backgroundColor
      const darkActiveLineNumberColor = getComputedStyle(root.querySelector('.line-numbers.active-line-number')!).color
      const shellRect = root.querySelector('.editor-shell')!.getBoundingClientRect()
      const firstLineRect = root.querySelector('.view-line')!.getBoundingClientRect()
      const initialValue = element.editor.getValue()
      element.value = '# External update'
      await element.updateComplete
      const syncedValue = element.editor.getValue()

      element.editor.setValue('# Edited in Monaco')
      await waitFor(() => changes.length > 0)

      element.disabled = true
      await element.updateComplete
      const readOnly = element.editor.getOption(104)

      element.remove()
      return {
        hasMonacoSurface: Boolean(monacoSurface),
        monacoBackground,
        monacoFontSize,
        gutterWidth: Math.round(gutterWidth),
        cursorBackground,
        cursorWidth,
        activeLineNumberColor,
        darkMonacoBackground,
        darkGutterBackground,
        darkCursorBackground,
        darkActiveLineNumberColor,
        stylesheetLoaded,
        overflowGuardPosition,
        firstLineInsideShell: firstLineRect.top >= shellRect.top && firstLineRect.bottom <= shellRect.bottom,
        initialValue,
        syncedValue,
        changes,
        readOnly,
        disposed: !element.editor && !element.model,
      }
    })

    expect(state.hasMonacoSurface).toBe(true)
    expect(state.monacoBackground).toBe('rgb(255, 255, 255)')
    expect(state.monacoFontSize).toBe('13px')
    expect(state.gutterWidth).toBeGreaterThanOrEqual(32)
    expect(state.gutterWidth).toBeLessThanOrEqual(46)
    expect(state.cursorBackground).toBe('rgb(4, 66, 137)')
    expect(state.cursorWidth).toBe('1px')
    expect(state.activeLineNumberColor).toBe('rgb(36, 41, 46)')
    expect(state.darkMonacoBackground).toBe('rgb(13, 17, 23)')
    expect(state.darkGutterBackground).toBe('rgb(13, 17, 23)')
    expect(state.darkCursorBackground).toBe('rgb(200, 225, 255)')
    expect(state.darkActiveLineNumberColor).toBe('rgb(225, 228, 232)')
    expect(state.stylesheetLoaded).toBe(true)
    expect(state.overflowGuardPosition).toBe('relative')
    expect(state.firstLineInsideShell).toBe(true)
    expect(state.initialValue).toBe('# Initial prompt')
    expect(state.syncedValue).toBe('# External update')
    expect(state.changes).toEqual([{ value: '# Edited in Monaco' }])
    expect(state.readOnly).toBe(true)
    expect(state.disposed).toBe(true)
  } finally {
    await page.close()
  }
})

test('code editor initializes Monaco from value attribute', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-code-editor'))

    const state = await page.evaluate(async () => {
      const waitFor = async (predicate: () => boolean, timeoutMs = 5000): Promise<void> => {
        const started = performance.now()
        while (!predicate()) {
          if (performance.now() - started > timeoutMs) throw new Error('timed out waiting for condition')
          await new Promise((resolve) => setTimeout(resolve, 20))
        }
      }
      const element = document.createElement('lv-code-editor') as any
      element.setAttribute('value', 'Attribute seeded prompt')
      element.language = 'markdown'
      document.body.append(element)
      await element.updateComplete
      const hasLoadingTextarea = Boolean(element.shadowRoot.querySelector('textarea.loading-editor'))
      await waitFor(() => Boolean(element.editor))
      const initialModelValue = element.editor.getValue()
      element.editor.setValue('')
      await waitFor(() => element.value === '' && element.editor.getValue() === '')
      return {
        attr: element.getAttribute('value'),
        prop: element.value,
        initialModelValue,
        modelValue: element.editor.getValue(),
        hasLoadingTextarea,
      }
    })

    expect(state.attr).toBe('Attribute seeded prompt')
    expect(state.hasLoadingTextarea).toBe(false)
    expect(state.initialModelValue).toBe('Attribute seeded prompt')
    expect(state.prop).toBe('')
    expect(state.modelValue).toBe('')
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
          body { --fontStack-system: system-ui; --fontStack-monospace: ui-monospace; --text-codeBlock-size: 13px; --base-text-weight-normal: 400; --base-text-weight-medium: 500; --base-text-weight-semibold: 600; --base-text-lineHeight-tight: 1.25; --base-text-lineHeight-snug: 1.375; --base-text-lineHeight-normal: 1.5; --base-text-lineHeight-relaxed: 1.625; --lv-type-caption: 400 12px/1.25 system-ui; --lv-type-secondary: 400 12px/1.625 system-ui; --lv-type-body: 400 14px/1.5 system-ui; --lv-type-body-large: 400 16px/1.5 system-ui; --lv-type-section-title: 600 16px/1.5 system-ui; --lv-type-page-title: 600 20px/1.625 system-ui; --lv-type-title-large: 600 32px/1.5 system-ui; --lv-type-code-block: 400 13px/1.5 ui-monospace; --lv-type-code-inline: 400 0.9285em ui-monospace; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-accent-muted: #ddf4ff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-accent: #0969da; --lv-icon-muted: #57606a; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --base-size-8: 8px; --base-size-16: 16px; }
          [data-color-mode='dark'] { --lv-bg-panel: #0d1117; }
          lv-code-editor { display: block; width: 760px; margin: 24px; }
        </style>
      </head>
      <body>
        <script type="module" src="/code-editor-under-test.js"></script>
      </body>
    </html>
  `
}
