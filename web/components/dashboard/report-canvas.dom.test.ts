import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/report-canvas-test')

beforeAll(async () => {
  await Bun.$`rm -rf ${root}`.quiet()
  const built = await Bun.build({
    entrypoints: [
      join(projectRoot, 'web/components/dashboard/report-canvas.ts'),
      join(projectRoot, 'web/components/dashboard/report-view-controls.ts'),
      join(projectRoot, 'web/components/dashboard/filters/url-sync.ts'),
    ],
    target: 'browser',
    format: 'esm',
    outdir: root,
    naming: { entry: '[name].js' },
  })
  if (!built.success) throw new Error('failed to build report canvas test bundles')

  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/report-canvas.js' || url.pathname === '/report-view-controls.js' || url.pathname === '/url-sync.js') {
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
      return
    }
    response.setHeader('content-type', 'text/html')
    response.end(url.pathname === '/scoped-zoom' ? scopedZoomDocument() : testDocument())
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('report canvas test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('report canvas restores route-scoped view preferences through URL sync history', async () => {
  const page = await browser.newPage({ viewport: { width: 1000, height: 700 } })
  try {
    await page.addInitScript(() => {
      localStorage.setItem('leapview-report-layout:/report-a', 'desktop')
      localStorage.setItem('leapview-report-zoom:/report-a', 'custom')
      localStorage.setItem('leapview-report-zoom-scale:/report-a', '1.5')
      localStorage.setItem('leapview-report-layout:/report-b', 'auto')
      localStorage.setItem('leapview-report-zoom:/report-b', 'fit-page')
      localStorage.setItem('leapview-report-zoom-scale:/report-b', '0.4')
    })
    await page.goto(`${baseURL}/report-a`)
    await page.waitForFunction(() => customElements.get('lv-report-canvas') && Boolean((window as any).DatastarURLSync))

    const waitForState = async (pathname: string, layoutMode: string, layout: string, mode: string, scale: number) => {
      await page.waitForFunction(({ pathname: expectedPathname, layoutMode: expectedLayoutMode, layout: expectedLayout, mode: expectedMode, scale: expectedScale }) => {
        const canvas = document.querySelector('lv-report-canvas') as any
        const surface = document.querySelector('lv-report-canvas')?.shadowRoot?.querySelector('.surface') as HTMLElement | null
        return canvas?.layoutMode === expectedLayoutMode
          && window.location.pathname === expectedPathname
          && surface?.dataset.layout === expectedLayout
          && surface?.dataset.presentationMode === expectedMode
          && Math.abs(Number(surface?.dataset.scale) - expectedScale) < 0.001
      }, { pathname, layoutMode, layout, mode, scale })
    }

    await waitForState('/report-a', 'desktop', 'desktop', 'custom', 1.5)

    await page.evaluate(() => {
      const urlSync = (window as any).DatastarURLSync
      if (!urlSync) throw new Error('Datastar URL sync was not installed')
      urlSync.push({}, '/report-b')
    })
    await waitForState('/report-b', 'auto', 'desktop', 'fit-page', 0.725)

    await page.goBack({ waitUntil: 'load' })
    await waitForState('/report-a', 'desktop', 'desktop', 'custom', 1.5)

    await page.goForward({ waitUntil: 'load' })
    await waitForState('/report-b', 'auto', 'desktop', 'fit-page', 0.725)
  } finally {
    await page.close()
  }
}, 20_000)

function testDocument(): string {
  return `<!doctype html><html><head><style>html,body{width:100%;height:100%;margin:0;}lv-report-canvas{display:block;width:100%;height:100%;--lv-report-canvas-bg:#eef1f4;--lv-report-page-bg:#fff;--lv-scrollbar-thumb:#8c959f;--lv-scrollbar-thumb-hover:#6e7781;}</style></head><body><lv-report-canvas width="1366" height="768"></lv-report-canvas><script type="module" src="/report-canvas.js"></script><script type="module" src="/url-sync.js"></script></body></html>`
}

