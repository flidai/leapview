import { expect } from 'bun:test'
import type { Page } from '@playwright/test'
import { governedBarPreviewEnvelope } from './dashboard-builder-test-fixtures'

export async function verifyBuilderZoomActionTargets(page: Page, baseURL: string): Promise<void> {
  const previewEnvelope = governedBarPreviewEnvelope('sha256:builder-zoom-targets')
  await page.goto(baseURL)
  await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
  await page.locator('lv-dashboard-builder').evaluate(async (element: any, envelope: any) => {
    const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
    mergePatch({ builderVisuals: { 'sales-chart': envelope } })
    await element.updateComplete
  }, previewEnvelope)
  await page.waitForFunction(() => Boolean(document.querySelector('lv-dashboard-builder')?.shadowRoot?.querySelector('.visual-preview lv-visualization-host')?.shadowRoot?.querySelector('.visual-options')))

  const result = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
    document.documentElement.style.setProperty('--control-xsmall-size', '20px')
    document.documentElement.style.setProperty('--lv-button-height-xs', '20px')
    document.documentElement.style.setProperty('--control-small-size', '20px')
    document.documentElement.style.setProperty('--lv-button-height-sm', '20px')
    const root = element.shadowRoot
    const canvas = root.querySelector('.canvas') as HTMLElement
    const host = root.querySelector('.visual-preview lv-visualization-host') as any
    const measure = async (scale: number, reportInverseScale?: string) => {
      element.canvasZoom = scale
      element.syncCanvasViewport(element.selectedPage(element.builder))
      if (reportInverseScale === undefined) host.style.removeProperty('--report-canvas-inverse-scale')
      else host.style.setProperty('--report-canvas-inverse-scale', reportInverseScale)
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const options = host.shadowRoot.querySelector('.visual-options') as HTMLDetailsElement
      options.open = true
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const rect = (selector: string) => {
        const bounds = (host.shadowRoot.querySelector(selector) as HTMLElement).getBoundingClientRect()
        return { x: bounds.x, y: bounds.y, width: bounds.width, height: bounds.height }
      }
      return {
        scale: canvas.style.getPropertyValue('--builder-canvas-scale'),
        actions: [rect('[data-visualization-expand]'), rect('.visual-options summary')],
        menu: Array.from(host.shadowRoot.querySelectorAll('.visual-options .menu button')).map((button) => {
          const bounds = (button as HTMLElement).getBoundingClientRect()
          return { width: bounds.width, height: bounds.height }
        }),
      }
    }
    const minimum = await measure(0.25)
    const normal = await measure(1)
    const report = await measure(1, '2')
    return { minimum, normal, report }
  })

  expect(result.minimum.scale).toBe('0.25')
  expect(result.normal.scale).toBe('1')
  for (const state of [result.minimum, result.normal]) {
    expect(state.actions).toHaveLength(2)
    for (const target of state.actions) {
      expect(target.width).toBeGreaterThanOrEqual(24)
      expect(target.height).toBeGreaterThanOrEqual(24)
    }
    expect(state.menu.length).toBeGreaterThan(0)
    for (const target of state.menu) {
      expect(target.width).toBeGreaterThanOrEqual(24)
      expect(target.height).toBeGreaterThanOrEqual(24)
    }
  }
  const [firstAction, secondAction] = result.minimum.actions
  expect(
    firstAction.x + firstAction.width <= secondAction.x ||
      secondAction.x + secondAction.width <= firstAction.x ||
      firstAction.y + firstAction.height <= secondAction.y ||
      secondAction.y + secondAction.height <= firstAction.y,
  ).toBe(true)
  expect(result.report.actions.every(({ width, height }: { width: number; height: number }) => width >= 48 && height >= 48)).toBe(true)
}
