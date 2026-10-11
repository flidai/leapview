import { afterAll, afterEach, beforeAll, beforeEach, expect, test } from 'bun:test'
import { chromium, expect as browserExpect, type Browser, type Page } from '@playwright/test'
import { startTestPlayground } from './test-server'

let server: Awaited<ReturnType<typeof startTestPlayground>>
let browser: Browser
let page: Page
let errors: string[]

beforeAll(async () => { server = await startTestPlayground(); browser = await chromium.launch() }, 60000)
afterAll(async () => { await browser?.close(); await server?.stop(true) })
beforeEach(async () => {
  errors = []
  page = await browser.newPage({ viewport: { width: 1440, height: 1100 }, reducedMotion: 'reduce' })
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => { if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`) })
  await page.goto(`${server.url}?theme=light#recipes/dashboard-contract`)
  await browserExpect(page.locator('playground-dashboard-contract lv-code-editor .monaco-editor')).toBeVisible()
})
afterEach(async () => { await page?.close(); expect(errors).toEqual([]) })

async function editorColors() {
  return page.locator('playground-dashboard-contract lv-code-editor').evaluate(element => {
    const root = element.shadowRoot!
    const probe = document.createElement('span')
    probe.style.position = 'absolute'
    probe.style.visibility = 'hidden'
    root.append(probe)
    try {
      probe.style.backgroundColor = 'var(--lv-bg-panel)'
      probe.style.color = 'var(--lv-fg-muted)'
      const styles = getComputedStyle(probe)
      return {
        panel: styles.backgroundColor,
        muted: styles.color,
        surface: getComputedStyle(root.querySelector('.monaco-editor')!).backgroundColor,
        gutter: getComputedStyle(root.querySelector('.margin')!).backgroundColor,
        lineNumber: getComputedStyle(root.querySelector('.line-numbers:not(.active-line-number)')!).color,
      }
    } finally { probe.remove() }
  })
}

function contrast(foreground: string, background: string) {
  const luminance = (color: string) => {
    const channels = color.match(/[\d.]+/g)!.slice(0, 3).map(value => Number(value) / 255)
    const linear = channels.map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
    return linear[0]! * 0.2126 + linear[1]! * 0.7152 + linear[2]! * 0.0722
  }
  const a = luminance(foreground)
  const b = luminance(background)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

test('production editor surface and gutter follow the actual Playground light/dark theme', async () => {
  const backgrounds: string[] = []
  for (let step = 0; step < 3; step++) {
    const expected = (await editorColors()).panel
    await browserExpect.poll(async () => {
      const colors = await editorColors()
      return { surface: colors.surface, gutter: colors.gutter }
    }).toEqual({ surface: expected, gutter: expected })
    backgrounds.push(expected)
    if (step === 0) await page.getByRole('button', { name: 'Switch to dark mode', exact: true }).click()
    if (step === 1) await page.getByRole('button', { name: 'Switch to light mode', exact: true }).click()
  }
  expect(backgrounds[0]).not.toBe(backgrounds[1])
  expect(backgrounds[2]).toBe(backgrounds[0])
})

test('inactive editor line numbers use the production muted color with readable contrast in both themes', async () => {
  for (let step = 0; step < 2; step++) {
    await browserExpect.poll(async () => {
      const colors = await editorColors()
      return colors.lineNumber === colors.muted
    }).toBe(true)
    const colors = await editorColors()
    expect(contrast(colors.lineNumber, colors.gutter)).toBeGreaterThanOrEqual(4.5)
    if (step === 0) await page.getByRole('button', { name: 'Switch to dark mode', exact: true }).click()
  }
})
