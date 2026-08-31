import { defineConfig } from '@playwright/test'
import { join } from 'node:path'

const root = process.cwd()
const artifactRoot = process.env.LEAPVIEW_VISUAL_ARTIFACT_DIR
  ?? join(root, '.tmp', 'qa-ui-framework', 'visual-artifacts')

export default defineConfig({
  testDir: '.',
  testMatch: 'visual_regression.spec.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 90_000,
  outputDir: join(artifactRoot, 'test-results'),
  snapshotPathTemplate: '{testDir}/visual-regression-baselines/{arg}{ext}',
  reporter: [
    ['list'],
    ['html', { outputFolder: join(artifactRoot, 'html-report'), open: 'never' }],
  ],
  use: {
    baseURL: process.env.LEAPVIEW_BASE_URL ?? 'http://localhost:8195',
    browserName: 'chromium',
    colorScheme: 'light',
    deviceScaleFactor: 1,
    locale: 'en-US',
    reducedMotion: 'reduce',
    timezoneId: 'UTC',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
})