test('mobile cards use compact chart heights and content-sized table heights', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => document.querySelector('lv-report-canvas')?.getAttribute('data-layout') === 'mobile')
    await page.locator('lv-report-canvas').evaluate(canvas => {
      for (const kind of ['cartesian', 'matrix']) {
        const card = document.createElement('div')
        card.setAttribute('data-canvas-visual', '')
        card.dataset.componentKind = 'visual'
        card.dataset.visualType = kind
        if (kind === 'matrix') card.style.setProperty('--lv-mobile-table-height', '254px')
        canvas.append(card)
      }
    })
    expect(await page.locator('[data-visual-type="cartesian"]').evaluate(e => e.getBoundingClientRect().height)).toBe(360)
    expect(await page.locator('[data-visual-type="matrix"]').evaluate(e => e.getBoundingClientRect().height)).toBe(254)
  } finally { await page.close() }
})

test('report zoom commands and state stay within their owning report scope', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 700 } })
  try {
    await page.goto(`${baseURL}/scoped-zoom`)
    await page.waitForFunction(() => (
      customElements.get('lv-report-canvas')
      && customElements.get('lv-report-zoom')
      && document.querySelectorAll('[data-report-view-scope] lv-report-canvas').length === 2
    ))
    const state = await page.evaluate(async () => {
      const canvases = Array.from(document.querySelectorAll('[data-report-view-scope] lv-report-canvas')) as any[]
      const controls = Array.from(document.querySelectorAll('[data-report-view-scope] lv-report-zoom')) as any[]
      await Promise.all([...canvases, ...controls].map((element) => element.updateComplete))
      const readScales = () => canvases.map((canvas) => Number((canvas.shadowRoot as ShadowRoot).querySelector('.surface')?.getAttribute('data-scale')))
      const readPercentages = () => controls.map((control) => (control.shadowRoot as ShadowRoot).querySelector('input')?.getAttribute('aria-valuetext'))
      const before = { scales: readScales(), percentages: readPercentages() }
      const firstZoomIn = (controls[0].shadowRoot as ShadowRoot).querySelector('[aria-label="Zoom in"]') as HTMLButtonElement
      firstZoomIn.click()
      await Promise.all([...canvases, ...controls].map((element) => element.updateComplete))
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const afterFirst = { scales: readScales(), percentages: readPercentages() }
      const secondZoomIn = (controls[1].shadowRoot as ShadowRoot).querySelector('[aria-label="Zoom in"]') as HTMLButtonElement
      secondZoomIn.click()
      await Promise.all([...canvases, ...controls].map((element) => element.updateComplete))
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const afterSecond = { scales: readScales(), percentages: readPercentages() }
      return { before, afterFirst, afterSecond }
    })

    expect(state.afterFirst.scales[0]).toBeGreaterThan(state.before.scales[0])
    expect(state.afterFirst.scales[1]).toBe(state.before.scales[1])
    expect(state.afterFirst.percentages[0]).not.toBe(state.before.percentages[0])
    expect(state.afterFirst.percentages[1]).toBe(state.before.percentages[1])
    expect(state.afterSecond.scales[0]).toBe(state.afterFirst.scales[0])
    expect(state.afterSecond.scales[1]).toBeGreaterThan(state.afterFirst.scales[1])
    expect(state.afterSecond.percentages[0]).toBe(state.afterFirst.percentages[0])
    expect(state.afterSecond.percentages[1]).not.toBe(state.afterFirst.percentages[1])
  } finally {
    await page.close()
  }
})

function scopedZoomDocument(): string {
  return `<!doctype html><html><head><style>html,body{width:100%;height:100%;margin:0;}body{display:flex;gap:16px;}[data-report-view-scope]{display:block;width:500px;height:600px;}lv-report-canvas{display:block;width:100%;height:100%;--lv-report-canvas-bg:#eef1f4;--lv-report-page-bg:#fff;--lv-scrollbar-thumb:#8c959f;--lv-scrollbar-thumb-hover:#6e7781;}lv-report-zoom{display:block;height:40px;}</style></head><body><div data-report-view-scope><lv-report-canvas width="1366" height="768"></lv-report-canvas><lv-report-zoom></lv-report-zoom></div><div data-report-view-scope><lv-report-canvas width="1366" height="768"></lv-report-canvas><lv-report-zoom></lv-report-zoom></div><script type="module" src="/report-canvas.js"></script><script type="module" src="/report-view-controls.js"></script></body></html>`
}
