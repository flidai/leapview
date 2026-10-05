import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { reviewBaseURL } from './settings'

export type ReviewTheme = 'light' | 'dark'
export type ExampleLink = { route: string; label: string }
export type ExampleOptions = {
  theme: ReviewTheme
  preview?: boolean
  width?: 'responsive' | '360' | '768' | '1200'
  height?: '260' | '420' | '640'
}

/** Reload the document so fixture, scroll, popover and selection state cannot leak. */
export async function openExample(page: Page, route: string, options: ExampleOptions) {
  const parameters = new URLSearchParams({ theme: options.theme, width: options.width || 'responsive', height: options.height || '420' })
  if (options.preview) parameters.set('preview', '1')
  // A hash-only goto is same-document navigation and can leave the old fixture mounted.
  await page.goto('about:blank')
  await page.goto(`/?${parameters}#${route}`)
  // The lazy-load status is visible before the example module is ready.
  const example = page.locator('playground-app .workspace > main > .viewport > :not([role="status"])')
  await example.waitFor({ state: 'visible' })
  await example.evaluate(async (element) => {
    await (element as HTMLElement & { updateComplete?: Promise<unknown> }).updateComplete
  })
  await page.evaluate(async () => { await document.fonts.ready })
  await page.locator('lv-visualization-host').evaluateAll(async (elements) => {
    for (const element of elements) {
      await (element as HTMLElement & { ensureMounted(): Promise<void> }).ensureMounted()
    }
  })
  if (route.startsWith('graphs/')) {
    await page.locator('.react-flow__node').first().waitFor({ state: 'visible' })
  }
  if (route === 'content/code-editor') {
    await page.locator('lv-code-editor .monaco-editor').waitFor({ state: 'visible' })
  }
  if (route === 'content/code-block') {
    await page.locator('lv-code-block').evaluate(async (element) => {
      await (element as HTMLElement & { updateComplete: Promise<unknown> }).updateComplete
    })
  }
  // Give font/layout observers two frames to apply after their async render work.
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

/** All categories remain in the DOM when collapsed; discover links, not a copied list. */
export async function discoverExamples(page: Page, theme: ReviewTheme): Promise<ExampleLink[]> {
  await openExample(page, 'charts/bar', { theme })
  return page.getByRole('navigation', { name: 'Examples' }).locator('a[href^="#"]').evaluateAll((links) => {
    const unique = new Map<string, ExampleLink>()
    for (const link of links) {
      const route = link.getAttribute('href')?.slice(1)
      if (route && /^[a-z0-9-]+\/[a-z0-9-]+$/.test(route)) {
        unique.set(route, { route, label: link.textContent?.trim() || route })
      }
    }
    return [...unique.values()]
  })
}

/** Includes the shell and the default example, traversing open component shadows. */
export function scanAccessibility(page: Page) {
  return new AxeBuilder({ page })
    .include('playground-app')
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze()
}

/** Preserve diagnostics for assertions/attachments in the review specs. */
export function collectBrowserIssues(page: Page) {
  const errors: string[] = []
  const unexpectedRequests: string[] = []
  const localOrigin = new URL(reviewBaseURL).origin
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => {
    if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`)
  })
  page.on('requestfailed', request => {
    const reason = request.failure()?.errorText
    // Navigation/teardown cancels in-flight local assets; connection failures do not.
    if (reason && reason !== 'net::ERR_ABORTED') errors.push(`${reason} ${request.url()}`)
  })
  page.on('request', request => {
    const url = new URL(request.url())
    if (url.protocol === 'blob:' || url.protocol === 'data:') return
    const asset = ['/', '/index.html', '/__playground/events'].includes(url.pathname) || url.pathname.startsWith('/assets/') || url.pathname.startsWith('/static/')
    if (url.origin !== localOrigin || !asset || !['GET', 'HEAD'].includes(request.method())) unexpectedRequests.push(request.url())
  })
  return { errors, unexpectedRequests }
}
