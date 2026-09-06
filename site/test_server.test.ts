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

test('site test server stops its executable and remains idempotent', async () => {
  const startupDeadline = Date.now() + 60_000
  const port = await availablePort()
  const site = await startSiteTestServer(port, startupDeadline)
  const pid = site.process.pid
  try {
    await waitForHealth(port, site.process, startupDeadline)
  } finally {
    await site.stop()
    await site.stop()
  }

  assert.equal(site.process.exitCode !== null, true)
  assert.throws(
    () => process.kill(pid, 0),
    (error: unknown) => (error as NodeJS.ErrnoException)?.code === 'ESRCH',
  )
}, 70_000)

async function availablePort(): Promise<number> {
  const listener = createServer()
  await new Promise<void>((resolve, reject) => {
    listener.once('error', reject)
    listener.listen(0, '127.0.0.1', resolve)
  })
  const address = listener.address()
  if (!address || typeof address === 'string') throw new Error('test server did not receive a port')
  await new Promise<void>((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()))
  return address.port
}

async function waitForHealth(port: number, siteProcess: Bun.Subprocess, deadline: number): Promise<void> {
  const url = `http://127.0.0.1:${port}/healthz`
  while (Date.now() < deadline) {
    if (siteProcess.exitCode !== null) throw new Error(`site exited before becoming ready with code ${siteProcess.exitCode}`)
    try {
      const response = await fetch(url)
      if (response.ok) return
    } catch {
      // The site is still starting.
    }
    await Bun.sleep(100)
  }
  throw new Error(`site did not become ready at ${url} before startup deadline`)
}
