import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, expect as browserExpect, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

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
      await waitFor(() => Boolean((element.shadowRoot as ShadowRoot).querySelector('.view-line')))

      const root = (element.shadowRoot as ShadowRoot)
      const stylesheetLoaded = Boolean(root.querySelector<HTMLLinkElement>('link[data-monaco-styles]')?.sheet)
      const monacoSurface = root.querySelector('.monaco-editor')
      const monacoBackground = getComputedStyle(monacoSurface!).backgroundColor
      const monacoFontSize = getComputedStyle(root.querySelector('.view-line')!).fontSize
      const overflowGuardPosition = getComputedStyle(root.querySelector('.overflow-guard')!).position
      const gutterWidth = root.querySelector('.margin')!.getBoundingClientRect().width
      const cursorBackground = getComputedStyle(root.querySelector('.cursors-layer > .cursor')!).backgroundColor
      const cursorWidth = getComputedStyle(root.querySelector('.cursors-layer > .cursor')!).width
      const activeLineNumberColor = getComputedStyle(root.querySelector('.line-numbers.active-line-number')!).color
      document.documentElement.dataset.colorMode = 'dark'
      document.documentElement.dataset.lightTheme = 'light'
      document.documentElement.dataset.darkTheme = 'dark'
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
      const hasLoadingTextarea = Boolean((element.shadowRoot as ShadowRoot).querySelector('textarea.loading-editor'))
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

test('code editor follows container and viewport resizing so wrapped source stays visible', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  const source = '# ' + 'Dashboard YAML remains readable when the container narrows. '.repeat(5) + 'tail-marker'
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-code-editor'))
    await page.evaluate((source) => {
      const element = document.createElement('lv-code-editor') as any
      element.value = source
      element.style.width = 'min(760px, calc(100vw - 48px))'
      document.body.append(element)
    }, source)
    const editor = page.locator('lv-code-editor')
    await browserExpect(editor.locator('.monaco-editor')).toBeVisible()
    for (const viewportWidth of [900, 390, 1200]) {
      await page.setViewportSize({ width: viewportWidth, height: 700 })
      await browserExpect.poll(() => editor.evaluate((element: any) => {
        const shell = element.shadowRoot.querySelector('.editor-shell')
        return Math.abs(element.editor.getLayoutInfo().width - shell.clientWidth)
      })).toBeLessThanOrEqual(1)
    }
    await editor.evaluate((element: any) => { element.style.width = '320px' })
    await browserExpect.poll(() => editor.evaluate((element: any) => {
      const shell = element.shadowRoot.querySelector('.editor-shell')
      return Math.abs(element.editor.getLayoutInfo().width - shell.clientWidth)
    })).toBeLessThanOrEqual(1)
    const state = await editor.evaluate((element: any) => {
      const root = element.shadowRoot
      const shell = root.querySelector('.editor-shell')
      const position = { lineNumber: 1, column: element.editor.getValue().length + 1 }
      element.editor.revealPositionInCenter(position)
      const tail = element.editor.getScrolledVisiblePosition(position)
      return {
        source: element.editor.getValue(),
        wrapping: element.editor.getLayoutInfo().isViewportWrapping,
        horizontalOverflow: shell.scrollWidth > shell.clientWidth,
        tailFits: tail !== null && tail.left >= element.editor.getLayoutInfo().contentLeft && tail.left <= element.editor.getLayoutInfo().width,
      }
    })
    expect(state).toEqual({ source, wrapping: true, horizontalOverflow: false, tailFits: true })
  } finally { await page.close() }
})

