import { test } from 'bun:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:net'

import { startSiteTestServer } from './test_server'

test('site test server rejects an expired startup deadline before spawning', async () => {
  await assert.rejects(
    () => startSiteTestServer(0, Date.now() - 1),
    /startup deadline expired before build/,
  )
})

test('site test server reports bind failures from its own child', async () => {
  const listener = createServer()
  await new Promise<void>((resolve) => listener.listen(0, '127.0.0.1', resolve))
  const address = listener.address()
  assert.ok(address && typeof address !== 'string')
  try {
    await assert.rejects(async () => {
      const site = await startSiteTestServer(address.port)
      await site.stop()
    }, /address already in use/)
  } finally {
    await new Promise<void>((resolve) => listener.close(() => resolve()))
  }
}, 70_000)

test('concurrent site servers return healthy distinct ephemeral URLs and stop idempotently', async () => {
  const sites = await Promise.allSettled([startSiteTestServer(), startSiteTestServer()])
  try {
    const servers = sites.map((result) => {
      if (result.status === 'rejected') throw result.reason
      return result.value
    })
    assert.notEqual(servers[0].baseURL, servers[1].baseURL)
    for (const site of servers) {
      assert.match(site.baseURL, /^http:\/\/127\.0\.0\.1:[1-9]\d*$/)
      assert.equal((await fetch(`${site.baseURL}/healthz`)).ok, true)
    }
  } finally {
    for (const result of sites) {
      if (result.status !== 'fulfilled') continue
      const site = result.value
      await Promise.all([site.stop(), site.stop()])
      assert.ok(site.process.exitCode !== null || site.process.signalCode !== null)
      assert.throws(() => process.kill(site.process.pid, 0),
        (error: unknown) => (error as NodeJS.ErrnoException)?.code === 'ESRCH')
    }
  }
}, 70_000)

test('site test server includes configuration errors in startup diagnostics', async () => {
  const previous = process.env.LEAPVIEW_SITE_BASE_URL
  process.env.LEAPVIEW_SITE_BASE_URL = 'invalid-origin'
  try {
    await assert.rejects(() => startSiteTestServer(), /site base URL must be an HTTP\(S\) origin/)
  } finally {
    if (previous === undefined) delete process.env.LEAPVIEW_SITE_BASE_URL
    else process.env.LEAPVIEW_SITE_BASE_URL = previous
  }
}, 70_000)
