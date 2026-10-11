import { afterAll, beforeAll } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser, type Page } from '@playwright/test'
import { testVisualizationEnvelopes } from '../dashboard-page-test-fixtures'


/** Fail inside the test so finally releases intercepted requests before Bun's outer deadline. */
export async function hostBrowserStep<T>(name: string, operation: Promise<T>, timeout = 5_000): Promise<T> {
  let timer: ReturnType<typeof setTimeout>
  try {
    return await Promise.race([operation, new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error(`Visualization browser step timed out: ${name}`)), timeout)
    })])
  } catch (error) {
    // Cleanup can also time out; retain the first failing operation in CI logs.
    console.error(`Visualization browser step failed: ${name}`, error)
    throw error
  } finally { clearTimeout(timer!) }
}

export function hostBrowserFixture() {
  const fixture = { browser: undefined as unknown as Browser, baseURL: '', newPage }
  let server: Server
  const projectRoot = process.cwd()
  const fixtureRoot = join(projectRoot, '.tmp/visualization-host-test')

  async function newPage(): Promise<Page> {
    const pending = fixture.browser.newPage()
    try {
      const page = await hostBrowserStep('create loading-test page', pending)
      page.setDefaultTimeout(5_000)
      return page
    } catch (error) {
      // A late protocol response must not leak a page after the test unwinds.
      void pending.then(page => page.close()).catch(() => {})
      throw error
    }
  }

  function testDocument(): string {
    // Exercise the real host and adapters without requiring dashboard/Datastar
    // bootstrap. Route-level behavior stays covered by the dashboard suites.
    return `<!doctype html><html><body>
      <script type="module">
        import '/visualization-host-under-test.js';
        const envelopes = ${JSON.stringify(testVisualizationEnvelopes())};
        const sources = Object.fromEntries(Object.entries(envelopes).map(([id, envelope]) => [id, { envelope }]));
        const eager = document.createElement('lv-visualization-host');
        eager.envelope = envelopes.orders_kpi;
        document.body.append(eager);
        sources.orders_kpi = eager;
        window.__lvSourceHosts = sources;
      </script>
    </body></html>`
  }

  beforeAll(async () => {
    server = createServer(async (request, response) => {
      const url = new URL(request.url ?? '/', 'http://127.0.0.1')
      if (url.pathname === '/') {
        response.setHeader('content-type', 'text/html')
        response.end(testDocument())
        return
      }
      const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : fixtureRoot
      const file = normalize(join(fileRoot, url.pathname))
      if (!file.startsWith(fileRoot)) { response.writeHead(404); response.end('not found'); return }
      try {
        response.setHeader('content-type', file.endsWith('.css') ? 'text/css' : 'text/javascript')
        response.end(await readFile(file))
      } catch { response.writeHead(404); response.end('not found') }
    })
    await new Promise<void>((resolve) => server.listen(0, resolve))
    const address = server.address()
    if (!address || typeof address === 'string') throw new Error('test server did not bind')
    fixture.baseURL = `http://127.0.0.1:${address.port}`
    fixture.browser = await chromium.launch()
  })

  afterAll(async () => {
    try { await hostBrowserStep('close fixture browser', fixture.browser?.close()) }
    finally {
      const closed = new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
      server.closeAllConnections()
      await hostBrowserStep('close fixture server', closed)
    }
  }, 15_000)

  return fixture
}
