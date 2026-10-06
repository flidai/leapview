import { expect } from 'bun:test'
import type { Page } from '@playwright/test'

export async function assertDataExplorerResponsiveDrawers(page: Page): Promise<void> {
  const closeFilters = page.getByRole('button', { name: 'Close filters', exact: true })
  if (await closeFilters.isVisible()) await closeFilters.click()

  await page.setViewportSize({ width: 640, height: 540 })
  const fields = page.locator('.semantic-fields')
  const fieldToggle = page.getByRole('button', { name: 'Model & fields', exact: true })
  expect(await fields.isVisible()).toBe(false)

  await fieldToggle.click()
  const fieldSearch = page.getByLabel('Search semantic fields', { exact: true })
  expect(await fieldSearch.isVisible()).toBe(true)
  await fieldSearch.fill('Status')
  await page.keyboard.press('Escape')
  expect(await fieldToggle.getAttribute('aria-expanded')).toBe('false')
  expect(await fields.isVisible()).toBe(false)

  await fieldToggle.click()
  await fieldToggle.press('Escape')
  expect(await fieldToggle.getAttribute('aria-expanded')).toBe('false')
  const openerFocused = await fieldToggle.evaluate((button) => {
    const root = button.getRootNode()
    return root instanceof ShadowRoot && root.activeElement === button
  })
  expect(openerFocused).toBe(true)

  await fieldToggle.click()
  await page.locator('.result-meta').click()
  expect(await fieldToggle.getAttribute('aria-expanded')).toBe('false')
  expect(await fields.isVisible()).toBe(false)

  const tableBounds = await page.locator('lv-data-explore-table').boundingBox()
  expect(tableBounds!.y).toBeLessThan(440)
  expect(tableBounds!.height).toBeGreaterThanOrEqual(100)

  await fieldToggle.click()
  await page.locator('.semantic-filter-rail').click()
  expect(await fieldToggle.getAttribute('aria-expanded')).toBe('false')
  expect(await page.getByRole('button', { name: 'Close filters', exact: true }).isVisible()).toBe(true)
  const filterDockBounds = await page.locator('.semantic-filter-dock').boundingBox()
  const mainBounds = await page.locator('.main').boundingBox()
  expect(filterDockBounds!.width).toBeGreaterThanOrEqual(200)
  expect(filterDockBounds!.x + filterDockBounds!.width).toBeLessThanOrEqual(mainBounds!.x + mainBounds!.width + 1)

  await fieldToggle.click()
  expect(await fields.isVisible()).toBe(true)
  expect(await page.getByRole('button', { name: 'Close filters', exact: true }).isVisible()).toBe(false)
  await fieldToggle.click()

  await page.setViewportSize({ width: 1280, height: 820 })
  expect(await fields.isVisible()).toBe(true)
  expect(await fieldToggle.isVisible()).toBe(false)

  await page.setViewportSize({ width: 1024, height: 720 })
  await page.evaluate(async () => {
    const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
    mergePatch({ agent: {} })
    const explorer = document.querySelector('lv-data-explorer') as any
    explorer.agentDrawerOpen = true
    await explorer.updateComplete
  })

  const chatLayout = await page.locator('lv-data-explorer').evaluate((element: any) => {
    const root = element.shadowRoot as ShadowRoot
    const width = (selector: string) => root.querySelector<HTMLElement>(selector)!.getBoundingClientRect().width
    const layout = root.querySelector<HTMLElement>('.semantic-layout')!
    return {
      host: element.getBoundingClientRect().width,
      route: width('.route'),
      main: width('.main'),
      chatOpen: width('lv-chat-drawer') > 0,
      fieldsToggleVisible: width('.semantic-fields-toggle') > 0,
      fieldsClosed: width('.semantic-fields') === 0,
      filterRail: getComputedStyle(layout).gridTemplateColumns.split(' ').at(-1),
      noOverflow: layout.scrollWidth <= layout.clientWidth + 1,
    }
  })
  expect(chatLayout.host).toBeGreaterThan(900)
  expect(chatLayout.route).toBeGreaterThan(900)
  expect(chatLayout.main).toBeLessThan(900)
  expect(chatLayout.chatOpen).toBe(true)
  expect(chatLayout.fieldsToggleVisible).toBe(true)
  expect(chatLayout.fieldsClosed).toBe(true)
  expect(chatLayout.filterRail).toBe('38px')
  expect(chatLayout.noOverflow).toBe(true)
}
