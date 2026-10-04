import { defineConfig } from '@playwright/test'
import { resolve } from 'node:path'
import { repositoryRoot, reviewBaseURL, reviewDirectory, reviewOutput } from './settings'

// Explicitly selected from the CLI; independent of the product QA config and CI.
export default defineConfig({
  testDir: reviewDirectory,
  testMatch: ['visual.spec.ts', 'accessibility.spec.ts'],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: Boolean(process.env.CI),
  timeout: 90_000,
  outputDir: resolve(reviewOutput, 'test-results'),
  snapshotPathTemplate: '{testDir}/baselines/{platform}-chromium/{projectName}/{arg}{ext}',
  reporter: [
    ['list'],
    ['html', { outputFolder: resolve(reviewOutput, 'html-report'), open: 'never' }],
    ['json', { outputFile: resolve(reviewOutput, 'results.json') }],
  ],
  expect: {
    timeout: 15_000,
    toHaveScreenshot: {
      animations: 'disabled',
      caret: 'hide',
      scale: 'css',
      threshold: 0.2,
      maxDiffPixels: 0,
    },
  },
  use: {
    baseURL: reviewBaseURL,
    browserName: 'chromium',
    headless: true,
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    viewport: { width: 1440, height: 1000 },
    deviceScaleFactor: 1,
    locale: 'en-US',
    timezoneId: 'UTC',
    contextOptions: { reducedMotion: 'reduce' },
    serviceWorkers: 'block',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'visual-light', testMatch: 'visual.spec.ts', use: { colorScheme: 'light' } },
    { name: 'visual-dark', testMatch: 'visual.spec.ts', use: { colorScheme: 'dark' } },
    { name: 'accessibility-light', testMatch: 'accessibility.spec.ts', use: { colorScheme: 'light' } },
    { name: 'accessibility-dark', testMatch: 'accessibility.spec.ts', use: { colorScheme: 'dark' } },
  ],
  webServer: {
    command: 'bun playground/browser-review/server.ts',
    cwd: repositoryRoot,
    url: reviewBaseURL,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
})
