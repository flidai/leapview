import { constants } from 'node:fs'
import { access } from 'node:fs/promises'
import { spawnSync } from 'node:child_process'
import { chromium } from '@playwright/test'

const executable = chromium.executablePath()

try {
  await access(executable, constants.X_OK)
  process.exit(0)
} catch {
  // Browser provisioning is intentionally lazy for local, standalone test runs.
}

const install = spawnSync(process.execPath, ['x', 'playwright', 'install', 'chromium'], {
  stdio: 'inherit',
  timeout: 300_000,
})
if (install.error) throw install.error
if (install.status !== 0) {
  throw new Error(`Playwright Chromium installation failed with status ${install.status ?? 'unknown'}`)
}

await access(executable, constants.X_OK)
