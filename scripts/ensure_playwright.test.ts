import { expect, test } from 'bun:test'
import { spawnSync } from 'node:child_process'

test('trusted CI provisioning bypasses repeated Playwright discovery', () => {
  const result = spawnSync('node', ['scripts/ensure_playwright.mjs'], {
    env: {
      ...process.env,
      LEAPVIEW_PLAYWRIGHT_READY: '1',
      PLAYWRIGHT_BROWSERS_PATH: '/definitely-not-a-playwright-cache',
    },
    timeout: 2_000,
  })

  expect(result.signal).toBeNull()
  expect(result.status).toBe(0)
})
