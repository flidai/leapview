import { expect, test } from 'bun:test'
import { spawnSync } from 'node:child_process'

test('trusted CI provisioning bypasses repeated Playwright discovery', () => {
  const result = spawnSync('node', ['scripts/ensure_playwright.mjs'], {
    env: {
      ...process.env,
      LEAPVIEW_PLAYWRIGHT_READY: '1',
      LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION: '',
      PLAYWRIGHT_BROWSERS_PATH: '/definitely-not-a-playwright-cache',
    },
    timeout: 2_000,
  })

  expect(result.signal).toBeNull()
  expect(result.status).toBe(0)
})

test('a missing locked browser fails without attempting a mutable download', () => {
  const result = spawnSync('node', ['scripts/ensure_playwright.mjs'], {
    env: {
      ...process.env,
      LEAPVIEW_PLAYWRIGHT_READY: '1',
      LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION: '1.61.1',
      PLAYWRIGHT_BROWSERS_PATH: '/nix/store/missing-browser-fixture',
    }, encoding: 'utf8', timeout: 5_000,
  })
  expect(result.signal).toBeNull()
  expect(result.status).not.toBe(0)
  expect(result.stderr).toContain('Locked Chromium executable is missing')
  expect(result.stdout).not.toContain('Downloading')
})

test('the trusted-ready flag cannot bypass a locked npm/browser version mismatch', () => {
  const result = spawnSync('node', ['scripts/ensure_playwright.mjs'], {
    env: {
      ...process.env,
      LEAPVIEW_PLAYWRIGHT_READY: '1',
      LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION: '0.0.0',
    }, encoding: 'utf8', timeout: 5_000,
  })
  expect(result.signal).toBeNull()
  expect(result.status).not.toBe(0)
  expect(result.stderr).toContain('does not match npm')
})
