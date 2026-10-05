import { expect, test } from '@playwright/test'
import type { AxeResults } from 'axe-core'
import { collectBrowserIssues, discoverExamples, openExample, scanAccessibility, type ExampleLink, type ReviewTheme } from './helpers'

type RouteReport = ExampleLink & {
  theme: ReviewTheme
  violations: AxeResults['violations']
  incomplete: AxeResults['incomplete']
  error?: string
  browserIssues: ReturnType<typeof collectBrowserIssues>
}

test('default examples accessibility sweep', async ({ page, context }, testInfo) => {
  test.setTimeout(600_000)
  const theme = testInfo.project.use.colorScheme as ReviewTheme
  const discoveryIssues = collectBrowserIssues(page)
  const examples = await discoverExamples(page, theme)
  expect(examples.length, 'Examples navigation must expose a nonempty catalog').toBeGreaterThan(0)
  const reports: RouteReport[] = []

  try {
    for (const example of examples) {
      await test.step(`${theme}: ${example.route}`, async () => {
        const reviewPage = await context.newPage()
        const report: RouteReport = { ...example, theme, violations: [], incomplete: [], browserIssues: collectBrowserIssues(reviewPage) }
        let deadline: ReturnType<typeof setTimeout> | undefined
        try {
          const result = await Promise.race([
            (async () => {
              await openExample(reviewPage, example.route, { theme })
              return scanAccessibility(reviewPage)
            })(),
            new Promise<never>((_, reject) => {
              deadline = setTimeout(() => reject(new Error('Example render or axe scan exceeded 30 seconds')), 30_000)
            }),
          ])
          report.violations = result.violations
          report.incomplete = result.incomplete
        } catch (error) {
          report.error = error instanceof Error ? error.message : String(error)
        } finally {
          clearTimeout(deadline)
          // Closing the page also cancels any render/axe work left by the deadline.
          await reviewPage.close()
          reports.push(report)
          await testInfo.attach(`${theme}-${example.route.replaceAll('/', '-')}.json`, {
            body: JSON.stringify(report, null, 2), contentType: 'application/json',
          })
        }
      })
    }
  } finally {
    await testInfo.attach(`accessibility-${theme}-summary.json`, {
      body: JSON.stringify({ theme, discovered: examples.length, completed: reports.length, discoveryIssues, reports }, null, 2),
      contentType: 'application/json',
    })
  }

  const failures = reports.flatMap(report => {
    const reasons = [
      ...(report.error ? [`Scan failed: ${report.error}`] : []),
      ...report.violations.map(rule => `${rule.id}: ${rule.help} (${rule.nodes.length} nodes)`),
      ...report.browserIssues.errors,
      ...report.browserIssues.unexpectedRequests.map(url => `Unexpected request: ${url}`),
    ]
    return reasons.length ? [{ route: report.route, theme, reasons }] : []
  })
  expect(discoveryIssues, 'Catalog discovery must load without browser errors or external/backend requests').toEqual({ errors: [], unexpectedRequests: [] })
  expect(reports.length, 'Every discovered route must produce a report').toBe(examples.length)
  expect(failures, `Accessibility findings for ${theme}; inspect attached route reports, including incomplete results`).toEqual([])
})
