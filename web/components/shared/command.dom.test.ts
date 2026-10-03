import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''
let droppedAttempts = 0
let heldAttempts = 0
let releaseHeldResponses: (() => void) | undefined
let heldResponseGate: Promise<void>

const runtimePath = join(process.cwd(), 'static/vendor/datastar-1.0.2.js')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    if (url.pathname === '/static/vendor/datastar-1.0.2.js') {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(runtimePath))
      return
    }
    if (url.pathname === '/drop') {
      response.destroy()
      return
    }
    if (url.pathname === '/held') {
      heldAttempts++
      await heldResponseGate
      if (!response.destroyed) response.writeHead(204).end()
      return
    }
    response.writeHead(404).end('not found')
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  releaseHeldResponses?.()
  await browser?.close()
  server?.closeAllConnections()
  if (server?.listening) {
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  }
}, 15_000)

test('Datastar does not retry a dropped non-replayable POST', async () => {
  droppedAttempts = 0
  const page = await browser.newPage()
  try {
    await page.route('**/drop', async (route) => {
      droppedAttempts++
      await route.abort('failed')
    })
    await page.goto(baseURL)
    await page.locator('#drop').click()
    await new Promise((resolve) => setTimeout(resolve, 1_200))
    expect(droppedAttempts).toBe(1)
  } finally {
    await page.close()
  }
})

test('Datastar does not resubmit a non-replayable POST when the page becomes visible', async () => {
  heldAttempts = 0
  heldResponseGate = new Promise<void>((resolve) => { releaseHeldResponses = resolve })
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const firstRequest = page.waitForRequest((request) => request.url().endsWith('/held'))
    await page.locator('#held').click()
    await firstRequest
    await page.evaluate(() => {
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => true })
      document.dispatchEvent(new Event('visibilitychange'))
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => false })
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(heldAttempts).toBe(1)
  } finally {
    releaseHeldResponses?.()
    await page.close()
  }
})

function testDocument(): string {
  return `<!doctype html><html><body>
    <button id="drop" data-on:click="@post('/drop', {retry: 'never', retryMaxCount: 0, openWhenHidden: true})">Drop</button>
    <button id="held" data-on:click="@post('/held', {retry: 'never', retryMaxCount: 0, openWhenHidden: true})">Hold</button>
    <script type="module" src="/static/vendor/datastar-1.0.2.js?v=test"></script>
  </body></html>`
}
