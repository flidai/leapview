import { afterAll, beforeAll } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { builderTestDocument } from './dashboard-builder-test-fixtures'

export function dashboardBuilderBrowserFixture(): { browser: Browser; baseURL: string } {
  let server: Server
  const fixture = { browser: undefined as unknown as Browser, baseURL: '' }
  const projectRoot = process.cwd()
  const root = join(projectRoot, '.tmp/dashboard-builder-test')

  beforeAll(async () => {
    server = createServer(async (request, response) => {
      const url = new URL(request.url ?? '/', 'http://127.0.0.1')
      if (url.pathname === '/') {
        response.setHeader('content-type', 'text/html')
        response.end(builderTestDocument())
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
    if (!address || typeof address === 'string') throw new Error('dashboard builder test server did not bind')
    fixture.baseURL = `http://127.0.0.1:${address.port}`
    fixture.browser = await chromium.launch()
  })

  afterAll(async () => {
    await fixture.browser?.close()
    await new Promise<void>((resolve, reject) => server?.close((error) => error ? reject(error) : resolve()))
  }, 15_000)
  return fixture
}
