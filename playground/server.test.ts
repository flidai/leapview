import { expect, test } from 'bun:test'
import { playgroundResponse } from './server'

test('the public response function remains a direct Bun fetch handler', async () => {
  const server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: playgroundResponse })
  try {
    const page = await fetch(server.url)
    expect(page.status).toBe(200)
    expect(await page.text()).toContain('<playground-app>')
    // Bun supplies its Server as the callback's second argument. It must never
    // be interpreted as a build context, even when no assets have been built.
    const missing = await fetch(new URL('/assets/missing-response-test.js', server.url))
    expect(missing.status).toBe(404)
    expect(await missing.text()).toBe('Not found')
    expect(await fetch(new URL('/__playground/events', server.url)).then(response => response.status)).toBe(204)
  } finally { await server.stop(true) }
})
