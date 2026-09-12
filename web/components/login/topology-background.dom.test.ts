import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/topology-background-test')

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

test('topology background renders and animates without external requests', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 500 } })
  const externalRequests: string[] = []
  const pageErrors: string[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== baseURL) externalRequests.push(request.url())
  })
  page.on('pageerror', (error) => pageErrors.push(error.message))

  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const host = document.querySelector('lv-topology-background')
      const canvas = host?.shadowRoot?.querySelector('canvas') as HTMLCanvasElement | null
      if (!canvas || canvas.width === 0 || canvas.height === 0) return false
      const pixels = canvas.getContext('2d')?.getImageData(0, 0, canvas.width, canvas.height).data
      return Boolean(pixels && pixels.some((value, index) => index % 4 === 3 && value > 0))
    })

    const firstFrame = await canvasDataURL(page)
    await page.waitForTimeout(300)
    const animatedFrame = await canvasDataURL(page)
    expect(animatedFrame).not.toBe(firstFrame)

    await page.setViewportSize({ width: 900, height: 600 })
    await page.waitForTimeout(150)
    const secondFrame = await canvasDataURL(page)

    expect(secondFrame).not.toBe(firstFrame)
    expect(externalRequests).toEqual([])
    expect(pageErrors).toEqual([])
  } finally {
    await page.close()
  }
}, 20_000)

async function canvasDataURL(page: import('@playwright/test').Page): Promise<string> {
  return page.locator('lv-topology-background').evaluate((host) => {
    const canvas = host.shadowRoot?.querySelector('canvas') as HTMLCanvasElement | null
    if (!canvas) throw new Error('topology canvas is missing')
    return canvas.toDataURL()
  })
}

function testDocument(): string {
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; width: 100%; height: 100%; }
          :root { --lv-accent: #0969da; --bgColor-accent-emphasis: #0969da; --lv-topology-bg: #0d1117; --bgColor-inverse: #0d1117; }
          lv-topology-background { position: fixed; inset: 0; }
        </style>
      </head>
      <body>
        <lv-topology-background></lv-topology-background>
        <script type="module" src="/topology-background-under-test.js"></script>
      </body>
    </html>
  `
}
