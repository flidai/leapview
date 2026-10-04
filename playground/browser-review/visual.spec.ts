import { expect, test } from '@playwright/test'
import { collectBrowserIssues, openExample, type ReviewTheme } from './helpers'

const examples = [
  { name: 'bar-desktop', route: 'charts/bar', mobile: false },
  { name: 'windowed-table-desktop', route: 'tables/windowed', mobile: false },
  { name: 'asset-lineage-desktop', route: 'graphs/asset-lineage', mobile: false },
  { name: 'linked-dashboard-mobile', route: 'recipes/linked-visuals', mobile: true },
]

for (const example of examples) {
  test(example.name, async ({ page }, testInfo) => {
    const issues = collectBrowserIssues(page)
    const theme = testInfo.project.use.colorScheme as ReviewTheme
    if (example.mobile) await page.setViewportSize({ width: 390, height: 844 })
    await openExample(page, example.route, { theme, preview: true, width: example.mobile ? '360' : '1200' })
    // A tall example is clipped by the shell's scroll container on mobile; capture
    // the actual visible screen instead of an element image with blank offscreen rows.
    if (example.mobile) await expect(page).toHaveScreenshot(`${example.name}.png`)
    else await expect(page.locator('playground-app .workspace > main > .viewport')).toHaveScreenshot(`${example.name}.png`)
    expect(issues.errors, `${example.route} browser errors`).toEqual([])
    expect(issues.unexpectedRequests, `${example.route} backend/external requests`).toEqual([])
  })
}