test('code editor reconnects with retained edits and one model and change listener per lifetime', async () => {
  const page = await browser.newPage()
  page.setDefaultTimeout(5000)
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-code-editor'))
    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-code-editor') as any
      element.setAttribute('value', 'Attribute seed')
      const changes: string[] = []
      element.addEventListener('lv-code-editor-change', (event: CustomEvent) => changes.push(event.detail.value))
      const waitForEditor = async () => {
        const deadline = performance.now() + 5000
        while (!element.editor) {
          if (performance.now() > deadline) throw new Error('editor did not initialize after reconnect')
          await new Promise(resolve => setTimeout(resolve, 20))
        }
      }
      document.body.append(element)
      await waitForEditor()
      const disposed: boolean[] = []
      const restored: string[] = []
      const surfaces: number[] = []
      for (const value of ['Edited YAML', '']) {
        element.editor.setValue(value)
        await element.updateComplete
        const previousEditor = element.editor
        const previousModel = element.model
        element.remove()
        disposed.push(!element.editor && !element.model && previousModel.isDisposed())
        document.body.append(element)
        await waitForEditor()
        if (element.editor === previousEditor) throw new Error('disposed editor was reused')
        restored.push(element.editor.getValue())
        surfaces.push(element.shadowRoot.querySelectorAll('.monaco-editor').length)
      }
      element.editor.setValue('Final edit')
      element.remove()
      return { disposed, restored, surfaces, changes }
    })
    expect(state).toEqual({
      disposed: [true, true], restored: ['Edited YAML', ''], surfaces: [1, 1],
      changes: ['Edited YAML', '', 'Final edit'],
    })
  } finally { await page.close() }
}, 15000)

for (const reconnectBeforeStylesLoad of [false, true]) {
  test(`code editor survives disconnect during stylesheet initialization (${reconnectBeforeStylesLoad ? 'reconnect before load' : 'reconnect after load'})`, async () => {
    const page = await browser.newPage()
    page.setDefaultTimeout(5000)
    let releaseStyles!: () => void
    const stylesGate = new Promise<void>(resolve => { releaseStyles = resolve })
    let requestedStyles!: () => void
    const stylesRequested = new Promise<void>(resolve => { requestedStyles = resolve })
    await page.route('**/static/monaco-editor-css.css', async route => {
      requestedStyles()
      await stylesGate
      await route.continue()
    })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-code-editor'))
      await page.evaluate(async () => {
        const element = document.createElement('lv-code-editor') as any
        element.value = 'Pending initialization'
        ;(window as any).pendingEditor = element
        document.body.append(element)
        await element.updateComplete
      })
      await stylesRequested
      await page.evaluate(() => {
        const element = (window as any).pendingEditor
        if (element.editor || element.model) throw new Error('stylesheet wait was not exercised')
        element.remove()
      })
      if (reconnectBeforeStylesLoad) await page.evaluate(() => document.body.append((window as any).pendingEditor))
      releaseStyles()
      if (!reconnectBeforeStylesLoad) {
        // Detached shadow stylesheets need not retain a sheet in Chromium.
        // Settle the pending load signal and let its continuation run detached.
        await page.evaluate(async () => {
          ;(window as any).pendingEditor.shadowRoot.querySelector('link[data-monaco-styles]').dispatchEvent(new Event('load'))
          await new Promise(resolve => setTimeout(resolve, 0))
        })
        await page.evaluate(() => document.body.append((window as any).pendingEditor))
      }
      await page.waitForFunction(() => Boolean((window as any).pendingEditor.editor))
      const state = await page.evaluate(async () => {
        const element = (window as any).pendingEditor
        await element.updateComplete
        const changes: string[] = []
        element.addEventListener('lv-code-editor-change', (event: CustomEvent) => changes.push(event.detail.value))
        const value = element.editor.getValue()
        const surfaces = element.shadowRoot.querySelectorAll('.monaco-editor').length
        element.editor.setValue('After reconnect')
        const model = element.model
        element.remove()
        return { value, surfaces, changes, disposed: !element.editor && !element.model && model.isDisposed() }
      })
      expect(state).toEqual({ value: 'Pending initialization', surfaces: 1, changes: ['After reconnect'], disposed: true })
    } finally {
      releaseStyles()
      await page.close()
    }
  }, 15000)
}

function testDocument(): string {
  return `
    <!doctype html>
    <html data-color-mode="light" data-light-theme="light" data-dark-theme="dark" style="color-scheme: light">
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          :root { ${typographyTestTokens} --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-accent-muted: #ddf4ff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-accent: #0969da; --lv-icon-muted: #57606a; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --base-size-8: 8px; --base-size-16: 16px; }
          [data-color-mode='dark'] { --lv-bg-panel: #0d1117; --lv-fg-muted: #9198a1; }
          lv-code-editor { display: block; width: 760px; margin: 24px; }
        </style>
      </head>
      <body>
        <script type="module" src="/code-editor-under-test.js"></script>
      </body>
    </html>
  `
}
