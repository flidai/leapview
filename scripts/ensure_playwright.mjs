import { constants } from 'node:fs'
import { access } from 'node:fs/promises'
import { spawnSync } from 'node:child_process'
import { createRequire } from 'node:module'

const lockedVersion = process.env.LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION
if (!lockedVersion && process.env.LEAPVIEW_PLAYWRIGHT_READY === '1') process.exit(0)

if (lockedVersion) {
  const require = createRequire(import.meta.url)
  const version = require('playwright-core/package.json').version
  if (version !== lockedVersion) {
    throw new Error(`Locked Playwright ${lockedVersion} does not match npm ${version}; update the Nix input alongside bun.lock`)
  }
  if (!process.env.PLAYWRIGHT_BROWSERS_PATH?.startsWith('/nix/store/')) {
    throw new Error('Locked Playwright browsers must come from the Nix store')
  }
}

const { chromium } = await import('@playwright/test')

const executable = chromium.executablePath()

try {
  await access(executable, constants.X_OK)
  process.exit(0)
} catch {
  if (lockedVersion) {
    throw new Error(`Locked Chromium executable is missing: ${executable}; rebuild the Nix development shell`)
  }
  // Browser provisioning is intentionally lazy for local, standalone test runs.
}

const install = spawnSync('bun', ['x', 'playwright', 'install', 'chromium'], {
  stdio: 'inherit',
  timeout: 300_000,
})
if (install.error) throw install.error
if (install.status !== 0) {
  throw new Error(`Playwright Chromium installation failed with status ${install.status ?? 'unknown'}`)
}

await access(executable, constants.X_OK)
