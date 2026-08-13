import { constants } from 'node:fs'
import { access } from 'node:fs/promises'
import { spawnSync } from 'node:child_process'

if (process.env.LEAPVIEW_PLAYWRIGHT_READY === '1') process.exit(0)

const { chromium } = await import('@playwright/test')

const executable = chromium.executablePath()

try {
  await access(executable, constants.X_OK)
  process.exit(0)
} catch {
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
