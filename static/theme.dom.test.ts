import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    if (url.pathname === '/saved-theme') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument('dark'))
      return
    }
    if (url.pathname === '/theme.js') {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(join(process.cwd(), 'static/theme.js'), 'utf8'))
      return
    }
    response.writeHead(404)
    response.end('not found')
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

test('theme bootstrap does not emit applied event during page reveal', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (window as any).themeBooted === true)
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))

    const state = await page.evaluate(() => ({
      events: (window as any).themeAppliedEvents,
      colorMode: document.documentElement.dataset.colorMode,
      colorScheme: document.documentElement.style.colorScheme,
      storedMode: localStorage.getItem('leapview-color-mode'),
    }))

    expect(state).toEqual({
      events: [],
      colorMode: 'light',
      colorScheme: 'light',
      storedMode: 'light',
    })
  } finally {
    await page.close()
  }
})

test('explicit theme changes still emit applied event', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (window as any).themeBooted === true)
    await page.evaluate(() => {
      ;(window as any).themeAppliedEvents = []
      document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: 'dark_dimmed' } }))
    })
    await page.waitForFunction(() => (window as any).themeAppliedEvents.length === 1)

    const state = await page.evaluate(() => ({
      events: (window as any).themeAppliedEvents,
      colorMode: document.documentElement.dataset.colorMode,
      darkTheme: document.documentElement.dataset.darkTheme,
      colorScheme: document.documentElement.style.colorScheme,
      storedMode: localStorage.getItem('leapview-color-mode'),
    }))

    expect(state).toEqual({
      events: [{ mode: 'dark_dimmed', resolvedMode: 'dark' }],
      colorMode: 'dark',
      darkTheme: 'dark_dimmed',
      colorScheme: 'dark',
      storedMode: 'dark_dimmed',
    })
  } finally {
    await page.close()
  }
})

test('supported Primer themes apply their native color mode and theme identifiers', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (window as any).themeBooted === true)
    const states = await page.evaluate(async () => {
      const preferences = [
        'light',
        'dark',
        'dark_dimmed',
        'light_colorblind',
        'dark_colorblind',
        'light_tritanopia',
        'dark_tritanopia',
      ]
      const results = []
      for (const mode of preferences) {
        document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode } }))
        await new Promise((resolve) => requestAnimationFrame(resolve))
        results.push({
          mode,
          colorMode: document.documentElement.dataset.colorMode,
          lightTheme: document.documentElement.dataset.lightTheme,
          darkTheme: document.documentElement.dataset.darkTheme,
        })
      }
      return results
    })

    expect(states).toEqual([
      { mode: 'light', colorMode: 'light', lightTheme: 'light', darkTheme: 'dark' },
      { mode: 'dark', colorMode: 'dark', lightTheme: 'light', darkTheme: 'dark' },
      { mode: 'dark_dimmed', colorMode: 'dark', lightTheme: 'light', darkTheme: 'dark_dimmed' },
      { mode: 'light_colorblind', colorMode: 'light', lightTheme: 'light_colorblind', darkTheme: 'dark' },
      { mode: 'dark_colorblind', colorMode: 'dark', lightTheme: 'light', darkTheme: 'dark_colorblind' },
      { mode: 'light_tritanopia', colorMode: 'light', lightTheme: 'light_tritanopia', darkTheme: 'dark' },
      { mode: 'dark_tritanopia', colorMode: 'dark', lightTheme: 'light', darkTheme: 'dark_tritanopia' },
    ])
  } finally {
    await page.close()
  }
})

test('authenticated saved theme overrides stale browser storage', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/saved-theme`)
    await page.waitForFunction(() => (window as any).themeBooted === true)
    const state = await page.evaluate(() => ({
      colorMode: document.documentElement.dataset.colorMode,
      storedMode: localStorage.getItem('leapview-color-mode'),
    }))
    expect(state).toEqual({ colorMode: 'dark', storedMode: 'dark' })
  } finally {
    await page.close()
  }
})

test('view transition abort rejections are treated as progressive enhancement misses', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (window as any).themeBooted === true)
    await page.evaluate(() => {
      ;(window as any).unhandledRejectionMessages = []
      window.addEventListener('unhandledrejection', (event) => {
        if (!event.defaultPrevented) {
          ;(window as any).unhandledRejectionMessages.push(event.reason?.message ?? String(event.reason))
        }
      })
      Promise.reject(new DOMException('Transition was skipped', 'AbortError'))
      Promise.reject(new DOMException('Transition was aborted because of invalid state', 'InvalidStateError'))
      Promise.reject(new Error('real failure'))
    })
    await page.waitForFunction(() => (window as any).unhandledRejectionMessages.length === 1)

    const messages = await page.evaluate(() => (window as any).unhandledRejectionMessages)
    expect(messages).toEqual(['real failure'])
  } finally {
    await page.close()
  }
})

function testDocument(savedTheme = ''): string {
  return `
    <!doctype html>
    <html data-color-mode="auto" data-light-theme="light" data-dark-theme="dark"${savedTheme ? ` data-theme-preference="${savedTheme}"` : ''}>
      <head>
        <script>
          localStorage.setItem('leapview-color-mode', 'light');
          window.themeAppliedEvents = [];
          document.addEventListener('leapview-theme-applied', (event) => {
            window.themeAppliedEvents.push(event.detail);
          });
        </script>
        <script type="module" src="/theme.js"></script>
        <script type="module">
          window.themeBooted = true;
        </script>
      </head>
      <body>
        <button data-theme-toggle type="button">
          <span data-theme-icon="system"></span>
          <span data-theme-icon="light"></span>
          <span data-theme-icon="dark"></span>
        </button>
      </body>
    </html>
  `
}
