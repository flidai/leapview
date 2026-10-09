import { afterAll, beforeAll } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testVisualizationEnvelopes } from '../dashboard-page-test-fixtures'

export function hostBrowserFixture() {
  const fixture = { browser: undefined as unknown as Browser, baseURL: '' }
  let server: Server
  const projectRoot = process.cwd()
  const fixtureRoot = join(projectRoot, '.tmp/visualization-host-test')

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
    await fixture.browser?.close()
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  }, 15_000)

  return fixture
}
