import { afterAll, beforeAll, expect, test } from 'bun:test'
import { chromium, type Browser } from '@playwright/test'
import { startSiteTestServer, type SiteTestServer } from './test_server'

const sitePort = 20000 + (process.pid % 10000)
const baseURL = `http://127.0.0.1:${sitePort}`
let browser: Browser
let siteServer: SiteTestServer | undefined
const siteReadyTimeout = 60_000
const lineExampleIDs = ['revenue_line', 'revenue_line_axis_policies', 'revenue_line_status', 'revenue_line_running', 'revenue_line_step', 'revenue_line_context']

beforeAll(async () => {
  const startupDeadline = Date.now() + siteReadyTimeout
  try {
    siteServer = await startSiteTestServer(sitePort, startupDeadline)
    await waitForSite(siteServer.process, startupDeadline)
    browser = await chromium.launch()
  } catch (error) {
    await siteServer?.stop()
    siteServer = undefined
    throw error
  }
}, siteReadyTimeout + 10_000)

afterAll(async () => {
  try {
    await browser?.close()
  } finally {
    await siteServer?.stop()
  }
})

test('homepage presents the new sections inside the shared site shell', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    expect(await page.locator('.site-header').count()).toBe(1)
    expect(await page.locator('.site-footer').count()).toBe(1)
    expect((await page.locator('.site-footer-bottom').textContent())?.trim()).toBe('A project by the Flid AI team')
    expect(await page.locator('.site-footer-bottom').getByRole('link', { name: 'Flid AI' }).getAttribute('href')).toBe('https://flid.ai/')
    expect(await page.locator('.landing-footer, .topbar').count()).toBe(0)
    expect(await page.getByRole('heading', { level: 1, name: 'From data to the full picture.' }).isVisible()).toBe(true)
    const sectionOrder = ['main-content', 'mission', 'connections', 'build', 'layers', 'enterprise', 'openness', 'get-involved']
    expect(await page.locator('.site-home > section').evaluateAll((sections) => sections.map((section) => section.id))).toEqual(sectionOrder)
    for (const id of sectionOrder) {
      expect(await page.locator(`#${id}`).count()).toBe(1)
    }
    expect(await page.locator('.mission-kicker, .story-kicker, .project-kicker, .architecture-kicker, .enterprise-kicker, .openness-kicker, .involved-kicker').count()).toBe(0)
    expect(await page.getByRole('heading', { level: 2, name: 'Analytics as code.' }).count()).toBe(1)
    expect(await page.getByRole('heading', { level: 2, name: 'Every answer has a foundation.' }).count()).toBe(1)
    expect(await page.locator('.site-interfaces-section, .site-stack-section, .site-desktop-section').count()).toBe(0)
    const screenshot = page.locator('#product-image')
    await page.waitForFunction(() => {
      const image = document.querySelector<HTMLImageElement>('#product-image')
      return image?.complete && image.naturalWidth === 1440
    })
    expect(await page.locator('.orbit-node').count()).toBe(17)
    await page.locator('#connections').scrollIntoViewIfNeeded()
    await page.waitForFunction(() => document.querySelector('#orbit-stage')?.classList.contains('is-in-view'))
    expect(await page.locator('.orbit-rotor').first().evaluate((element) => getComputedStyle(element).animationPlayState)).toBe('running')
    await page.locator('[data-project-file="connection"]').click()
    await page.waitForFunction(() => document.querySelector('#project-code')?.textContent?.includes('kind:'))
    expect(await page.locator('#project-tab-connection').getAttribute('aria-selected')).toBe('true')
    expect(await page.locator('#project-file-panel').getAttribute('aria-labelledby')).toBe('project-tab-connection')
    expect(await page.locator('#project-tab-connection .project-tab-filename').textContent()).toBe('olist.yaml')
    expect(await page.locator('.project-code-text').first().textContent()).toBe('apiVersion: leapview.dev/v1')
    expect(await page.locator('.project-editor-footer, .project-footnote').count()).toBe(0)
    expect(await page.locator('#project-file-path').textContent()).toBe('sales-project / connections / olist.yaml')
    expect(await page.getByRole('link', { name: /Join the community/ }).getAttribute('href')).toBe('https://discord.gg/pcfV4zAeRV')
  } finally {
    await page.close()
  }
})

test('homepage hero fits the first screen and mission rewrites without shifting the post', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    const desktop = await page.evaluate(() => ({
      heroBottom: document.querySelector('.hero')!.getBoundingClientRect().bottom,
      titleTop: document.querySelector('.hero h1')!.getBoundingClientRect().top,
      screenshotTop: document.querySelector('.product-frame')!.getBoundingClientRect().top,
      missionWidth: document.querySelector('.mission-inner')!.getBoundingClientRect().width,
      missionFont: getComputedStyle(document.querySelector('.mission-copy')!).fontFamily,
      siteFont: getComputedStyle(document.querySelector('.site-header')!).fontFamily,
    }))
    expect(desktop.heroBottom).toBeGreaterThan(1000)
    expect(desktop.heroBottom).toBeLessThan(1110)
    expect(desktop.titleTop).toBeGreaterThan(175)
    expect(desktop.titleTop).toBeLessThan(215)
    expect(desktop.screenshotTop).toBeGreaterThan(390)
    expect(desktop.screenshotTop).toBeLessThan(450)
    expect(desktop.missionWidth).toBeLessThanOrEqual(680)
    expect(desktop.missionFont).toBe(desktop.siteFont)

    await page.locator('#mission').scrollIntoViewIfNeeded()
    await page.waitForFunction(() => document.querySelector('.mission-subject')?.textContent === 'data', undefined, { timeout: 8000 })
    const cursorOffset = await page.evaluate(() => {
      const subject = document.querySelector('.mission-subject')!.getBoundingClientRect()
      const cursor = document.querySelector('.mission-cursor')!.getBoundingClientRect()
      return cursor.left - subject.right
    })
    expect(cursorOffset).toBeGreaterThanOrEqual(0)
    expect(cursorOffset).toBeLessThan(8)
    expect(await page.locator('#mission-title').getAttribute('aria-label')).toBe('Your business intelligence should belong to your business.')
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.waitForFunction(() => document.querySelector('.mission-subject')?.textContent === 'business intelligence')

    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(baseURL)
    const mobile = await page.evaluate(() => ({
      heroBottom: document.querySelector('.hero')!.getBoundingClientRect().bottom,
      titleTop: document.querySelector('.hero h1')!.getBoundingClientRect().top,
      screenshotTop: document.querySelector('.product-frame')!.getBoundingClientRect().top,
    }))
    expect(mobile.heroBottom).toBeLessThanOrEqual(900)
    expect(mobile.titleTop).toBeGreaterThan(120)
    expect(mobile.titleTop).toBeLessThan(165)
    expect(mobile.screenshotTop).toBeGreaterThan(480)
    expect(mobile.screenshotTop).toBeLessThan(530)
  } finally {
    await page.close()
  }
}, 10_000)

test('homepage hero ends at the screenshot on wide screens', async () => {
  const page = await browser.newPage({ viewport: { width: 1920, height: 1080 } })
  try {
    await page.goto(baseURL)
    for (const [width, height] of [[1920, 1080], [2560, 1440]]) {
      await page.setViewportSize({ width, height })
      const wide = await page.evaluate(() => {
        const hero = document.querySelector('.hero')!.getBoundingClientRect()
        const copy = document.querySelector('.hero-copy')!.getBoundingClientRect()
        const screenshot = document.querySelector('.product-frame')!.getBoundingClientRect()
        return { heroBottom: hero.bottom, screenshotBottom: screenshot.bottom, screenshotLeft: screenshot.left, screenshotRight: screenshot.right, copyLeft: copy.left }
      })
      expect(wide.heroBottom - wide.screenshotBottom).toBeLessThan(3)
      expect(wide.heroBottom).toBeLessThan(1100)
      expect(wide.screenshotRight).toBeLessThan(width - 20)
      expect(wide.screenshotLeft - wide.copyLeft).toBeGreaterThan(400)
      expect(wide.screenshotLeft - wide.copyLeft).toBeLessThan(430)
    }
  } finally {
    await page.close()
  }
})

test('homepage preview stays large and horizontally explorable on small screens', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    for (const width of [390, 768, 1023]) {
      await page.setViewportSize({ width, height: 900 })
      const layout = await page.evaluate(() => {
        const hero = document.querySelector('.hero')!.getBoundingClientRect()
        const frame = document.querySelector('.product-frame')!.getBoundingClientRect()
        const scroller = document.querySelector<HTMLElement>('#image-scroller')!
        return {
          heroBottom: hero.bottom,
          frameBottom: frame.bottom,
          frameRight: frame.right,
          scrollerWidth: scroller.clientWidth,
          scrollWidth: scroller.scrollWidth,
          pageWidth: document.documentElement.scrollWidth,
        }
      })
      expect(Math.abs(layout.heroBottom - layout.frameBottom)).toBeLessThan(3)
      expect(layout.frameRight).toBeGreaterThan(width + 100)
      expect(layout.scrollWidth).toBeGreaterThan(layout.scrollerWidth)
      expect(layout.pageWidth).toBe(width)
    }
    const boundary: { left: number; bottom: number }[] = []
    for (const width of [1100, 1101]) {
      await page.setViewportSize({ width, height: 900 })
      boundary.push(await page.locator('.product-frame').evaluate((frame) => {
        const rect = frame.getBoundingClientRect()
        return { left: rect.left, bottom: rect.bottom }
      }))
    }
    expect(Math.abs(boundary[1].left - boundary[0].left)).toBeLessThan(10)
    expect(Math.abs(boundary[1].bottom - boundary[0].bottom)).toBeLessThan(10)
  } finally {
    await page.close()
  }
})

test('homepage flow field draws in and respects reduced motion', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => Boolean(customElements.get('lv-site-flow-background')))
    const flow = page.locator('lv-site-flow-background.site-flow-field[draw-in]')
    await page.waitForFunction(() => {
      const canvas = document.querySelector('lv-site-flow-background')?.shadowRoot?.querySelector('canvas') as HTMLCanvasElement | null
      return Boolean(canvas && canvas.width > 0)
    })
    const first = await flow.evaluate((host) => (host.shadowRoot?.querySelector('canvas') as HTMLCanvasElement).toDataURL())
    await page.waitForTimeout(120)
    const second = await flow.evaluate((host) => (host.shadowRoot?.querySelector('canvas') as HTMLCanvasElement).toDataURL())
    expect(second).not.toBe(first)
    await page.emulateMedia({ reducedMotion: 'reduce' })
    expect(await flow.evaluate((host) => host.getBoundingClientRect().height)).toBeLessThanOrEqual(992)
  } finally {
    await page.close()
  }
})

test('homepage flow field remains bounded on ultra-wide screens', async () => {
  const page = await browser.newPage({ viewport: { width: 2560, height: 1000 } })
  try {
    await page.goto(baseURL)
    const geometry = await page.locator('lv-site-flow-background').evaluate((host) => {
      const rect = host.getBoundingClientRect()
      return { width: rect.width, left: rect.left, height: rect.height }
    })
    expect(geometry.width).toBeLessThanOrEqual(1920)
    expect(Math.abs(geometry.left - (2560 - geometry.width) / 2)).toBeLessThan(2)
    expect(geometry.height).toBeLessThanOrEqual(992)
  } finally {
    await page.close()
  }
})

test('site brand pairs the LeapView wordmark with the Lucide Aperture ring mark', async () => {
  const page = await browser.newPage({
    viewport: { width: 1600, height: 900 },
  })
  try {
    await page.goto(baseURL)
    const brand = page.getByRole('link', { name: 'LeapView', exact: true }).first()
    const mark = brand.locator('lv-brand-mark')
    expect(await mark.count()).toBe(1)
    expect(await mark.getAttribute('aria-hidden')).toBe('true')
    expect(await mark.evaluate((element) => element.shadowRoot?.querySelectorAll('svg').length)).toBe(1)
    expect(
      await mark.evaluate((element) => {
        const style = getComputedStyle(element)
        const wordmarkStyle = getComputedStyle(element.nextElementSibling!)
        return {
          backgroundColor: style.backgroundColor,
          colorMatchesWordmark: style.color === wordmarkStyle.color,
          borderRadius: style.borderRadius,
          borderWidth: style.borderWidth,
        }
      }),
    ).toEqual({ backgroundColor: 'rgba(0, 0, 0, 0)', colorMatchesWordmark: true, borderRadius: '0px', borderWidth: '0px' })
    const lockup = await brand.evaluate((element) => {
      const mark = element.querySelector('lv-brand-mark')
      const glyph = mark?.shadowRoot?.querySelector('svg')
      const wordmark = element.querySelector('span')
      const markBounds = mark?.getBoundingClientRect()
      const glyphBounds = glyph?.getBoundingClientRect()
      const wordmarkBounds = wordmark?.getBoundingClientRect()
      if (!markBounds || !glyphBounds || !wordmarkBounds) throw new Error('brand lockup is incomplete')
      return {
        markWidth: markBounds.width,
        markHeight: markBounds.height,
        opticalGap: wordmarkBounds.left - markBounds.right,
        centerDelta: Math.abs((markBounds.top + markBounds.bottom) / 2 - (wordmarkBounds.top + wordmarkBounds.bottom) / 2),
      }
    })
    expect(lockup.markWidth).toBe(28)
    expect(lockup.markHeight).toBe(28)
    expect(lockup.opticalGap).toBe(6)
    expect(lockup.centerDelta).toBeLessThanOrEqual(1)
    expect(await mark.evaluate((element) => element.shadowRoot?.querySelectorAll('circle[cx="12"][cy="12"][r="10"]').length)).toBe(1)
    expect(await mark.evaluate((element) => element.shadowRoot?.querySelectorAll('path[d="m14.31 8 5.74 9.94"]').length)).toBe(1)
    expect(
      await mark.evaluate((element) => {
        const svg = element.shadowRoot?.querySelector('svg')
        return { width: svg?.getAttribute('width'), height: svg?.getAttribute('height'), strokeWidth: svg?.getAttribute('stroke-width') }
      }),
    ).toEqual({ width: '28', height: '28', strokeWidth: '1.8' })
    const navigation = await page.locator('.site-nav').evaluate((element) => ({
      left: element.getBoundingClientRect().left,
      width: element.getBoundingClientRect().width,
      viewportWidth: window.innerWidth,
    }))
    expect(navigation.left).toBe(0)
    expect(navigation.width).toBe(navigation.viewportWidth)
  } finally {
    await page.close()
  }
})

test('desktop download page presents the same manifest-backed early preview', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  try {
    await page.goto(`${baseURL}/download`)

    expect(await page.getByRole('heading', { level: 1, name: 'LeapView on your desktop.' }).isVisible()).toBe(true)
    expect(await page.locator('.site-download-hero > .site-eyebrow').count()).toBe(0)
    expect(await page.getByText('Early preview', { exact: true }).isVisible()).toBe(true)
    expect(await page.getByText('These installers are not code-signed.', { exact: false }).isVisible()).toBe(true)
    expect(await page.getByRole('link', { name: 'Read the install guide' }).getAttribute('href')).toBe('/docs/desktop/install')
    expect(await page.getByRole('link', { name: 'Review desktop security' }).getAttribute('href')).toBe('/docs/desktop/security')
    expect(await page.locator('.site-download-platform').count()).toBe(3)
    expect(await page.locator('.site-download-artifact .site-button-primary').count()).toBe(4)
  } finally {
    await page.close()
  }
})

test('homepage navigation hides Desktop and Visuals while their pages remain available', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    expect(await page.locator('.site-header a[href="/download"], .site-header a[href="/visuals"], .site-footer a[href="/visuals"]').count()).toBe(0)
    expect(await page.locator('.site-header').getByRole('link', { name: 'Docs' }).count()).toBe(1)
  } finally {
    await page.close()
  }
})

test('site loads Inter and keeps readable homepage and documentation type', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  try {
    await page.goto(baseURL)
    const fontLoaded = await page.evaluate(async () => {
      await document.fonts.load('400 16px "Inter Variable"')
      return document.fonts.check('400 16px "Inter Variable"')
    })
    expect(fontLoaded).toBe(true)
    const heading = await page.locator('.site-home .hero h1').evaluate((element) => getComputedStyle(element))
    expect(Number.parseFloat(heading.letterSpacing)).toBeLessThan(0)
    await page.goto(`${baseURL}/docs/introduction`)
    expect(await page.locator('.site-docs-article').evaluate((element) => Number.parseFloat(getComputedStyle(element).fontSize))).toBeGreaterThanOrEqual(16)
  } finally {
    await page.close()
  }
})

test('documentation header keeps only search and theme actions', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/introduction`)
    const header = page.locator('.site-header')
    const actions = header.locator('.site-nav-actions')
    expect(await header.getByRole('link', { name: 'LeapView', exact: true }).count()).toBe(1)
    expect(await actions.locator('lv-site-search').count()).toBe(1)
    expect(await actions.locator('lv-site-theme-toggle').count()).toBe(1)
    expect(await actions.locator('lv-site-docs-drawer-toggle').count()).toBe(0)
    expect(await actions.locator('lv-site-mobile-menu').count()).toBe(0)
    expect(await actions.getByRole('link', { name: 'Docs', exact: true }).count()).toBe(0)
    expect(await actions.getByRole('link', { name: 'Demo', exact: true }).count()).toBe(0)
    expect(await actions.getByRole('link', { name: 'Visuals', exact: true }).count()).toBe(0)

    await page.setViewportSize({ width: 390, height: 844 })
    expect(await actions.getByRole('button', { name: 'Search documentation' }).isVisible()).toBe(true)
    const docsMenu = page.locator('.site-docs-article-header lv-site-docs-drawer-toggle:not([placement])')
    expect(await docsMenu.count()).toBe(1)
    expect(await docsMenu.getByRole('button', { name: 'Open documentation menu' }).isVisible()).toBe(true)

    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(baseURL)
    const siteActions = page.locator('.site-header .site-nav-actions')
    expect(await siteActions.getByRole('link', { name: 'Docs', exact: true }).count()).toBe(1)
    expect(await siteActions.getByRole('link', { name: 'GitHub', exact: true }).getAttribute('href')).toBe('https://github.com/flidai/leapview')
    expect(await siteActions.getByRole('link', { name: 'Discord', exact: true }).getAttribute('href')).toBe('https://discord.gg/pcfV4zAeRV')
    expect(await siteActions.getByRole('link', { name: 'Demo', exact: true }).count()).toBe(0)
    expect(await siteActions.getByRole('link', { name: 'Visuals', exact: true }).count()).toBe(0)
  } finally {
    await page.close()
  }
})

test('site header follows homepage section colors on scroll', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    const header = page.locator('.site-header')
    expect(await header.evaluate((element) => element.classList.contains('is-scrolled'))).toBe(false)
    expect(await header.evaluate((element) => getComputedStyle(element).borderBottomWidth)).toBe('0px')

    await page.evaluate(() => {
      document.documentElement.style.scrollBehavior = 'auto'
      window.scrollTo(0, 600)
    })
    await page.waitForFunction(() => document.querySelector('.site-header')?.classList.contains('is-scrolled'))
    expect(await header.evaluate((element) => getComputedStyle(element).borderBottomWidth)).toBe('0px')
    await page.waitForFunction(() => getComputedStyle(document.querySelector('.site-header')!, '::before').backdropFilter === 'blur(12px)')
    expect(await header.evaluate((element) => getComputedStyle(element, '::before').backgroundColor)).not.toBe('rgba(0, 0, 0, 0)')

    const sectionFills: string[] = []
    for (const id of ['enterprise', 'openness']) {
      const section = page.locator(`#${id}`)
      const color = await section.evaluate((element) => getComputedStyle(element).backgroundColor)
      await section.evaluate((element) => window.scrollTo(0, element.getBoundingClientRect().top + window.scrollY + 180))
      await page.waitForFunction((expected) => document.querySelector<HTMLElement>('.site-header')?.style.getPropertyValue('--site-header-fill').includes(expected), color)
      sectionFills.push(await header.evaluate((element) => element.style.getPropertyValue('--site-header-fill')))
    }
    expect(sectionFills[0]).not.toBe(sectionFills[1])

    await page.evaluate(() => {
      const section = document.querySelector('#openness')!
      const header = document.querySelector<HTMLElement>('.site-header')!
      window.scrollTo(0, section.getBoundingClientRect().top + window.scrollY - header.offsetHeight / 2)
    })
    await page.waitForFunction(() => getComputedStyle(document.querySelector('.site-header')!, '::before').backgroundImage.includes('linear-gradient'))

    await page.setViewportSize({ width: 390, height: 844 })
    const menu = page.locator('lv-site-mobile-menu')
    await menu.getByRole('button').click()
    expect(await menu.getByRole('link', { name: 'Docs' }).isVisible()).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)

    await page.evaluate(() => window.scrollTo(0, 0))
    await page.waitForFunction(() => !document.querySelector('.site-header')?.classList.contains('is-scrolled'))
  } finally {
    await page.close()
  }
})

test('site theme control updates the homepage screenshot and colors', async () => {
  const page = await browser.newPage()
  try {
    await page.addInitScript(() => localStorage.setItem('leapview-color-mode', 'dark'))
    await page.goto(baseURL)
    await page.waitForFunction(() => Boolean(customElements.get('lv-site-theme-toggle')))
    const toggle = page.locator('lv-site-theme-toggle button[data-theme-toggle]')
    await page.waitForFunction(() => document.querySelector('#product-image')?.getAttribute('src') === '/static/product-dashboard-dark.png')
    await page.evaluate(() => {
      document.documentElement.style.scrollBehavior = 'auto'
      const section = document.querySelector('#enterprise')!
      window.scrollTo(0, section.getBoundingClientRect().top + window.scrollY + 180)
    })
    await page.waitForFunction(() => {
      const section = document.querySelector('#enterprise')!
      return document.querySelector<HTMLElement>('.site-header')?.style.getPropertyValue('--site-header-fill').includes(getComputedStyle(section).backgroundColor)
    })
    const darkHeaderFill = await page.locator('.site-header').evaluate((element) => element.style.getPropertyValue('--site-header-fill'))
    await toggle.click()
    await page.waitForFunction(() => document.documentElement.dataset.colorMode === 'auto')
    await toggle.click()
    await page.waitForFunction(() => document.documentElement.dataset.colorMode === 'light')
    await page.waitForFunction(() => document.querySelector('#product-image')?.getAttribute('src') === '/static/product-dashboard-light.png')
    expect(await page.locator('.site-home').evaluate((element) => element.classList.contains('is-light'))).toBe(true)
    await page.waitForFunction(() => {
      const section = document.querySelector('#enterprise')!
      return document.querySelector<HTMLElement>('.site-header')?.style.getPropertyValue('--site-header-fill').includes(getComputedStyle(section).backgroundColor)
    })
    expect(await page.locator('.site-header').evaluate((element) => element.style.getPropertyValue('--site-header-fill'))).not.toBe(darkHeaderFill)
  } finally {
    await page.close()
  }
})

test('mobile homepage keeps the shared navigation and all sections usable', async () => {
  const context = await browser.newContext({ hasTouch: true, viewport: { width: 320, height: 900 } })
  const page = await context.newPage()
  try {
    await page.goto(baseURL)
    const menu = page.locator('lv-site-mobile-menu')
    const button = menu.locator('button')
    expect(await button.count()).toBe(1)
    await button.click()
    expect(await button.getAttribute('aria-expanded')).toBe('true')
    expect(await menu.getByRole('link', { name: 'Docs' }).count()).toBe(1)
    expect(await menu.getByRole('link', { name: 'Visuals' }).count()).toBe(0)
    expect(await page.locator('.site-header').getByRole('link', { name: 'GitHub' }).isVisible()).toBe(true)
    expect(await page.locator('.site-header').getByRole('link', { name: 'Discord' }).isVisible()).toBe(true)
    expect(await page.locator('.orbit-node').count()).toBe(17)
    expect(await page.locator('[data-project-file]').count()).toBe(6)
    const tabList = page.getByRole('tablist', { name: 'Sales example files' })
    expect(await tabList.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true)
    expect(await page.locator('#project-tab-semantics').getAttribute('aria-selected')).toBe('true')
    await page.locator('#project-tab-dashboard').click()
    await page.waitForFunction(() => document.querySelector('#project-code')?.textContent?.includes('kind: Dashboard'))
    expect(await page.locator('#project-file-path').textContent()).toBe('sales-project / dashboards / executive-sales.yaml')
    expect(await page.locator('#project-tab-dashboard').getAttribute('aria-selected')).toBe('true')
    await page.keyboard.press('ArrowLeft')
    expect(await page.locator('#project-tab-pipeline').getAttribute('aria-selected')).toBe('true')
    await page.waitForFunction(() => document.querySelector('#project-code')?.textContent?.includes('kind: Pipeline'))
    for (const width of [320, 390, 768]) {
      await page.setViewportSize({ width, height: 900 })
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    }
  } finally {
    await context.close()
  }
})

test('getting started route directs users through the first learning path', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/docs/getting-started`)
    await page.evaluate(() => {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: {
          writeText: async (value: string) => {
            document.documentElement.dataset.copiedMarkdown = value
          },
        },
      })
    })
    expect(await page.getByRole('article').count()).toBe(1)
    expect(await page.getByRole('heading', { name: 'Get started with LeapView' }).isVisible()).toBe(true)
    const sidebar = page.locator('.site-docs-sidebar')
    expect(await sidebar.count()).toBe(1)
    expect(await sidebar.evaluate((element) => getComputedStyle(element).position)).toBe('sticky')
    const docsNavigation = page.getByRole('navigation', {
      name: 'Documentation',
    })
    expect(await docsNavigation.getByRole('link', { name: 'Get started with LeapView' }).getAttribute('aria-current')).toBe('page')
    const startGroup = sidebar.locator('details[data-site-docs-group="start"]')
    expect(await startGroup.count()).toBe(1)
    expect(await startGroup.getAttribute('open')).not.toBeNull()
    const configurationGroup = sidebar.locator('details[data-site-docs-group="reference-configuration"]')
    expect(await configurationGroup.count()).toBe(1)
    expect(await configurationGroup.getAttribute('open')).toBeNull()
    expect(await configurationGroup.locator('a[href="/docs/config/connection"]').count()).toBe(1)
    expect(await configurationGroup.locator('a[href="/docs/config/project"]').count()).toBe(0)
    expect(await docsNavigation.locator('a[href="/docs/enterprise-auth"]').count()).toBe(1)
    expect(await docsNavigation.locator('a[href="/docs/storage-architecture"]').count()).toBe(1)
    expect(await docsNavigation.getByText('Dashboard demo', { exact: true }).count()).toBe(0)
    const referenceGroup = sidebar.locator('details[data-site-docs-group="reference"]')
    expect(await referenceGroup.count()).toBe(1)
    expect(await referenceGroup.getAttribute('open')).toBeNull()
    const chartGroup = sidebar.locator('details[data-site-docs-group="reference-visuals"]')
    expect(await chartGroup.count()).toBe(1)
    expect(await chartGroup.getAttribute('open')).toBeNull()
    expect(await chartGroup.locator('a[href="/docs/visuals/overview"]').count()).toBe(1)
    expect(await chartGroup.locator('a[href="/docs/visuals/line"]').count()).toBe(1)
    const apiGroup = sidebar.locator('details[data-site-docs-group="reference-api"]')
    expect(await apiGroup.count()).toBe(1)
    expect(await apiGroup.locator('a[href="/docs/api"]').getAttribute('href')).toBe('/docs/api')
    expect(await apiGroup.locator('a[href="/docs/api/projects"]').count()).toBe(1)
    const breadcrumb = page.getByRole('navigation', { name: 'Breadcrumb' })
    expect(await breadcrumb.getByRole('link', { name: 'Start here' }).getAttribute('href')).toBe('/docs/introduction')
    expect(await breadcrumb.getByRole('link', { name: 'Documentation' }).count()).toBe(0)
    expect(await breadcrumb.getByRole('link', { name: 'LeapView' }).count()).toBe(0)
    expect(await breadcrumb.getByText('Getting started', { exact: true }).getAttribute('aria-current')).toBe('page')

    const markdownCopy = page.locator('lv-site-markdown-copy')
    expect(await markdownCopy.getAttribute('markdown')).toStartWith('# Get started with LeapView')
    expect(await markdownCopy.evaluate((element) => (element as HTMLElement & { markdown?: string }).markdown)).toStartWith('# Get started with LeapView')
    const copyMarkdown = page.getByRole('button', { name: 'Copy Markdown' })
    await copyMarkdown.click()
    await page.waitForFunction(() => document.querySelector('lv-site-markdown-copy')?.shadowRoot?.querySelector('button')?.getAttribute('aria-label') === 'Markdown copied')
    expect(await markdownCopy.evaluate((element) => element.shadowRoot?.querySelector('button')?.getAttribute('aria-label'))).toBe('Markdown copied')
    expect(await page.locator('html').getAttribute('data-copied-markdown')).toStartWith('# Get started with LeapView')

    expect(await page.locator('.site-guide-step').count()).toBe(0)
    expect(await page.locator('.site-docs-article pre code').count()).toBe(0)
    expect(await page.getByRole('heading', { name: 'Choose your starting point' }).isVisible()).toBe(true)
    expect(await page.getByRole('heading', { name: 'What you will learn' }).isVisible()).toBe(true)
    expect(await page.getByRole('link', { name: 'Installation' }).count()).toBeGreaterThan(0)
    expect(await page.getByRole('link', { name: 'Build your first dashboard' }).count()).toBeGreaterThan(0)
    expect(await page.getByRole('link', { name: 'Visual types' }).count()).toBeGreaterThan(0)
  } finally {
    await page.close()
  }
})

test('documentation index exposes every task-oriented section', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/docs`)
    expect(await page.getByRole('heading', { name: 'Documentation' }).isVisible()).toBe(true)
    const articleNavigation = page.getByRole('navigation', {
      name: 'Documentation sections',
    })
    for (const title of ['Start here', 'Build dashboards', 'Deploy and operate', 'Reference', 'Architecture and contributing']) {
      expect(await articleNavigation.getByRole('heading', { name: title }).isVisible()).toBe(true)
    }
    expect(await page.getByRole('searchbox', { name: 'Search documentation' }).count()).toBe(1)
  } finally {
    await page.close()
  }
})

test('documentation search finds authored and generated content', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/docs/search?q=semantic+relationships`)
    expect(await page.getByRole('heading', { name: 'Search documentation' }).isVisible()).toBe(true)
    expect(await page.getByRole('searchbox', { name: 'Search documentation' }).inputValue()).toBe('semantic relationships')
    expect(await page.getByRole('link', { name: 'Semantic models' }).count()).toBeGreaterThan(0)
    expect(await page.getByText(/results for "semantic relationships"/).isVisible()).toBe(true)
  } finally {
    await page.close()
  }
})

test('chart documentation renders every executable variation from its YAML', async () => {
  const expectedExampleIDs = lineExampleIDs
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/docs/visuals/line`)
    const sidebar = page.locator('.site-docs-sidebar')
    const startGroup = sidebar.locator('details[data-site-docs-group="start"]')
    const referenceGroup = sidebar.locator('details[data-site-docs-group="reference"]')
    const chartGroup = sidebar.locator('details[data-site-docs-group="reference-visuals"]')
    const apiGroup = sidebar.locator('details[data-site-docs-group="reference-api"]')
    expect(await startGroup.count()).toBe(1)
    expect(await referenceGroup.getAttribute('open')).not.toBeNull()
    expect(await chartGroup.getAttribute('open')).not.toBeNull()
    expect(await apiGroup.getAttribute('open')).toBeNull()
    const breadcrumb = page.getByRole('navigation', { name: 'Breadcrumb' })
    expect(await breadcrumb.getByRole('link', { name: 'Visuals' }).getAttribute('href')).toBe('/docs/visuals/overview')
    expect(await breadcrumb.getByRole('link', { name: 'Documentation' }).count()).toBe(0)
    expect(await page.getByRole('heading', { name: 'Line chart' }).isVisible()).toBe(true)
    expect(await page.getByRole('heading', { name: 'API reference' }).isVisible()).toBe(true)
    expect(await page.locator('.site-visual-api-summary').count()).toBe(0)
    const articleHeadings = await page.locator('.site-docs-article h2').allTextContents()
    expect(articleHeadings.indexOf('API reference')).toBeGreaterThan(articleHeadings.indexOf('Stepped line'))
    expect(articleHeadings.indexOf('About this page')).toBeGreaterThan(articleHeadings.indexOf('API reference'))
    const fieldReference = page.getByRole('table', { name: 'API reference' })
    expect(await fieldReference.getByRole('columnheader').allTextContents()).toEqual(['Field', 'Type', 'Default', 'Allowed values', 'Description'])
    const stepReference = fieldReference.getByRole('row').filter({ hasText: 'presentation.step' })
    expect(await stepReference.count()).toBe(1)
    expect(await stepReference.textContent()).toContain('boolean')
    const referenceColors = await page.locator('.site-docs-article').evaluate((article) => {
      const summaryCode = article.querySelector('#site-visual-api-reference + p code')
      const fieldCode = article.querySelector('table[aria-labelledby="site-visual-api-reference"] tbody th code')
      const keyField = article.querySelector('.site-visual-key-field')
      const keyFieldCode = keyField?.querySelector('code')
      if (!summaryCode || !fieldCode || !keyField || !keyFieldCode) throw new Error('visual reference color targets are missing')
      return {
        article: getComputedStyle(article).color,
        field: getComputedStyle(fieldCode).color,
        interactive: getComputedStyle(keyField).color,
        interactiveCode: getComputedStyle(keyFieldCode).color,
        summary: getComputedStyle(summaryCode).color,
      }
    })
    expect(referenceColors.summary).toBe(referenceColors.article)
    expect(referenceColors.field).toBe(referenceColors.article)
    expect(referenceColors.interactiveCode).toBe(referenceColors.interactive)
    expect(referenceColors.interactive).not.toBe(referenceColors.article)
    expect(await page.getByRole('heading', { name: 'Basic' }).isVisible()).toBe(true)
    expect(await page.getByRole('heading', { name: 'Multiple series' }).isVisible()).toBe(true)
    expect(await page.getByRole('heading', { name: 'Visual calculation' }).isVisible()).toBe(true)
    expect(await page.getByRole('heading', { name: 'Stepped line' }).isVisible()).toBe(true)
    await page.waitForFunction((expectedIDs) => {
      const examples = [...document.querySelectorAll('lv-site-visual-example')] as Array<HTMLElement & { shadowRoot: ShadowRoot }>
      return examples.length === expectedIDs.length && examples.every((example, index) => {
        const host = example.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: { dataState?: { datasets?: Array<{ rows?: unknown[] }> } } }
        return example.getAttribute('example-id') === expectedIDs[index] && Boolean(host?.envelope?.dataState?.datasets?.some((dataset) => dataset.rows?.length))
      })
    }, expectedExampleIDs)
    expect(await page.locator('lv-site-visual-example').evaluateAll((examples) => examples.map((example) => example.getAttribute('example-id')))).toEqual(expectedExampleIDs)
    const configurations = await page.locator('.site-docs-article pre code').allTextContents()
    expect(configurations.some((source) => source.includes('visuals:\n  revenue_line:'))).toBe(true)
    expect(configurations.every((source) => !source.includes('shape:'))).toBe(true)
    expect(configurations.some((source) => source.includes('step: true'))).toBe(true)
    const keyFields = await page.locator('.site-visual-key-fields').allTextContents()
    expect(keyFields).toHaveLength(expectedExampleIDs.length)
    expect(keyFields[expectedExampleIDs.indexOf('revenue_line_running')]).toContain('calculations')
    expect(keyFields[expectedExampleIDs.indexOf('revenue_line_step')]).toContain('presentation.step')
    await page.waitForFunction(() => document.querySelectorAll('lv-code-block[data-visual-example="revenue_line_step"] .code-block-highlighted-line').length === 3)
    const steppedConfiguration = page.locator('lv-code-block[data-visual-example="revenue_line_step"]')
    expect(await steppedConfiguration.getAttribute('data-highlighted-fields')).toBe('presentation.dataZoom,presentation.showSymbols,presentation.step')
    expect(await steppedConfiguration.locator('.code-block-highlighted-line').allTextContents()).toEqual([
      '      step: true',
      '      showSymbols: false',
      '      dataZoom: true',
    ])
    expect(await steppedConfiguration.locator('.code-block-highlighted-line').first().evaluate((line) => ({
      display: getComputedStyle(line).display,
      marker: getComputedStyle(line, '::before').width,
      padding: getComputedStyle(line).paddingInlineStart,
    }))).toEqual({ display: 'inline-block', marker: '4px', padding: '0px' })
    const stepField = page.getByRole('button', { name: 'Highlight presentation.step in YAML' })
    expect(await stepField.count()).toBe(1)
    expect(await stepField.getAttribute('aria-controls')).toBe('visual-example-revenue_line_step-yaml')
    await stepField.focus()
    await page.waitForFunction(() => document.querySelectorAll('lv-code-block[data-visual-example="revenue_line_step"] .code-block-focused-line').length === 1)
    expect(await steppedConfiguration.locator('.code-block-focused-line').allTextContents()).toEqual(['      step: true'])
    await stepField.blur()
    await page.waitForFunction(() => document.querySelectorAll('lv-code-block[data-visual-example="revenue_line_step"] .code-block-focused-line').length === 0)
    const stepped = await page.locator('lv-site-visual-example[example-id="revenue_line_step"]').evaluate((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: { spec?: { presentation?: Record<string, unknown> } } }
      return host?.envelope?.spec?.presentation?.step
    })
    expect(stepped).toBe(true)
  } finally {
    await page.close()
  }
})

test('KPI documentation automatically demonstrates every valid layout from one visual definition', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/docs/visuals/kpi`)
    await page.waitForFunction(() => document.querySelectorAll('lv-site-visual-example[type="kpi"]').length === 9)
    await page.waitForFunction(() => {
      const examples = [...document.querySelectorAll('lv-site-visual-example[type="kpi"]')]
      return examples.length === 9 && examples.every((example) => example.shadowRoot?.querySelectorAll('[data-layout-preview]').length === 2)
    })
    const favorable = page.locator('lv-site-visual-example[example-id="revenue_kpi_favorable"]')
    const previews = await favorable.evaluate((example) =>
      [...example.shadowRoot!.querySelectorAll<HTMLElement>('[data-layout-preview]')].map((preview) => {
        const host = preview.querySelector('lv-visualization-host')
        const renderer = host?.shadowRoot?.querySelector<HTMLElement>('.renderer')
        return {
          documented: preview.dataset.layoutPreview,
          selected: renderer?.dataset.layoutVariant,
          fit: renderer?.dataset.layoutFit,
          sparkline: renderer?.querySelectorAll('.lv-kpi-sparkline').length,
          width: Math.round(host?.getBoundingClientRect().width ?? 0),
          height: Math.round(host?.getBoundingClientRect().height ?? 0),
        }
      }),
    )
    expect(previews).toEqual([
      { documented: 'wide', selected: 'wide', fit: 'fit', sparkline: 1, width: 320, height: 148 },
      { documented: 'stacked', selected: 'stacked', fit: 'fit', sparkline: 1, width: 192, height: 124 },
    ])
    const yaml = await page.locator('lv-code-block[data-visual-example="revenue_kpi_favorable"]').getAttribute('code')
      ?? await page.locator('lv-code-block[data-visual-example="revenue_kpi_favorable"]').textContent()
    expect(yaml).not.toContain('layout_variant')
    expect(yaml).not.toContain('responsive_visibility')
  } finally {
    await page.close()
  }
})

test('responsive widget reference covers every KPI and filter layout plus intermediate sizes', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    await page.goto(`${baseURL}/visuals/responsive`)
    await page.waitForFunction(() => {
      const reference = document.querySelector('lv-site-responsive-widget-reference')
      const root = reference?.shadowRoot
      return root?.querySelectorAll('[data-kpi-scenario]').length === 9
        && root.querySelectorAll('[data-filter-scenario]').length === 5
        && root.querySelectorAll('[data-layout-frame]').length === 26
    })
    await page.waitForFunction(() => {
      const root = document.querySelector('lv-site-responsive-widget-reference')?.shadowRoot
      const kpis = [...(root?.querySelectorAll('[data-kpi-scenario] lv-visualization-host') ?? [])]
      const filters = [...(root?.querySelectorAll('[data-filter-scenario] lv-slicer') ?? [])]
      return kpis.length === 18 && filters.length === 8
        && kpis.every((host) => host.shadowRoot?.querySelector('.renderer')?.getAttribute('data-layout-fit') === 'fit')
        && filters.every((slicer) => slicer.shadowRoot?.querySelector('lv-filter-leaf')?.getAttribute('data-layout-fit') === 'fit')
    })

    const coverage = await page.locator('lv-site-responsive-widget-reference').evaluate((element) => {
      const root = element.shadowRoot!
      return {
        kpiScenarios: root.querySelectorAll('[data-kpi-scenario]').length,
        kpiFrames: root.querySelectorAll('[data-kpi-scenario] [data-layout-frame]').length,
        filterScenarios: root.querySelectorAll('[data-filter-scenario]').length,
        filterFrames: root.querySelectorAll('[data-filter-scenario] [data-layout-frame]').length,
        dimensions: [...root.querySelectorAll('[data-layout-frame]')].every((frame) => /\d+×\d+/.test(frame.getAttribute('aria-label') ?? '')),
        valuesFit: [...root.querySelectorAll('[data-kpi-scenario] lv-visualization-host')].every((host) => {
          const value = host.shadowRoot?.querySelector<HTMLElement>('.lv-visualization-kpi')
          const card = host.shadowRoot?.querySelector<HTMLElement>('.lv-kpi-card')
          return Boolean(value && card)
            && value!.scrollWidth <= value!.clientWidth
            && value!.clientHeight > 0
            && card!.scrollHeight <= card!.clientHeight
        }),
        filterIdleSummariesAbsent: [...root.querySelectorAll('[data-filter-scenario] lv-slicer')].every((slicer) => {
          const leaf = slicer.shadowRoot?.querySelector('lv-filter-leaf')
          return !leaf?.shadowRoot?.querySelector('.selection-summary, .status')
        }),
        frameRowsFit: [...root.querySelectorAll<HTMLElement>('.frame-row')].every((row) => row.scrollWidth <= row.clientWidth + 1),
        captionsAreCompact: [...root.querySelectorAll('figcaption')].every((caption) => !caption.textContent?.includes('exact minimum')),
        stressTestLabel: root.querySelector('[data-kpi-scenario="revenue_kpi_all_features"] h3')?.textContent?.trim(),
        missingComparison: (() => {
          const host = root.querySelector('[data-kpi-scenario="revenue_kpi_missing_comparison"] lv-visualization-host')
          const card = host?.shadowRoot?.querySelector<HTMLElement>('.lv-kpi-card')
          return { text: card?.textContent?.replace(/\s+/g, ' ').trim(), aria: card?.getAttribute('aria-label') }
        })(),
        filterControlType: [...root.querySelectorAll('[data-filter-scenario] lv-slicer')].every((slicer) => {
          const leaf = slicer.shadowRoot?.querySelector('lv-filter-leaf')
          const controls = [...(leaf?.shadowRoot?.querySelectorAll<HTMLElement>('input, select, button') ?? [])]
          return controls.length > 0 && controls.every((control) => {
            const size = Number.parseFloat(getComputedStyle(control).fontSize)
            return size > 0 && size <= 14
          })
        }),
        wideTrendBelowValue: (() => {
          const host = root.querySelector('[data-kpi-scenario="revenue_kpi_trend"] [data-layout-frame="wide"] lv-visualization-host')
          const value = host?.shadowRoot?.querySelector<HTMLElement>('.lv-visualization-kpi')
          const sparkline = host?.shadowRoot?.querySelector<HTMLElement>('.lv-kpi-sparkline')
          if (!value || !sparkline) return false
          return sparkline.getBoundingClientRect().top >= value.getBoundingClientRect().bottom
        })(),
      }
    })
    expect(coverage).toEqual({
      kpiScenarios: 9,
      kpiFrames: 18,
      filterScenarios: 5,
      filterFrames: 8,
      dimensions: true,
      valuesFit: true,
      filterIdleSummariesAbsent: true,
      frameRowsFit: true,
      captionsAreCompact: true,
      stressTestLabel: 'All features — stress test',
      missingComparison: {
        text: 'Revenue with unavailable comparison$4.6KPrior period: —Unavailable',
        aria: 'Revenue with unavailable comparison. Demonstrates an explicitly unavailable comparison. Current $4,597.00. Prior period —. Change Unavailable.',
      },
      filterControlType: true,
      wideTrendBelowValue: true,
    })

    const reference = page.locator('lv-site-responsive-widget-reference')
    const width = reference.getByRole('slider', { name: 'Preview width' })
    const height = reference.getByRole('slider', { name: 'Preview height' })
    await width.fill('250')
    await height.fill('130')
    await page.waitForFunction(() => document.querySelector('lv-site-responsive-widget-reference')?.shadowRoot?.querySelector('[data-playground-frame]')?.getAttribute('data-selected-layout') === 'stacked')
    expect(await reference.locator('[data-playground-frame]').getAttribute('data-fit')).toBe('fit')

    await width.fill('520')
    await page.waitForFunction(() => {
      const frame = document.querySelector('lv-site-responsive-widget-reference')?.shadowRoot?.querySelector('[data-playground-frame]')
      return frame?.getAttribute('data-selected-layout') === 'stacked' && frame.getBoundingClientRect().width >= 520
    })
    const maxWidthStackedValueFits = await reference.evaluate((element) => {
      const host = element.shadowRoot!.querySelector('[data-playground-frame] lv-visualization-host')
      const value = host?.shadowRoot?.querySelector<HTMLElement>('.lv-visualization-kpi')
      return Boolean(value) && value!.scrollHeight <= value!.clientHeight && value!.scrollWidth <= value!.clientWidth
    })
    expect(maxWidthStackedValueFits).toBe(true)
    await width.fill('250')

    await reference.getByRole('combobox', { name: 'Preview widget' }).selectOption('date-range')
    await page.waitForFunction(() => document.querySelector('lv-site-responsive-widget-reference')?.shadowRoot?.querySelector('[data-playground-frame]')?.getAttribute('data-fit') === 'too-small')
    await height.fill('154')
    await page.waitForFunction(() => document.querySelector('lv-site-responsive-widget-reference')?.shadowRoot?.querySelector('[data-playground-frame]')?.getAttribute('data-fit') === 'fit')

    await page.setViewportSize({ width: 1150, height: 845 })
    const intermediateLayout = await reference.evaluate((element) => {
      const rows = [...element.shadowRoot!.querySelectorAll<HTMLElement>('.frame-row')]
      return {
        rowsFit: rows.every((row) => row.scrollWidth <= row.clientWidth + 1),
        constrainedRowsStack: rows
          .filter((row) => row.clientWidth < 532)
          .every((row) => getComputedStyle(row).flexDirection === 'column'),
        scenarioColumns: getComputedStyle(element.shadowRoot!.querySelector('.scenario-grid')!).gridTemplateColumns.split(' ').length,
      }
    })
    expect(intermediateLayout).toEqual({ rowsFit: true, constrainedRowsStack: true, scenarioColumns: 2 })

    await page.setViewportSize({ width: 390, height: 844 })
    const mobileLayout = await reference.evaluate((element) => {
      const root = element.shadowRoot!
      const frameRows = [...root.querySelectorAll<HTMLElement>('.frame-row')]
      return {
        frameRowsStack: frameRows.every((row) => getComputedStyle(row).flexDirection === 'column'),
        frameRowsFit: frameRows.every((row) => row.scrollWidth <= row.clientWidth + 1),
        documentFits: document.documentElement.scrollWidth === document.documentElement.clientWidth,
      }
    })
    expect(mobileLayout).toEqual({ frameRowsStack: true, frameRowsFit: true, documentFits: true })
    expect(await reference.locator('[data-playground-frame]').getAttribute('data-selected-layout')).toBe('stacked')

    await page.setViewportSize({ width: 355, height: 844 })
    const narrowMobileLayout = await reference.evaluate((element) => {
      const rows = [...element.shadowRoot!.querySelectorAll<HTMLElement>('.frame-row')]
      return {
        documentFits: document.documentElement.scrollWidth === document.documentElement.clientWidth,
        overflowIsContained: rows
          .filter((row) => row.scrollWidth > row.clientWidth + 1)
          .every((row) => getComputedStyle(row).overflowX === 'auto'),
      }
    })
    expect(narrowMobileLayout).toEqual({ documentFits: true, overflowIsContained: true })
  } finally {
    await page.close()
  }
}, 20_000)

test('governed label policies remain renderable across visual families, locales, and compact resizes', async () => {
  const context = await browser.newContext({ locale: 'pt-BR', viewport: { width: 1280, height: 900 } })
  const page = await context.newPage()
  const pageErrors: string[] = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  const cases = [
    { path: 'heatmap', id: 'category_status_heatmap_labels', density: 'dense' },
    { path: 'pie', id: 'category_pie_inside', density: 'automatic' },
    { path: 'scatter', id: 'delivery_scatter_labeled', density: 'automatic' },
    { path: 'tree', id: 'category_state_status_tree', density: 'automatic' },
    { path: 'gauge', id: 'review_gauge_thresholds', density: 'automatic' },
  ]
  try {
    for (const item of cases) {
      await page.goto(`${baseURL}/docs/visuals/${item.path}`)
      const example = page.locator(`lv-site-visual-example[example-id="${item.id}"]`)
      await page.waitForFunction(
        ({ id }) => {
          const element = document.querySelector(`lv-site-visual-example[example-id="${id}"]`)
          const host = element?.shadowRoot?.querySelector('lv-visualization-host')
          const canvas = host?.shadowRoot?.querySelector('canvas') as HTMLCanvasElement | null
          return Boolean(canvas?.width && canvas.height && !host?.shadowRoot?.querySelector('[role="alert"]'))
        },
        { id: item.id },
      )
      const desktop = await example.evaluate((element) => {
        const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & {
          envelope?: { spec?: { presentation?: { labelPolicy?: { density?: string; tooltipFallback?: boolean } } } }
        }
        const canvas = host?.shadowRoot?.querySelector('canvas') as HTMLCanvasElement | null
        return {
          density: host.envelope?.spec?.presentation?.labelPolicy?.density,
          fallback: host.envelope?.spec?.presentation?.labelPolicy?.tooltipFallback,
          width: canvas?.width ?? 0,
          height: canvas?.height ?? 0,
        }
      })
      expect(desktop).toMatchObject({ density: item.density, fallback: true })
      expect(desktop.width).toBeGreaterThan(0)
      expect(desktop.height).toBeGreaterThan(0)

      await page.setViewportSize({ width: 480, height: 900 })
      await page.waitForFunction(
        ({ id, desktopWidth }) => {
          const element = document.querySelector(`lv-site-visual-example[example-id="${id}"]`)
          const host = element?.shadowRoot?.querySelector('lv-visualization-host')
          const canvas = host?.shadowRoot?.querySelector('canvas') as HTMLCanvasElement | null
          return Boolean(canvas?.width && canvas.height && canvas.width < desktopWidth && !host?.shadowRoot?.querySelector('[role="alert"]'))
        },
        { id: item.id, desktopWidth: desktop.width },
      )
      const compact = await example.evaluate((element) => {
        const host = element.shadowRoot?.querySelector('lv-visualization-host')
        const canvas = host?.shadowRoot?.querySelector('canvas') as HTMLCanvasElement | null
        return { width: canvas?.width ?? 0, height: canvas?.height ?? 0 }
      })
      expect(compact.width).toBeGreaterThan(0)
      expect(compact.width).toBeLessThan(desktop.width)
      expect(compact.height).toBeGreaterThan(0)
      await page.setViewportSize({ width: 1280, height: 900 })
    }
    expect(pageErrors).toEqual([])
  } finally {
    await context.close()
  }
}, 60_000)

test('every visual documentation page mounts its generated production payloads', async () => {
  const page = await browser.newPage()
  const pageErrors: Array<{ url: string; message: string; stack?: string }> = []
  page.on('pageerror', (error) => pageErrors.push({ url: page.url(), message: error.message, stack: error.stack }))
  const visualTypes = ['line', 'area', 'bar', 'column', 'pie', 'donut', 'scatter', 'funnel', 'treemap', 'gauge', 'heatmap', 'sankey', 'graph', 'map', 'candlestick', 'boxplot', 'combo', 'waterfall', 'histogram', 'radar', 'tree', 'sunburst', 'kpi', 'table', 'matrix', 'pivot']
  try {
    for (const visualType of visualTypes) {
      await page.goto(`${baseURL}/docs/visuals/${visualType}`)
      const expected = visualType === 'map' ? 6 : visualType === 'line' ? lineExampleIDs.length : visualType === 'candlestick' ? 2 : visualType === 'kpi' ? 9 : visualType === 'table' ? 2 : ['matrix', 'pivot'].includes(visualType) ? 1 : 3
      await page.waitForFunction(
        ({ count }) => {
          const examples = [...document.querySelectorAll('lv-site-visual-example')]
          return examples.length === count && examples.every((example) => {
            const host = example.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: { dataState?: { datasets?: Array<{ rows?: unknown[] }>; blocks?: Record<string, { rows?: unknown[] }>; cardinality?: { count?: number } } } } | null
            const state = host?.envelope?.dataState
            return Boolean(state?.datasets?.some((dataset) => dataset.rows?.length) || Object.values(state?.blocks ?? {}).some((block) => block.rows?.length) || (state?.cardinality?.count ?? 0) > 0)
          })
        },
        { count: expected },
      )
      expect(await page.locator('lv-site-visual-example').count()).toBe(expected)
    }
    await page.goto(`${baseURL}/docs/visuals/gauge`)
    await page.waitForFunction(() => document.querySelectorAll('lv-site-visual-example').length === 3)
    const thresholds = await page.locator('lv-site-visual-example').nth(2).evaluate((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: { spec?: { presentation?: { thresholds?: unknown[] } } } }
      return host.envelope?.spec?.presentation?.thresholds?.length
    })
    expect(thresholds).toBe(3)

    await page.goto(`${baseURL}/docs/visuals/map`)
    await page.waitForFunction(() => document.querySelectorAll('lv-site-visual-example').length === 6)
    expect(await page.locator('lv-site-visual-example').first().evaluate((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: any }
      const envelope = host.envelope
      const rows = envelope?.dataState?.datasets?.[0]?.rows ?? []
      return [envelope?.spec?.kind, envelope?.spec?.layers?.[0]?.geometry?.id, rows.length, new Set(rows.map((row: unknown[]) => row[0])).size]
    })).toEqual(['geographic', 'br-states-ibge', 27, 27])
    expect(await page.locator('lv-site-visual-example').evaluateAll((elements) => elements.map((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: any }
      return host.envelope?.spec?.layers?.[0]?.kind
    }))).toEqual(['choropleth', 'point', 'heat', 'density', 'reference', 'path'])
    await page.waitForFunction(() => {
      const example = document.querySelector('lv-site-visual-example') as HTMLElement & { shadowRoot: ShadowRoot }
      const host = example?.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { shadowRoot: ShadowRoot }
      return Boolean(host?.shadowRoot?.querySelector('.renderer[aria-label]'))
    })
    expect(await page.locator('lv-site-visual-example').first().evaluate((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { shadowRoot: ShadowRoot }
      return host?.shadowRoot?.querySelector('.renderer[aria-label]')?.getAttribute('aria-label')
    })).not.toContain('NaN')
    await page.goto(`${baseURL}/docs/visuals/combo`)
    await page.waitForFunction(() => document.querySelectorAll('lv-site-visual-example').length === 3)
    expect(await page.locator('lv-site-visual-example').first().evaluate((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: any }
      return [host.envelope?.spec?.kind, host.envelope?.spec?.presentation?.comboSeries?.length]
    })).toEqual(['cartesian', 2])
    expect(pageErrors).toEqual([])
  } finally {
    await page.close()
  }
}, 120_000)

test('map documentation renders fitted, attributed canvases without adapter errors', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/docs/visuals/map`)
    await page.waitForFunction(() => {
      const examples = [...document.querySelectorAll('lv-site-visual-example')]
      return examples.length === 6 && examples.every((element) => {
        const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: unknown } | null
        return Boolean(host?.envelope)
      })
    })
    try {
      await page.waitForFunction(() => {
        const examples = [...document.querySelectorAll('lv-site-visual-example')]
        return examples.every((element) => {
          const host = element.shadowRoot?.querySelector('lv-visualization-host')
          const renderer = host?.shadowRoot?.querySelector('.renderer')
          return Boolean(host?.shadowRoot?.querySelector('canvas.maplibregl-canvas')) && renderer?.getAttribute('aria-busy') === 'false' && !host?.shadowRoot?.querySelector('[role="alert"]')
        }) && Boolean(examples[0]?.shadowRoot?.querySelector('lv-visualization-host')?.shadowRoot?.querySelector('[data-map-attribution]')?.textContent?.trim())
      })
    } catch (error) {
      const diagnostics = await page.locator('lv-site-visual-example').evaluateAll((elements) => elements.map((element) => {
        const host = element.shadowRoot?.querySelector('lv-visualization-host')
        const renderer = host?.shadowRoot?.querySelector('.renderer')
        return {
          id: element.getAttribute('example-id'),
          busy: renderer?.getAttribute('aria-busy'),
          canvas: Boolean(host?.shadowRoot?.querySelector('canvas.maplibregl-canvas')),
          alert: host?.shadowRoot?.querySelector('[role="alert"]')?.textContent?.trim() ?? '',
          attribution: host?.shadowRoot?.querySelector('[data-map-attribution]')?.textContent?.trim() ?? '',
        }
      }))
      throw new Error(`map examples did not settle: ${JSON.stringify(diagnostics)}`, { cause: error })
    }
    const states = await page.locator('lv-site-visual-example').evaluateAll((elements) => elements.map((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host')
      const canvas = host?.shadowRoot?.querySelector('canvas.maplibregl-canvas') as HTMLCanvasElement | null
      return {
        alert: host?.shadowRoot?.querySelector('[role="alert"]')?.textContent?.trim() ?? '',
        attribution: host?.shadowRoot?.querySelector('[data-map-attribution]')?.textContent?.trim() ?? '',
        width: canvas?.width ?? 0,
        height: canvas?.height ?? 0,
        rendererChildren: host?.shadowRoot?.querySelector('.renderer')?.childElementCount ?? 0,
      }
    }))
    expect(states.map(({ alert, attribution, rendererChildren }) => ({ alert, attribution, rendererChildren }))).toEqual([
      { alert: '', attribution: '© OpenStreetMap contributors · Instituto Brasileiro de Geografia e Estatística (IBGE)', rendererChildren: 1 },
      { alert: '', attribution: '© OpenStreetMap contributors', rendererChildren: 1 },
      { alert: '', attribution: '© OpenStreetMap contributors', rendererChildren: 1 },
      { alert: '', attribution: '© OpenStreetMap contributors', rendererChildren: 1 },
      { alert: '', attribution: 'Instituto Brasileiro de Geografia e Estatística (IBGE)', rendererChildren: 1 },
      { alert: '', attribution: '© OpenStreetMap contributors', rendererChildren: 1 },
    ])
    expect(await page.locator('lv-site-visual-example').evaluateAll((elements) => elements.map((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host')
      return {
        title: host?.shadowRoot?.querySelector('[data-visualization-title]')?.textContent?.trim(),
        expand: host?.shadowRoot?.querySelector('button')?.getAttribute('aria-label'),
      }
    }))).toEqual([
      { title: 'Orders by state', expand: 'Expand map' },
      { title: 'Order locations', expand: 'Expand map' },
      { title: 'Revenue concentration', expand: 'Expand map' },
      { title: 'Order density', expand: 'Expand map' },
      { title: 'Brazil state reference boundaries', expand: 'Expand map' },
      { title: 'State order paths', expand: 'Expand map' },
    ])
    expect(states.every(({ width, height }) => width > 500 && height >= 400)).toBe(true)
    expect(await page.evaluate(() => performance.getEntries().some(({ name }) => /\/map-assets\/leapview-streets\/styles\/[0-9a-f]{64}\/style\.json$/.test(name)))).toBe(true)
    expect(await page.locator('lv-site-visual-example').evaluateAll((elements) => elements.map((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host')
      const fallback = host?.shadowRoot?.querySelector('[data-map-data-table]')
      return {
        summary: fallback?.querySelector('summary')?.textContent?.trim() ?? '',
        columns: fallback?.querySelectorAll('thead th').length ?? 0,
        rows: fallback?.querySelectorAll('tbody tr').length ?? 0,
      }
    }))).toEqual([
      { summary: 'View map data (27 rows)', columns: 2, rows: 27 },
      { summary: 'View map data (0 features)', columns: 6, rows: 0 },
      { summary: 'View map data (0 features)', columns: 5, rows: 0 },
      { summary: 'View map data (0 features)', columns: 4, rows: 0 },
      { summary: 'View map data (27 rows)', columns: 2, rows: 27 },
      { summary: 'View map data (35 rows)', columns: 2, rows: 35 },
    ])

    expect(await page.locator('lv-site-visual-example').evaluateAll((elements) => elements.slice(0, 2).map((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { envelope?: any }
      return host.envelope?.spec?.presentation?.theme
    }))).toEqual(['auto', 'light'])

    const mapSnapshot = (exampleIndex: number) => page.locator('lv-site-visual-example').nth(exampleIndex).evaluate(async (element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement & { snapshot(): Promise<Blob> }
      const blob = await host.snapshot()
      const bitmap = await createImageBitmap(blob)
      const canvas = new OffscreenCanvas(bitmap.width, bitmap.height)
      const context = canvas.getContext('2d')!
      context.drawImage(bitmap, 0, 0)
      const pixels = context.getImageData(0, 0, bitmap.width, bitmap.height).data
      let visiblePixels = 0
      for (let index = 3; index < pixels.length; index += 4) if (pixels[index]! > 0) visiblePixels++
      return { corner: Array.from(pixels.slice(0, 4)), height: bitmap.height, size: blob.size, type: blob.type, visiblePixels, width: bitmap.width }
    })
    const snapshot = await mapSnapshot(0)
    expect(snapshot.type).toBe('image/png')
    expect(snapshot.size).toBeGreaterThan(0)
    expect(snapshot.visiblePixels).toBeGreaterThan(10_000)

    const applyTheme = async (mode: 'dark' | 'light') => {
      await page.evaluate((nextMode) => document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: nextMode } })), mode)
      await page.waitForFunction((nextMode) => document.documentElement.style.colorScheme === nextMode, mode)
    }
    await applyTheme('dark')
    await page.waitForTimeout(250)
    const darkSnapshot = await mapSnapshot(0)
    await applyTheme('light')
    await page.waitForTimeout(250)
    const lightSnapshot = await mapSnapshot(0)
    expect(darkSnapshot.corner).not.toEqual(lightSnapshot.corner)

    const pinnedLightSnapshot = await mapSnapshot(1)
    await applyTheme('dark')
    await page.waitForTimeout(250)
    expect((await mapSnapshot(1)).corner).toEqual(pinnedLightSnapshot.corner)

    await page.setViewportSize({ width: 390, height: 844 })
    await page.waitForFunction(() => document.querySelector('.site-docs-sidebar')?.getAttribute('aria-hidden') === 'true')
    await page.waitForTimeout(250)
    const mobile = await page.locator('lv-site-visual-example').evaluateAll((elements) => elements.map((element) => {
      const host = element.shadowRoot?.querySelector('lv-visualization-host')
      const canvas = host?.shadowRoot?.querySelector('canvas.maplibregl-canvas') as HTMLCanvasElement | null
      return {
        alert: host?.shadowRoot?.querySelector('[role="alert"]')?.textContent?.trim() ?? '',
        left: canvas?.getBoundingClientRect().left ?? -1,
        right: canvas?.getBoundingClientRect().right ?? Number.POSITIVE_INFINITY,
        width: canvas?.getBoundingClientRect().width ?? 0,
      }
    }))
    expect(mobile.every(({ alert, left, right, width }) => alert === '' && left >= 0 && right <= 390 && width >= 320)).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  } finally {
    await page.close()
  }
}, 60_000)

test('documentation articles apply the shared Markdown treatment', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/docs/visuals/line`)
    await page.waitForFunction(() => Boolean(document.querySelector('.site-docs-article lv-code-block .shiki')))
    const codeBlock = await page
      .locator('.site-docs-article lv-code-block .code-block-shell')
      .first()
      .evaluate((element) => {
        const style = getComputedStyle(element)
        const toolbar = element.querySelector('.code-block-toolbar') as HTMLElement
        return {
          borderTopWidth: style.borderTopWidth,
          borderRadius: style.borderRadius,
          toolbarHeight: toolbar.getBoundingClientRect().height,
        }
      })
    expect(codeBlock.borderTopWidth).toBe('1px')
    expect(codeBlock.borderRadius).not.toBe('0px')
    expect(codeBlock.toolbarHeight).toBe(33)

    await page.setViewportSize({ width: 390, height: 800 })
    const compactCodeBlock = await page
      .locator('.site-docs-article lv-code-block')
      .first()
      .evaluate((element) => {
        const article = element.closest('.site-docs-article') as HTMLElement
        const pre = element.querySelector('pre') as HTMLElement
        return {
          articleWidth: article.getBoundingClientRect().width,
          codeWidth: element.getBoundingClientRect().width,
          overflowX: getComputedStyle(pre).overflowX,
          pageOverflows: document.documentElement.scrollWidth > document.documentElement.clientWidth,
        }
      })
    expect(compactCodeBlock.codeWidth).toBe(compactCodeBlock.articleWidth)
    expect(compactCodeBlock.overflowX).toBe('auto')
    expect(compactCodeBlock.pageOverflows).toBe(false)

    await page.goto(`${baseURL}/docs/configuration`)
    const tableHeader = await page
      .locator('.site-docs-article th')
      .first()
      .evaluate((element) => getComputedStyle(element).backgroundColor)
    expect(tableHeader).not.toBe('rgba(0, 0, 0, 0)')

    const siteCSS = await (await fetch(`${baseURL}/static/site.css`)).text()
    expect(siteCSS).not.toContain('--lv-chat-')
  } finally {
    await page.close()
  }
})

test('documentation Mermaid fences render as accessible responsive diagrams', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/introduction`)
    const diagram = page.locator('lv-site-mermaid').first()
    await diagram.locator('svg').waitFor({ state: 'visible' })

    expect(await diagram.getAttribute('aria-label')).toBe('LeapView resource layers')
    expect(await diagram.locator('svg').getAttribute('role')).toBe('img')
    expect(await diagram.locator('svg title').textContent()).toBe('LeapView resource layers')
    expect(await page.locator('.site-docs-article lv-code-block[language="mermaid"]').count()).toBe(0)

    const desktop = await diagram.evaluate((element) => {
      const svg = element.shadowRoot?.querySelector('svg') as SVGElement
      return {
        diagramWidth: element.getBoundingClientRect().width,
        articleWidth: (element.closest('.site-docs-article') as HTMLElement).getBoundingClientRect().width,
        svgMaxWidth: getComputedStyle(svg).maxWidth,
      }
    })
    expect(desktop.diagramWidth).toBe(desktop.articleWidth)
    expect(desktop.svgMaxWidth).toBe('100%')

    await page.setViewportSize({ width: 390, height: 800 })
    expect(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)).toBe(false)

    await page.evaluate(() => document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: 'dark' } })))
    await page.waitForFunction(() => document.querySelector('html')?.getAttribute('data-color-mode') === 'dark')
    await page.waitForFunction(() => document.querySelector('lv-site-mermaid')?.getAttribute('data-rendered-theme') === 'dark')
  } finally {
    await page.close()
  }
})

test('documentation articles provide a readable, navigable reference experience', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/guides/build/dashboard`)
    await page.evaluate(() => {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: {
          writeText: async (value: string) => {
            document.documentElement.dataset.copiedCode = value
          },
        },
      })
    })

    const typography = await page.locator('.site-docs-article').evaluate((article) => {
      const paragraph = article.querySelector('p') as HTMLElement
      const orderedList = article.querySelector('ol') as HTMLElement
      const unorderedList = article.querySelector('ul') as HTMLElement
      const heading = article.querySelector('h1') as HTMLElement
      const action = article.querySelector('lv-site-markdown-copy') as HTMLElement
      const code = article.querySelector('pre code') as HTMLElement
      const navigation = document.querySelector('.site-docs-link') as HTMLElement
      return {
        articleWidth: article.getBoundingClientRect().width,
        codeFontSize: Number.parseFloat(getComputedStyle(code).fontSize),
        headingFontSize: Number.parseFloat(getComputedStyle(heading).fontSize),
        headingLineHeight: Number.parseFloat(getComputedStyle(heading).lineHeight),
        navigationFontSize: Number.parseFloat(getComputedStyle(navigation).fontSize),
        paragraphWidth: paragraph.getBoundingClientRect().width,
        paragraphFontSize: Number.parseFloat(getComputedStyle(paragraph).fontSize),
        paragraphLineHeight: Number.parseFloat(getComputedStyle(paragraph).lineHeight),
        paragraphColor: getComputedStyle(paragraph).color,
        articleColor: getComputedStyle(article).color,
        orderedListStyle: getComputedStyle(orderedList).listStyleType,
        unorderedListStyle: getComputedStyle(unorderedList).listStyleType,
        headingRight: heading.getBoundingClientRect().right,
        actionLeft: action.getBoundingClientRect().left,
      }
    })
    expect(typography.headingFontSize).toBe(36)
    expect(typography.headingLineHeight / typography.headingFontSize).toBeCloseTo(1.2, 2)
    expect(typography.paragraphFontSize).toBe(16)
    expect(typography.paragraphLineHeight / typography.paragraphFontSize).toBeCloseTo(1.65, 2)
    expect(typography.codeFontSize).toBe(13)
    expect(typography.navigationFontSize).toBe(13)
    expect(typography.paragraphColor).toBe(typography.articleColor)
    expect(typography.orderedListStyle).toBe('decimal')
    expect(typography.unorderedListStyle).toBe('disc')
    expect(typography.paragraphWidth).toBeGreaterThanOrEqual(620)
    expect(Math.abs(typography.articleWidth - typography.paragraphWidth)).toBeLessThanOrEqual(1)
    expect(typography.actionLeft).toBeGreaterThanOrEqual(typography.headingRight)

    expect(await page.locator('.site-docs-callout[data-callout="tip"]').count()).toBe(1)
    expect(await page.locator('.site-docs-callout-label').getByText('Tip', { exact: true }).isVisible()).toBe(true)
    await page.waitForFunction(() => Boolean(document.querySelector('.site-docs-article lv-code-block .shiki')))
    const codeBlock = page.locator('.site-docs-article lv-code-block[language="sh"]').first()
    expect(await codeBlock.getAttribute('language')).toBe('sh')
    expect(await codeBlock.getAttribute('toolbar')).not.toBeNull()
    expect(await codeBlock.locator('.shiki').getAttribute('class')).toContain('github-light')
    expect(await codeBlock.getByText('Shell', { exact: true }).isVisible()).toBe(true)
    await codeBlock.getByRole('button', { name: 'Copy code' }).click()
    await page.waitForFunction(() => document.documentElement.dataset.copiedCode === 'leapview validate --source-root dashboards\nleapview plan --source-root dashboards\n')
    expect(await codeBlock.getByRole('button', { name: 'Code copied' }).isVisible()).toBe(true)

    const activeGroup = page.locator('.site-docs-nav-group-active > summary').first()
    const currentLink = page.locator('.site-docs-link-current')
    const navigationTreatment = await activeGroup.evaluate(
      (summary, link) => ({
        groupBackground: getComputedStyle(summary).backgroundColor,
        linkBackground: getComputedStyle(link as Element).backgroundColor,
      }),
      await currentLink.elementHandle(),
    )
    expect(navigationTreatment.groupBackground).toBe('rgba(0, 0, 0, 0)')
    expect(navigationTreatment.linkBackground).not.toBe(navigationTreatment.groupBackground)

    const search = page.locator('lv-site-search')
    await search.getByRole('button', { name: 'Search documentation' }).click()
    expect(await search.getByRole('dialog', { name: 'Search documentation' }).isVisible()).toBe(true)
    const searchInput = search.locator('input[slot="input"]')
    await page.waitForFunction(() => document.activeElement?.matches('lv-site-search input[slot="input"]'))
    expect(await searchInput.getAttribute('data-bind')).toBe('docsSearch.query')
    expect(await searchInput.getAttribute('data-on:input__debounce.200ms')).toBe("@get('/docs/search/active', {filterSignals: {include: /^docsSearch\\./}})")
    await searchInput.fill('semantic relationships')
    const semanticModelsResult = search.locator('a[href="/docs/concepts/semantic-models"]')
    await semanticModelsResult.waitFor({ state: 'visible' })
    expect(await semanticModelsResult.isVisible()).toBe(true)
    expect(page.url()).toBe(`${baseURL}/docs/guides/build/dashboard`)
    const resultCount = await search.locator('.status').innerText()
    expect(resultCount).toMatch(/^[1-9]\d* results$/)
    const emptyQuery = 'no-document-can-match-this-query-9f83c1'
    const expectedEmptyStatus = `No results for “${emptyQuery}”.`
    const emptySearchResponse = page.waitForResponse((response) => {
      const responseURL = new URL(response.url())
      if (responseURL.pathname !== '/docs/search/active' || !response.ok()) return false
      const encodedSignals = responseURL.searchParams.get('datastar')
      if (!encodedSignals) return false
      try {
        const signals = JSON.parse(encodedSignals) as { docsSearch?: { query?: string } }
        return signals.docsSearch?.query === emptyQuery
      } catch {
        return false
      }
    })
    await searchInput.fill(emptyQuery)
    expect(await (await emptySearchResponse).finished()).toBeNull()
    const emptyStatus = search.locator('.status')
    await page.waitForFunction((expected) => {
      const shadowRoot = document.querySelector('lv-site-search')?.shadowRoot
      return shadowRoot?.querySelector('.status')?.textContent === expected && shadowRoot?.querySelector('.results')?.getAttribute('aria-busy') === 'false'
    }, expectedEmptyStatus)
    expect(await emptyStatus.innerText()).toBe(expectedEmptyStatus)
    expect(await emptyStatus.getAttribute('role')).toBe('status')
    await search.getByRole('button', { name: 'Close search' }).click()
    await page.keyboard.press('/')
    expect(await search.getByRole('dialog', { name: 'Search documentation' }).isVisible()).toBe(true)
  } finally {
    await page.close()
  }
})

test('documentation navigation follows DuckDBs 900px drawer breakpoint', async () => {
  const page = await browser.newPage({ viewport: { width: 901, height: 900 } })
  try {
    await page.goto(`${baseURL}/docs/guides/build`)
    const sidebar = page.locator('.site-docs-sidebar')
    expect(await sidebar.evaluate((element) => getComputedStyle(element).position)).toBe('sticky')
    expect(await sidebar.getAttribute('aria-hidden')).toBe('false')
    expect(await page.getByRole('button', { name: 'Open documentation menu' }).isVisible()).toBe(false)

    await page.setViewportSize({ width: 900, height: 900 })
    await page.waitForFunction(() => document.querySelector('.site-docs-sidebar')?.getAttribute('aria-hidden') === 'true')
    expect(await sidebar.evaluate((element) => getComputedStyle(element).position)).toBe('fixed')
    expect(await sidebar.getAttribute('aria-hidden')).toBe('true')
    expect(await page.getByRole('button', { name: 'Open documentation menu' }).isVisible()).toBe(true)
    const widths = await page.locator('.site-guide-shell').evaluate((shell) => ({
      article: shell.querySelector('.site-docs-article')?.getBoundingClientRect().width ?? 0,
      shell: shell.getBoundingClientRect().width,
    }))
    expect(Math.abs(widths.shell - widths.article)).toBeLessThanOrEqual(1)

    await page.setViewportSize({ width: 390, height: 844 })
    const hierarchy = await page.locator('.site-docs-article').evaluate((article) => ({
      h1: Number.parseFloat(getComputedStyle(article.querySelector('h1') as Element).fontSize),
      h2: Number.parseFloat(getComputedStyle(article.querySelector('h2') as Element).fontSize),
    }))
    expect(hierarchy.h1).toBeGreaterThan(hierarchy.h2)
  } finally {
    await page.close()
  }
})

test('documentation navigation uses compact rows and Overview labels', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/guides/build`)
    const navigation = page.getByRole('navigation', { name: 'Documentation' })
    const overview = navigation.locator('a[href="/docs/guides/build"]')
    expect(await overview.innerText()).toBe('Overview')
    expect(await overview.getAttribute('title')).toBe('Overview')
    for (const href of ['/docs/data-ingestion', '/docs/guides/operate', '/docs/enterprise-auth', '/docs/integrate', '/docs/config', '/docs/architecture', '/docs/contributing/repository']) {
      const sectionOverview = navigation.locator(`a[href="${href}"]`)
      expect(await sectionOverview.getAttribute('title')).toBe('Overview')
    }
    expect(await navigation.locator('details[data-site-docs-group="architecture-architecture"]').count()).toBe(1)
    expect(await navigation.locator('details[data-site-docs-group="architecture-contributing"]').count()).toBe(1)
    const projectsLink = navigation.locator('a[href="/docs/concepts/projects-environments"]')
    expect(await projectsLink.count()).toBe(1)
    expect(await projectsLink.textContent()).toBe('Projects and environments')
    expect(await navigation.getByRole('link', { name: 'Build dashboards', exact: true }).count()).toBe(0)
    expect(await page.getByRole('heading', { name: 'Build dashboards', exact: true }).isVisible()).toBe(true)

    const metrics = await overview.evaluate((link) => {
      const summary = link.closest('details')?.querySelector(':scope > summary') as HTMLElement
      const summaryLabel = summary.querySelector('.site-docs-nav-label') as HTMLElement
      const sidebar = link.closest('.site-docs-sidebar') as HTMLElement
      const linkStyle = getComputedStyle(link)
      const summaryStyle = getComputedStyle(summary)
      const summaryLabelStyle = getComputedStyle(summaryLabel)
      const sidebarStyle = getComputedStyle(sidebar)
      return {
        linkHeight: link.getBoundingClientRect().height,
        linkOverflow: linkStyle.overflow,
        linkPaddingBlock: Number.parseFloat(linkStyle.paddingTop),
        linkTextOverflow: linkStyle.textOverflow,
        linkWhiteSpace: linkStyle.whiteSpace,
        scrollbarGutter: sidebarStyle.scrollbarGutter,
        scrollbarWidth: sidebarStyle.scrollbarWidth,
        summaryHeight: summary.getBoundingClientRect().height,
        summaryLabelOverflow: summaryLabelStyle.overflow,
        summaryLabelTextOverflow: summaryLabelStyle.textOverflow,
        summaryLabelWhiteSpace: summaryLabelStyle.whiteSpace,
        summaryPaddingBlock: Number.parseFloat(summaryStyle.paddingTop),
      }
    })
    expect(metrics.linkHeight).toBe(28)
    expect(metrics.summaryHeight).toBe(28)
    expect(metrics.linkPaddingBlock).toBe(4)
    expect(metrics.summaryPaddingBlock).toBe(4)
    expect(metrics.linkOverflow).toBe('hidden')
    expect(metrics.linkTextOverflow).toBe('ellipsis')
    expect(metrics.linkWhiteSpace).toBe('nowrap')
    expect(metrics.summaryLabelOverflow).toBe('hidden')
    expect(metrics.summaryLabelTextOverflow).toBe('ellipsis')
    expect(metrics.summaryLabelWhiteSpace).toBe('nowrap')
    expect(metrics.scrollbarGutter).toBe('stable')
    expect(metrics.scrollbarWidth).toBe('thin')
  } finally {
    await page.close()
  }
})

test('documentation reading columns stay centered and readable at every layout tier', async () => {
  const page = await browser.newPage({
    viewport: { width: 1600, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/introduction`)

    const measure = () =>
      page.locator('.site-docs-reading-layout').evaluate((reading) => {
        const content = document.querySelector('.site-docs-content') as HTMLElement
        const shell = reading.querySelector('.site-guide-shell') as HTMLElement
        const article = reading.querySelector('.site-docs-article') as HTMLElement
        const paragraph = article.querySelector('p') as HTMLElement
        const outline = reading.querySelector('lv-site-article-toc') as HTMLElement
        const contentRect = content.getBoundingClientRect()
        const contentStyle = getComputedStyle(content)
        const readingRect = reading.getBoundingClientRect()
        const articleRect = article.getBoundingClientRect()
        const paragraphRect = paragraph.getBoundingClientRect()
        const shellRect = shell.getBoundingClientRect()
        const outlineRect = outline.getBoundingClientRect()
        const sectionHeading = article.querySelector('h2') as HTMLElement
        const precedingBlock = sectionHeading.previousElementSibling as HTMLElement
        return {
          articleLeftSpace: articleRect.left - shellRect.left,
          articleRightSpace: shellRect.right - articleRect.right,
          articleWidth: articleRect.width,
          outlineVisible: getComputedStyle(outline).display !== 'none',
          outlineRightSpace: contentRect.right - Number.parseFloat(contentStyle.paddingRight) - outlineRect.right,
          paragraphWidth: paragraphRect.width,
          readingLeftSpace: readingRect.left - (contentRect.left + Number.parseFloat(contentStyle.paddingLeft)),
          readingRightSpace: contentRect.right - Number.parseFloat(contentStyle.paddingRight) - readingRect.right,
          sectionGap: sectionHeading.getBoundingClientRect().top - precedingBlock.getBoundingClientRect().bottom,
          shellWidth: shellRect.width,
        }
      })

    const wide = await measure()
    expect(wide.outlineVisible).toBe(true)
    expect(Math.abs(wide.outlineRightSpace)).toBeLessThanOrEqual(1)
    expect(Math.abs(wide.readingLeftSpace - wide.readingRightSpace)).toBeLessThanOrEqual(1)
    expect(wide.articleWidth).toBeGreaterThanOrEqual(1000)
    expect(wide.articleWidth).toBeLessThanOrEqual(1024)
    expect(Math.abs(wide.paragraphWidth - wide.articleWidth)).toBeLessThanOrEqual(1)
    expect(wide.sectionGap).toBeGreaterThanOrEqual(40)
    expect(wide.sectionGap).toBeLessThanOrEqual(60)

    await page.setViewportSize({ width: 1201, height: 900 })
    const withOutline = await measure()
    expect(withOutline.outlineVisible).toBe(true)
    expect(Math.abs(withOutline.outlineRightSpace)).toBeLessThanOrEqual(1)
    expect(Math.abs(withOutline.readingLeftSpace - withOutline.readingRightSpace)).toBeLessThanOrEqual(1)
    expect(withOutline.articleWidth).toBeGreaterThan(600)
    expect(withOutline.articleWidth).toBeLessThan(800)
    expect(Math.abs(withOutline.paragraphWidth - withOutline.articleWidth)).toBeLessThanOrEqual(1)

    await page.setViewportSize({ width: 1200, height: 900 })
    const desktop = await measure()
    expect(desktop.outlineVisible).toBe(false)
    expect(Math.abs(desktop.articleLeftSpace - desktop.articleRightSpace)).toBeLessThanOrEqual(1)
    expect(desktop.articleWidth).toBeGreaterThan(816)
    expect(desktop.articleWidth).toBeLessThanOrEqual(1024)
    expect(Math.abs(desktop.paragraphWidth - desktop.articleWidth)).toBeLessThanOrEqual(1)

    await page.setViewportSize({ width: 768, height: 900 })
    const tablet = await measure()
    expect(tablet.outlineVisible).toBe(false)
    expect(Math.abs(tablet.articleLeftSpace - tablet.articleRightSpace)).toBeLessThanOrEqual(1)
    expect(Math.abs(tablet.articleWidth - tablet.shellWidth)).toBeLessThanOrEqual(1)
    expect(Math.abs(tablet.paragraphWidth - tablet.articleWidth)).toBeLessThanOrEqual(1)

    await page.setViewportSize({ width: 390, height: 844 })
    const mobile = await measure()
    expect(mobile.outlineVisible).toBe(false)
    expect(Math.abs(mobile.articleLeftSpace - mobile.articleRightSpace)).toBeLessThanOrEqual(1)
    expect(Math.abs(mobile.articleWidth - mobile.shellWidth)).toBeLessThanOrEqual(1)
    expect(Math.abs(mobile.paragraphWidth - mobile.articleWidth)).toBeLessThanOrEqual(1)
  } finally {
    await page.close()
  }
})

test('documentation CSS keeps site tokens available and fragment targets below the sticky header', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/getting-started`)
    const runtimeStyles = await page.locator('.site-docs-article').evaluate((article) => ({
      articleWidth: article.getBoundingClientRect().width,
      shellWidth: article.closest('.site-guide-shell')?.getBoundingClientRect().width ?? 0,
      readingWidth: getComputedStyle(document.documentElement).getPropertyValue('--site-reading-width').trim(),
    }))
    expect(runtimeStyles.readingWidth).not.toBe('')
    expect(Math.abs(runtimeStyles.articleWidth - runtimeStyles.shellWidth)).toBeLessThanOrEqual(1)
    expect(runtimeStyles.articleWidth).toBeLessThanOrEqual(1024)

    await page.getByRole('navigation', { name: 'In this article' }).getByRole('link', { name: 'What you will learn' }).click()
    await page.waitForFunction(() => location.hash === '#what-you-will-learn')
    const anchorPosition = await page.locator('#what-you-will-learn').evaluate((heading) => ({
      headingTop: heading.getBoundingClientRect().top,
      headerBottom: document.querySelector('.site-header')?.getBoundingClientRect().bottom ?? 0,
    }))
    expect(anchorPosition.headingTop).toBeGreaterThan(anchorPosition.headerBottom)
  } finally {
    await page.close()
  }
})

test('site disables smooth scrolling for reduced motion', async () => {
  const page = await browser.newPage()
  try {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto(`${baseURL}/docs/getting-started`)
    expect(await page.locator('html').evaluate((element) => getComputedStyle(element).scrollBehavior)).toBe('auto')
  } finally {
    await page.close()
  }
})

test('documentation header keeps the Markdown copy action beside the title at every width', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/configuration`)

    const measure = () =>
      page.locator('.site-docs-article').evaluate((article) => {
        const button = document.querySelector('lv-site-markdown-copy')?.shadowRoot?.querySelector('button')
        const title = article.querySelector('h1')
        const action = article.querySelector('.site-docs-article-actions')
        const buttonStyle = button ? getComputedStyle(button) : null
        const titleRect = title?.getBoundingClientRect()
        const actionRect = action?.getBoundingClientRect()
        const buttonRect = button?.getBoundingClientRect()
        return {
          actionTop: actionRect?.top ?? 0,
          buttonFontSize: Number.parseFloat(buttonStyle?.fontSize ?? '0'),
          buttonHeight: buttonRect?.height ?? 0,
          buttonLeft: buttonRect?.left ?? 0,
          buttonRight: buttonRect?.right ?? 0,
          pageWidth: document.documentElement.scrollWidth,
          titleBottom: titleRect?.bottom ?? 0,
          titleLeft: titleRect?.left ?? 0,
          titleRight: titleRect?.right ?? 0,
          titleTop: titleRect?.top ?? 0,
          viewportWidth: window.innerWidth,
        }
      })

    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height: 900 })
      const layout = await measure()
      expect(layout.buttonFontSize).toBe(12)
      expect(layout.buttonHeight).toBe(33)
      expect(layout.buttonLeft).toBeGreaterThanOrEqual(layout.titleRight)
      expect(layout.actionTop).toBeGreaterThanOrEqual(layout.titleTop)
      expect(layout.actionTop).toBeLessThan(layout.titleBottom)
      expect(layout.buttonRight).toBeLessThanOrEqual(layout.viewportWidth)
      expect(layout.pageWidth).toBeLessThanOrEqual(layout.viewportWidth)
    }
  } finally {
    await page.close()
  }
})

test('documentation articles end with responsive pagination cards and an About this page panel', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/getting-started`)
    const article = page.locator('.site-docs-article')
    const pagination = article.getByRole('navigation', { name: 'Documentation pagination' })
    const panel = article.locator('.site-docs-page-meta')
    const previous = pagination.getByRole('link', { name: 'Previous page: Installation' })
    const next = pagination.getByRole('link', { name: 'Next page: Build your first dashboard' })
    expect(await previous.getAttribute('href')).toBe('/docs/installation')
    expect(await previous.getAttribute('rel')).toBe('prev')
    expect(await next.getAttribute('href')).toBe('/docs/first-dashboard')
    expect(await next.getAttribute('rel')).toBe('next')
    expect(await panel.getByRole('heading', { name: 'About this page', exact: true }).count()).toBe(1)
    expect(await panel.getByRole('link', { name: 'Report content issue', exact: true }).getAttribute('href')).toContain('github.com/flidai/leapview/issues/new?')
    expect(await panel.getByRole('link', { name: 'See this page as Markdown', exact: true }).getAttribute('href')).toBe('https://raw.githubusercontent.com/flidai/leapview/main/docs/getting-started.md')
    expect(await panel.getByRole('link', { name: 'Edit this page on GitHub', exact: true }).getAttribute('href')).toBe('https://github.com/flidai/leapview/edit/main/docs/getting-started.md')

    const measure = () =>
      pagination.evaluate((element) => {
        const article = element.closest('.site-docs-article') as HTMLElement
        const previous = element.querySelector<HTMLElement>('.site-docs-pagination-previous')!
        const next = element.querySelector<HTMLElement>('.site-docs-pagination-next')!
        const panel = article.querySelector<HTMLElement>('.site-docs-page-meta')!
        const previousRect = previous.getBoundingClientRect()
        const nextRect = next.getBoundingClientRect()
        const paginationRect = element.getBoundingClientRect()
        const panelRect = panel.getBoundingClientRect()
        const heading = panel.querySelector('h2') as HTMLElement
        const list = panel.querySelector('ul') as HTMLElement
        const item = panel.querySelector('li') as HTMLElement
        const headingRect = heading.getBoundingClientRect()
        const listRect = list.getBoundingClientRect()
        const panelStyle = getComputedStyle(panel)
        const headingStyle = getComputedStyle(heading)
        const listStyle = getComputedStyle(list)
        const itemStyle = getComputedStyle(item)
        return {
          articleWidth: article.getBoundingClientRect().width,
          background: panelStyle.backgroundColor,
          borderRadius: Number.parseFloat(panelStyle.borderRadius),
          headingFontSize: Number.parseFloat(headingStyle.fontSize),
          headingLineHeight: Number.parseFloat(headingStyle.lineHeight),
          headingMarginBottom: Number.parseFloat(headingStyle.marginBottom),
          headingLeft: headingRect.left,
          headingBottom: headingRect.bottom,
          itemFontSize: Number.parseFloat(itemStyle.fontSize),
          itemLineHeight: Number.parseFloat(itemStyle.lineHeight),
          listStyle: listStyle.listStyleType,
          listLeft: listRect.left,
          listTop: listRect.top,
          marginTop: Number.parseFloat(panelStyle.marginTop),
          nextLeft: nextRect.left,
          nextTop: nextRect.top,
          padding: Number.parseFloat(panelStyle.paddingTop),
          paddingLeft: Number.parseFloat(listStyle.paddingLeft),
          paginationBottom: paginationRect.bottom,
          panelTop: panelRect.top,
          panelWidth: panelRect.width,
          previousLeft: previousRect.left,
          previousTop: previousRect.top,
        }
      })

    const desktop = await measure()
    expect(desktop.background).not.toBe('rgba(0, 0, 0, 0)')
    expect(desktop.borderRadius).toBe(6)
    expect(desktop.padding).toBe(20)
    expect(desktop.headingFontSize).toBe(14)
    expect(desktop.headingLineHeight / desktop.headingFontSize).toBeCloseTo(1.2, 2)
    expect(desktop.headingMarginBottom).toBe(7)
    expect(desktop.listTop).toBeGreaterThan(desktop.headingBottom)
    expect(desktop.listLeft).toBe(desktop.headingLeft)
    expect(desktop.itemFontSize).toBe(14)
    expect(desktop.itemLineHeight / desktop.itemFontSize).toBeCloseTo(1.4, 2)
    expect(desktop.listStyle).toBe('disc')
    expect(desktop.marginTop).toBe(0)
    expect(desktop.paddingLeft).toBe(20)
    expect(Math.abs(desktop.panelWidth - desktop.articleWidth)).toBeLessThanOrEqual(1)
    expect(desktop.previousTop).toBe(desktop.nextTop)
    expect(desktop.previousLeft).toBeLessThan(desktop.nextLeft)
    expect(desktop.paginationBottom).toBeLessThan(desktop.panelTop)

    await page.setViewportSize({ width: 390, height: 844 })
    const mobile = await measure()
    expect(mobile.padding).toBe(20)
    expect(mobile.listTop).toBeGreaterThan(mobile.headingBottom)
    expect(mobile.listLeft).toBe(mobile.headingLeft)
    expect(Math.abs(mobile.panelWidth - mobile.articleWidth)).toBeLessThanOrEqual(1)
    expect(mobile.previousLeft).toBe(mobile.nextLeft)
    expect(mobile.previousTop).toBeLessThan(mobile.nextTop)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  } finally {
    await page.close()
  }
})

test('compact documentation navigation opens in a drawer', async () => {
  const context = await browser.newContext({
    hasTouch: true,
    viewport: { width: 640, height: 900 },
  })
  const page = await context.newPage()
  try {
    await page.addInitScript(() => {
      const calls: Array<{
        block?: ScrollLogicalPosition
        href: string | null
        inline?: ScrollLogicalPosition
      }> = []
      ;(window as unknown as { siteDocsRevealCalls: typeof calls }).siteDocsRevealCalls = calls
      Element.prototype.scrollIntoView = function scrollIntoView(options?: boolean | ScrollIntoViewOptions) {
        const normalized = typeof options === 'object' ? options : {}
        calls.push({
          block: normalized.block,
          href: this.getAttribute('href'),
          inline: normalized.inline,
        })
      }
    })
    await page.goto(`${baseURL}/docs/getting-started`)
    await page.waitForFunction(() =>
      (
        window as unknown as {
          siteDocsRevealCalls: Array<{ href: string | null }>
        }
      ).siteDocsRevealCalls.some((call) => call.href === '/docs/getting-started'),
    )

    const sidebar = page.locator('.site-docs-sidebar')
    const headerDrawerToggle = page.locator('lv-site-docs-drawer-toggle:not([placement])')
    const toggle = page.getByRole('button', {
      name: 'Open documentation menu',
    })
    expect(await toggle.isVisible()).toBe(true)
    expect(await toggle.evaluate((element) => element.getBoundingClientRect().height)).toBeGreaterThanOrEqual(44)
    expect(await toggle.getAttribute('aria-expanded')).toBe('false')
    expect(await sidebar.getAttribute('aria-hidden')).toBe('true')
    const revealCount = await page.evaluate(() => (window as unknown as { siteDocsRevealCalls: unknown[] }).siteDocsRevealCalls.length)

    await toggle.click()
    await page.waitForFunction(() => document.querySelector('.site-docs-layout')?.classList.contains('site-docs-drawer-open'))
    await page.waitForFunction((previousCount) => (window as unknown as { siteDocsRevealCalls: unknown[] }).siteDocsRevealCalls.length > previousCount, revealCount)
    expect(await headerDrawerToggle.evaluate((element) => element.shadowRoot?.querySelector('button')?.getAttribute('aria-expanded'))).toBe('true')
    expect(await sidebar.getAttribute('aria-hidden')).toBe('false')
    expect(await page.locator('.site-header').evaluate((element) => (element as HTMLElement).inert)).toBe(true)
    expect(await page.locator('.site-docs-content').evaluate((element) => (element as HTMLElement).inert)).toBe(true)
    const drawerToggle = page.locator('lv-site-docs-drawer-toggle[placement="drawer"]')
    await page.waitForFunction(() =>
      document.querySelector<HTMLElement>('lv-site-docs-drawer-toggle[placement="drawer"]')?.shadowRoot?.activeElement?.matches('button'),
    )
    await page.keyboard.press('Shift+Tab')
    expect(await page.evaluate(() => document.querySelector('.site-docs-sidebar')?.contains(document.activeElement))).toBe(true)
    expect(
      await sidebar
        .locator('.site-docs-link')
        .first()
        .evaluate((element) => element.getBoundingClientRect().height),
    ).toBeGreaterThanOrEqual(44)
    expect(
      await page.evaluate(() =>
        (
          window as unknown as {
            siteDocsRevealCalls: Array<{
              block?: string
              href: string | null
              inline?: string
            }>
          }
        ).siteDocsRevealCalls.at(-1),
      ),
    ).toEqual({
      block: 'nearest',
      href: '/docs/getting-started',
      inline: 'nearest',
    })
    expect(await sidebar.evaluate((element) => getComputedStyle(element).transitionDuration)).not.toBe('0s')

    await drawerToggle.getByRole('button', { name: 'Close documentation menu' }).click()
    await page.waitForFunction(() => !document.querySelector('.site-docs-layout')?.classList.contains('site-docs-drawer-open'))
    expect(await headerDrawerToggle.evaluate((element) => element.shadowRoot?.querySelector('button')?.getAttribute('aria-expanded'))).toBe('false')
  } finally {
    await context.close()
  }
})

test('documentation navigation preserves sidebar context within the current tab', async () => {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 600 },
  })
  const page = await context.newPage()
  try {
    await page.goto(`${baseURL}/docs/visuals/line`)
    const sidebar = page.locator('.site-docs-sidebar')
    const current = sidebar.locator('a[href="/docs/visuals/line"]')
    await page.evaluate(() => sessionStorage.removeItem('leapview:docs-sidebar-scroll:v1'))
    await sidebar.evaluate((element, currentElement) => {
      const currentLink = currentElement as HTMLElement
      const sidebarRect = element.getBoundingClientRect()
      const currentRect = currentLink.getBoundingClientRect()
      element.scrollTop += currentRect.top - sidebarRect.top - element.clientHeight / 2
      element.dispatchEvent(new Event('scroll'))
    }, await current.elementHandle())
    await page.waitForFunction(() => sessionStorage.getItem('leapview:docs-sidebar-scroll:v1') !== null)

    const saved = await page.evaluate(() => JSON.parse(sessionStorage.getItem('leapview:docs-sidebar-scroll:v1') ?? 'null') as {
      anchor: { id: string; kind: 'group' | 'link'; offset: number }
      scrollTop: number
    })
    expect(saved.scrollTop).toBeGreaterThan(0)
    expect(saved.anchor.id).not.toBe('')

    await page.goto(`${baseURL}/docs/visuals/area`)
    await page.waitForFunction(() => document.querySelector('.site-docs-link-current')?.getAttribute('href') === '/docs/visuals/area')
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))

    const restored = await sidebar.evaluate((element) => {
      const currentLink = element.querySelector<HTMLElement>('.site-docs-link-current')
      if (!currentLink) throw new Error('restored documentation navigation target is missing')
      const sidebarRect = element.getBoundingClientRect()
      const currentRect = currentLink.getBoundingClientRect()
      return {
        activeVisible: currentRect.top >= sidebarRect.top && currentRect.bottom <= sidebarRect.bottom,
        scrollTop: element.scrollTop,
      }
    })
    expect(restored.scrollTop).toBeGreaterThan(0)
    expect(restored.activeVisible).toBe(true)
  } finally {
    await context.close()
  }
})

test('documentation outlines match the compact DuckDB article navigation treatment', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/guides/build/models`)
    const toc = page.locator('lv-site-article-toc')
    expect(await toc.locator('a[data-level="2"]').count()).toBeGreaterThanOrEqual(2)
    expect(await toc.locator('a[data-level="3"]').count()).toBeGreaterThanOrEqual(2)
    const tocTreatment = await toc.evaluate((element) => {
      const root = element.shadowRoot?.querySelector<HTMLElement>('ul#toc')
      const nested = root?.querySelector<HTMLElement>(':scope > li > ul')
      const heading = element.shadowRoot?.querySelector<HTMLElement>('nav > h2')
      const major = root?.querySelector<HTMLElement>(':scope > li > a[data-level="2"]')
      const subsection = nested?.querySelector<HTMLElement>(':scope > li > a[data-level="3"]')
      const active = root?.querySelector<HTMLElement>('a.active')
      const inactive = root?.querySelector<HTMLElement>('a:not(.active)')
      const headingStyle = heading ? getComputedStyle(heading) : null
      const rootStyle = root ? getComputedStyle(root) : null
      const nestedStyle = nested ? getComputedStyle(nested) : null
      const majorStyle = major ? getComputedStyle(major) : null
      const subsectionStyle = subsection ? getComputedStyle(subsection) : null
      const activeStyle = active ? getComputedStyle(active) : null
      const inactiveStyle = inactive ? getComputedStyle(inactive) : null
      return {
        activeColor: activeStyle?.color,
        activeWeight: activeStyle?.fontWeight,
        headingFontSize: Number.parseFloat(headingStyle?.fontSize ?? '0'),
        headingLetterSpacing: Number.parseFloat(headingStyle?.letterSpacing ?? '0'),
        headingLineHeight: Number.parseFloat(headingStyle?.lineHeight ?? '0'),
        headingMarginLeft: Number.parseFloat(headingStyle?.marginLeft ?? '0'),
        headingTransform: headingStyle?.textTransform,
        hostOverflow: getComputedStyle(element).overflow,
        hostPosition: getComputedStyle(element).position,
        inactiveColor: inactiveStyle?.color,
        inactiveWeight: inactiveStyle?.fontWeight,
        majorBorderRadius: Number.parseFloat(majorStyle?.borderRadius ?? '0'),
        majorFontSize: Number.parseFloat(majorStyle?.fontSize ?? '0'),
        majorLineHeight: Number.parseFloat(majorStyle?.lineHeight ?? '0'),
        majorPaddingBlock: Number.parseFloat(majorStyle?.paddingTop ?? '0'),
        majorPaddingInline: Number.parseFloat(majorStyle?.paddingLeft ?? '0'),
        nestedBorderLeftWidth: nestedStyle?.borderLeftWidth,
        nestedIndent: nested && root ? nested.getBoundingClientRect().left - root.getBoundingClientRect().left : 0,
        rootListStyle: rootStyle?.listStyleType,
        rootMarginTop: Number.parseFloat(rootStyle?.marginTop ?? '0'),
        subsectionFontSize: Number.parseFloat(subsectionStyle?.fontSize ?? '0'),
        subsectionOffset: subsection && major ? subsection.getBoundingClientRect().left - major.getBoundingClientRect().left : 0,
      }
    })
    expect(tocTreatment.hostPosition).toBe('sticky')
    expect(tocTreatment.hostOverflow).toBe('auto')
    expect(tocTreatment.headingFontSize).toBe(12)
    expect(tocTreatment.headingLineHeight / tocTreatment.headingFontSize).toBeCloseTo(1.2, 2)
    expect(tocTreatment.headingLetterSpacing).toBeCloseTo(0.36, 2)
    expect(tocTreatment.headingMarginLeft).toBe(12)
    expect(tocTreatment.headingTransform).toBe('uppercase')
    expect(tocTreatment.rootListStyle).toBe('none')
    expect(tocTreatment.rootMarginTop).toBe(15)
    expect(tocTreatment.majorFontSize).toBe(12)
    expect(tocTreatment.subsectionFontSize).toBe(12)
    expect(tocTreatment.majorLineHeight).toBe(12)
    expect(tocTreatment.majorPaddingBlock).toBe(6)
    expect(tocTreatment.majorPaddingInline).toBe(12)
    expect(tocTreatment.majorBorderRadius).toBeGreaterThan(1000)
    expect(tocTreatment.nestedBorderLeftWidth).toBe('1px')
    expect(tocTreatment.nestedIndent).toBe(15)
    expect(tocTreatment.subsectionOffset).toBe(16)
    expect(tocTreatment.activeColor).not.toBe(tocTreatment.inactiveColor)
    expect(tocTreatment.activeWeight).toBe(tocTreatment.inactiveWeight)

    const articleHierarchy = await page.locator('.site-docs-article').evaluate((article) => {
      const generatedHeadings = ['h4', 'h5', 'h6'].map((tagName) => {
        const heading = document.createElement(tagName)
        heading.textContent = tagName
        article.append(heading)
        return heading
      })
      const sizes = {
        h2: Number.parseFloat(getComputedStyle(article.querySelector('h2') as Element).fontSize),
        h3: Number.parseFloat(getComputedStyle(article.querySelector('h3') as Element).fontSize),
        h4: Number.parseFloat(getComputedStyle(generatedHeadings[0]).fontSize),
        h5: Number.parseFloat(getComputedStyle(generatedHeadings[1]).fontSize),
        h6: Number.parseFloat(getComputedStyle(generatedHeadings[2]).fontSize),
      }
      generatedHeadings.forEach((heading) => heading.remove())
      return sizes
    })
    expect(articleHierarchy.h2).toBe(28)
    expect(articleHierarchy.h3).toBe(24)
    expect(articleHierarchy.h4).toBe(18)
    expect(articleHierarchy.h5).toBe(16)
    expect(articleHierarchy.h6).toBe(14)
  } finally {
    await page.close()
  }
})

test('generated CLI outlines keep subcommands and omit repeated details and footer metadata', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/cli/semantic-models`)
    const article = page.locator('.site-docs-article')
    const toc = page.locator('lv-site-article-toc')
    await page.waitForFunction(() => Boolean(document.querySelector('lv-site-article-toc')?.shadowRoot?.querySelector('a')))

    expect(await article.locator('h3#dataset').count()).toBe(1)
    expect(await article.locator('h3#dataset ~ h4').first().textContent()).toBe('Usage')
    expect(await article.locator('.site-docs-page-meta h2').textContent()).toBe('About this page')

    const visibleOutlineLabels = await toc.evaluate((element) =>
      Array.from(element.shadowRoot?.querySelectorAll<HTMLAnchorElement>('a') ?? [])
        .filter((link) => link.getClientRects().length > 0)
        .map((link) => link.textContent?.trim() ?? ''),
    )
    expect(visibleOutlineLabels.filter((label) => label === 'Usage')).toHaveLength(1)
    expect(visibleOutlineLabels.filter((label) => label === 'Options')).toHaveLength(0)
    expect(visibleOutlineLabels).toContain('Subcommands')
    expect(visibleOutlineLabels).toContain('dataset')
    expect(visibleOutlineLabels).toContain('datasets')
    expect(visibleOutlineLabels).toContain('describe')
    expect(visibleOutlineLabels).not.toContain('Behavior')
    expect(visibleOutlineLabels).not.toContain('Inherited options')
    expect(visibleOutlineLabels).not.toContain('About this page')
    expect(await toc.getByRole('link', { name: 'About this page', exact: true }).count()).toBe(0)
  } finally {
    await page.close()
  }
})

test('generated API outlines keep operations and omit repeated operation details', async () => {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
  })
  try {
    await page.goto(`${baseURL}/docs/api/access`)
    const article = page.locator('.site-docs-article')
    const toc = page.locator('lv-site-article-toc')
    await page.waitForFunction(() => Boolean(document.querySelector('lv-site-article-toc')?.shadowRoot?.querySelector('a')))

    expect(await article.locator('h2#operations').count()).toBe(1)
    const listPrincipals = article.locator('h3#list-principals')
    expect(await listPrincipals.textContent()).toBe('List principals')
    expect(await listPrincipals.locator('xpath=following-sibling::h4[1]').textContent()).toBe('Parameters')

    const visibleOutlineLabels = await toc.evaluate((element) =>
      Array.from(element.shadowRoot?.querySelectorAll<HTMLAnchorElement>('a') ?? [])
        .filter((link) => link.getClientRects().length > 0)
        .map((link) => link.textContent?.trim() ?? ''),
    )
    expect(visibleOutlineLabels[0]).toBe('Operations')
    expect(visibleOutlineLabels).toContain('List principals')
    expect(visibleOutlineLabels).toContain('Create a local principal')
    expect(visibleOutlineLabels).not.toContain('Parameters')
    expect(visibleOutlineLabels).not.toContain('Request body')
    expect(visibleOutlineLabels).not.toContain('Responses')

    const listProjectRoles = article.locator('h3#list-project-roles')
    const listProjectRolesDetail = listProjectRoles.locator('xpath=following-sibling::h4[1]')
    await listProjectRolesDetail.evaluate((heading) => {
      document.documentElement.style.scrollBehavior = 'auto'
      window.scrollTo({ top: heading.getBoundingClientRect().top + window.scrollY - window.innerHeight * 0.2 })
    })
    await page.waitForFunction(() => {
      const toc = document.querySelector<HTMLElement>('lv-site-article-toc')
      const active = toc?.shadowRoot?.querySelector<HTMLAnchorElement>('a.active')
      return active?.textContent?.trim() === 'List project roles' && active.getClientRects().length > 0 && (toc?.scrollTop ?? 0) > 0
    })
    const activeOutline = await toc.evaluate((element) => {
      const active = element.shadowRoot?.querySelector<HTMLAnchorElement>('a.active')
      if (!active) throw new Error('active article outline link is missing')
      const hostRect = element.getBoundingClientRect()
      const activeRect = active.getBoundingClientRect()
      return {
        label: active.textContent?.trim(),
        scrollTop: element.scrollTop,
        visible: activeRect.top >= hostRect.top && activeRect.bottom <= hostRect.bottom,
      }
    })
    expect(activeOutline.label).toBe('List project roles')
    expect(activeOutline.scrollTop).toBeGreaterThan(0)
    expect(activeOutline.visible).toBe(true)
  } finally {
    await page.close()
  }
}, 10_000)

test('visual showcase renders every supported visual type', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/visuals`)
    await page.waitForFunction(() => {
      const showcase = document.querySelector('lv-site-visual-showcase') as HTMLElement & { shadowRoot: ShadowRoot }
      return showcase?.shadowRoot?.querySelectorAll('.chart').length === 23 && showcase?.shadowRoot?.querySelectorAll('.table-card lv-visualization-host').length === 3
    })
    const visuals = await page.locator('lv-site-visual-showcase').evaluate((element) => {
      const root = element.shadowRoot
      return {
        cards: root?.querySelectorAll('.chart').length,
        hosts: root?.querySelectorAll('.chart lv-visualization-host').length,
        kpis: Array.from(root?.querySelectorAll('.chart lv-visualization-host') ?? []).filter((host: any) => host.envelope?.spec?.kind === 'kpi').length,
        links: root?.querySelectorAll('article a[href^="/docs/visuals/"]').length,
      }
    })
    expect(visuals).toEqual({ cards: 23, hosts: 23, kpis: 1, links: 26 })
    await page.waitForFunction(() => {
      const showcase = document.querySelector('lv-site-visual-showcase') as HTMLElement & { shadowRoot: ShadowRoot }
      return showcase?.shadowRoot?.querySelectorAll('.table-card lv-visualization-host').length === 3
    })
    await page.waitForFunction(() => Array.from(document.querySelector('lv-site-visual-showcase')?.shadowRoot?.querySelectorAll('.table-card lv-visualization-host') ?? []).every((host: any) => Boolean(host.envelope?.spec?.title) && !host.shadowRoot?.querySelector('[role="alert"]')))
    const tables = await page.locator('lv-site-visual-showcase').evaluate((element) => ({
      cards: element.shadowRoot?.querySelectorAll('.table-card').length,
      tables: element.shadowRoot?.querySelectorAll('.table-card lv-visualization-host').length,
      titles: Array.from(element.shadowRoot?.querySelectorAll('.table-card lv-visualization-host') ?? []).map((host: any) => host.envelope?.spec?.title),
      aggregateValues: Array.from(element.shadowRoot?.querySelectorAll('.table-card lv-visualization-host') ?? [])
        .filter((host: any) => host.envelope?.spec?.kind !== 'table')
        .flatMap((host) => Array.from(host.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelectorAll('[role="cell"]') ?? []).map((cell) => cell.textContent?.trim())),
    }))
    expect(tables.cards).toBe(3)
    expect(tables.tables).toBe(3)
    expect(tables.titles).toContain('Orders')
    expect(tables.aggregateValues.some((value) => value === '—' || value === '-')).toBe(false)
    expect(tables.aggregateValues).toContain('0')
    expect(tables.aggregateValues).toContain('$0.00')
    const tableLayout = await page.locator('lv-site-visual-showcase').evaluate((element) => {
      const root = element.shadowRoot
      const chartGrid = root?.querySelector('.chart-grid')?.getBoundingClientRect()
      const tableSection = root?.querySelector('[aria-labelledby="table-showcase-heading"]')
      const tableHeading = tableSection?.querySelector('.section-heading')?.getBoundingClientRect()
      const tableGrid = root?.querySelector('.table-grid')?.getBoundingClientRect()
      const cards = Array.from(root?.querySelectorAll('.table-card') ?? []).map((card) => {
        const host = card.querySelector('lv-visualization-host') as any
        const rect = card.getBoundingClientRect()
        const table = host?.shadowRoot?.querySelector('lv-report-table')
        const scrollport = table?.shadowRoot?.querySelector('.table-scrollport') as HTMLElement | null
        const canvas = table?.shadowRoot?.querySelector('.canvas') as HTMLElement | null
        return {
          kind: host?.envelope?.spec?.kind,
          left: rect.left,
          top: rect.top,
          width: rect.width,
          height: rect.height,
          overflow: (scrollport?.scrollWidth ?? 0) - (scrollport?.clientWidth ?? 0),
          dataGap: (scrollport?.clientWidth ?? 0) - (canvas?.getBoundingClientRect().width ?? 0),
          classes: card.className,
        }
      })
      const compactCards = Array.from(root?.querySelectorAll('.table-card.compact') ?? [])
      const matrixCard = Array.from(root?.querySelectorAll('.table-card') ?? []).find((card) => (card.querySelector('lv-visualization-host') as any)?.envelope?.spec?.kind === 'matrix')
      const matrixTable = matrixCard?.querySelector('lv-visualization-host')?.shadowRoot?.querySelector('lv-report-table')
      const matrixScrollport = matrixTable?.shadowRoot?.querySelector<HTMLElement>('.table-scrollport')
      const compactDataGaps = compactCards.map((card) => {
        const host = card.querySelector('lv-visualization-host')
        const table = host?.shadowRoot?.querySelector('lv-report-table')
        const scrollport = table?.shadowRoot?.querySelector('.table-scrollport')?.getBoundingClientRect()
        const canvas = table?.shadowRoot?.querySelector('.canvas')?.getBoundingClientRect()
        return (scrollport?.bottom ?? 0) - (canvas?.bottom ?? 0)
      })
      return {
        sectionGap: (tableHeading?.top ?? 0) - (chartGrid?.bottom ?? 0),
        cards,
        gridWidth: tableGrid?.width ?? 0,
        gridCenter: (tableGrid?.left ?? 0) + (tableGrid?.width ?? 0) / 2,
        compactCards: compactCards.length,
        compactDataGaps,
        matrixOverflow: (matrixScrollport?.scrollWidth ?? 0) - (matrixScrollport?.clientWidth ?? 0),
      }
    })
    expect(tableLayout.sectionGap).toBeGreaterThanOrEqual(48)
    const regular = tableLayout.cards.find((card) => card.kind === 'table')!
    const matrix = tableLayout.cards.find((card) => card.kind === 'matrix')!
    const pivot = tableLayout.cards.find((card) => card.kind === 'pivot')!
    expect(Math.abs(regular.top - pivot.top)).toBeLessThanOrEqual(1)
    expect(regular.left + regular.width).toBeLessThanOrEqual(pivot.left + 1)
    expect(regular.width).toBeLessThan(pivot.width)
    expect(Math.abs(regular.height - pivot.height)).toBeLessThanOrEqual(1)
    expect(matrix.width).toBeGreaterThanOrEqual(tableLayout.gridWidth - 1)
    expect(matrix.top).toBeGreaterThan(pivot.top + pivot.height)
    // Every table variant deliberately preserves readable business-field widths
    // and scrolls horizontally when its authored columns exceed the card.
    expect(regular.overflow).toBeGreaterThan(1)
    expect(matrix.overflow).toBeGreaterThan(1)
    expect(pivot.overflow).toBeGreaterThan(1)
    expect(pivot.dataGap).toBeLessThanOrEqual(1)
    expect(regular.classes).toContain('centered')
    expect(regular.classes).toContain('narrow')
    expect(matrix.classes).toContain('wide')
    expect(pivot.classes).toContain('centered')
    expect(tableLayout.matrixOverflow).toBe(matrix.overflow)
    expect(tableLayout.compactCards).toBe(2)
    expect(tableLayout.compactDataGaps.every((gap) => gap <= 8)).toBe(true)
    const catalog = await page.locator('lv-site-visual-showcase').evaluate((element) =>
      Array.from(element.shadowRoot?.querySelectorAll('article') ?? []).map((card) => {
        const host = card.querySelector('lv-visualization-host') as any
        const link = card.querySelector<HTMLAnchorElement>('a[href^="/docs/visuals/"]')
        return {
          visualID: host?.envelope?.visualID,
          href: link?.getAttribute('href'),
          label: link?.getAttribute('aria-label'),
        }
      }),
    )
    expect(catalog).toHaveLength(26)
    expect(catalog).toContainEqual({ visualID: 'revenue_line', href: '/docs/visuals/line', label: 'Open Line chart documentation' })
    expect(catalog).toContainEqual({ visualID: 'revenue_kpi_favorable', href: '/docs/visuals/kpi', label: 'Open KPI documentation' })
    expect(catalog.every(({ visualID, href, label }) => Boolean(visualID && href && label))).toBe(true)
    const chartLabelPolicies = await page.locator('lv-site-visual-showcase').evaluate((element) =>
      Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []).flatMap((host: any) => {
        const { kind, mark, presentation } = host.envelope?.spec ?? {}
        const hasChartLabelPolicy = ['cartesian', 'point', 'proportional', 'hierarchy'].includes(kind) || (kind === 'polar' && mark === 'gauge')
        return hasChartLabelPolicy ? [{ visualID: host.envelope?.visualID, density: presentation?.labelPolicy?.density }] : []
      }),
    )
    expect(chartLabelPolicies.length).toBeGreaterThan(0)
    expect(chartLabelPolicies.filter(({ density }) => density !== 'automatic' && density !== 'hidden' && density !== 'dense')).toEqual([])
    expect(chartLabelPolicies.filter(({ density }) => density === 'dense').map(({ visualID }) => visualID).sort()).toEqual([
      'state_status_heatmap',
    ])
    expect(chartLabelPolicies.filter(({ density }) => density === 'hidden').map(({ visualID }) => visualID).sort()).toEqual([
      'delivery_distribution',
      'market_candlestick',
      'revenue',
      'revenue_line',
      'revenue_orders_combo',
    ])
    expect(await page.locator('lv-site-visual-showcase').evaluate((element) => {
      const hosts = Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as Array<HTMLElement & { envelope?: any }>
      return hosts.find((host) => host.envelope?.visualID === 'state_order_map')?.envelope?.spec?.presentation?.theme
    })).toBe('auto')
    expect(await page.locator('lv-site-visual-showcase').evaluate((element) => {
      const hosts = Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as Array<HTMLElement & { envelope?: any }>
      const sunburst = hosts.find((host) => host.envelope?.visualID === 'category_status_sunburst')
      return sunburst?.envelope?.spec?.presentation?.labelPolicy
    })).toMatchObject({ density: 'automatic', maxCharacters: 12, minimumSpacing: 6, tooltipFallback: true })
    expect(await page.locator('lv-site-visual-showcase').evaluate((element) => {
      const hosts = Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as Array<HTMLElement & { envelope?: any }>
      return hosts.find((host) => host.envelope?.visualID === 'state_status_heatmap')?.envelope?.spec?.presentation?.displayUnits
    })).toBe('none')
  } finally {
    await page.close()
  }
}, 20_000)

test('heatmap scale is a calculable range that hides only out-of-range visible cells', async () => {
  const page = await browser.newPage({ viewport: { width: 966, height: 749 } })
  try {
    await page.goto(`${baseURL}/visuals`)
    await page.waitForFunction(() => {
      const root = document.querySelector('lv-site-visual-showcase')?.shadowRoot
      const host = Array.from(root?.querySelectorAll('lv-visualization-host') ?? []).find((candidate: any) => candidate.envelope?.visualID === 'state_status_heatmap')
      return Boolean(host?.shadowRoot?.querySelector('[_echarts_instance_]'))
    })

    const states = await page.locator('lv-site-visual-showcase').evaluate(async (element) => {
      const host = Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []).find((candidate: any) => candidate.envelope?.visualID === 'state_status_heatmap') as HTMLElement
      const frame = host.shadowRoot?.querySelector<HTMLElement>('[_echarts_instance_]')
      if (!frame) throw new Error('heatmap ECharts frame is missing')

      let chart: any
      const moduleURLs = performance.getEntriesByType('resource')
        .map(({ name }) => name)
        .filter((name) => /\/chunks\/index-[^/]+\.js$/.test(name))
      for (const url of moduleURLs) {
        const module = await import(url)
        if (typeof module.getInstanceByDom !== 'function') continue
        chart = module.getInstanceByDom(frame)
        if (chart) break
      }
      if (!chart) throw new Error('heatmap ECharts instance is missing')

      const inspect = () => {
        const visualMap = chart.getOption().visualMap[0]
        const data = chart.getModel().getSeriesByIndex(0).getData()
        let hiddenRows = 0
        let visibleRows = 0
        for (let index = 0; index < data.count(); index++) {
          if (data.getItemVisual(index, 'style')?.opacity === 0) hiddenRows++
          else visibleRows++
        }
        return {
          calculable: visualMap.calculable as boolean,
          hiddenRows,
          maximum: visualMap.max as number,
          minimum: visualMap.min as number,
          rowCount: data.count(),
          text: visualMap.text as string[] | undefined,
          visibleRows,
        }
      }

      const select = async (selected: [number, number]) => {
        chart.dispatchAction({ type: 'selectDataRange', selected })
        await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
        return inspect()
      }

      return { initial: inspect(), narrowed: await select([2, 3]) }
    })

    expect(states.initial).toEqual({
      calculable: true,
      hiddenRows: 0,
      maximum: 3,
      minimum: 0,
      rowCount: 8,
      text: undefined,
      visibleRows: 8,
    })
    expect(states.narrowed.hiddenRows).toBeGreaterThan(0)
    expect(states.narrowed.visibleRows).toBeGreaterThan(0)
    expect(states.narrowed.hiddenRows + states.narrowed.visibleRows).toBe(states.initial.rowCount)
  } finally {
    await page.close()
  }
}, 20_000)

test('radar chrome follows the resolved chart theme', async () => {
  const page = await browser.newPage({ viewport: { width: 966, height: 749 } })
  try {
    await page.goto(`${baseURL}/visuals`)
    await page.waitForFunction(() => {
      const root = document.querySelector('lv-site-visual-showcase')?.shadowRoot
      const host = Array.from(root?.querySelectorAll('lv-visualization-host') ?? []).find((candidate: any) => candidate.envelope?.visualID === 'status_radar')
      return Boolean(host?.shadowRoot?.querySelector('[_echarts_instance_]'))
    })

    const states: Array<{ grid: string; surface: string }> = []
    for (const theme of ['light', 'dark'] as const) {
      await page.evaluate((mode) => document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode } })), theme)
      await page.waitForFunction((mode) => document.documentElement.getAttribute('data-color-mode') === mode, theme)
      await page.waitForTimeout(250)

      const state = await page.locator('lv-site-visual-showcase').evaluate(async (element) => {
        const host = Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []).find((candidate: any) => candidate.envelope?.visualID === 'status_radar') as HTMLElement
        const renderer = host.shadowRoot?.querySelector<HTMLElement>('.renderer')
        const frame = host.shadowRoot?.querySelector<HTMLElement>('[_echarts_instance_]')
        if (!renderer || !frame) throw new Error('radar ECharts frame is missing')

        let chart: any
        const moduleURLs = performance.getEntriesByType('resource')
          .map(({ name }) => name)
          .filter((name) => /\/chunks\/index-[^/]+\.js$/.test(name))
        for (const url of moduleURLs) {
          const module = await import(url)
          if (typeof module.getInstanceByDom !== 'function') continue
          chart = module.getInstanceByDom(frame)
          if (chart) break
        }
        if (!chart) throw new Error('radar ECharts instance is missing')

        const styles = getComputedStyle(renderer)
        const grid = styles.getPropertyValue('--lv-chart-grid').trim()
        const surface = styles.getPropertyValue('--lv-chart-surface').trim()
        const radar = chart.getOption().radar[0]
        return {
          axisColor: radar.axisLine.lineStyle.color as string,
          grid,
          splitAreaColors: radar.splitArea.areaStyle.color as string[],
          splitAreaOpacity: radar.splitArea.areaStyle.opacity as number,
          splitLineColor: radar.splitLine.lineStyle.color as string,
          surface,
        }
      })

      expect(state).toMatchObject({
        axisColor: state.grid,
        splitAreaColors: [state.surface, state.grid],
        splitAreaOpacity: 0.18,
        splitLineColor: state.grid,
      })
      states.push({ grid: state.grid, surface: state.surface })
    }
    expect(states[0]).not.toEqual(states[1])
  } finally {
    await page.close()
  }
}, 20_000)

test('visual showcase remains visibly rendered in light and dark themes', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    await page.goto(`${baseURL}/visuals`)
    await page.waitForFunction(() => {
      const root = document.querySelector('lv-site-visual-showcase')?.shadowRoot
      const hosts = Array.from(root?.querySelectorAll('lv-visualization-host') ?? [])
      return hosts.length === 26 && hosts.every((host: any) =>
        Boolean(host.envelope?.visualID) &&
        Boolean(host.shadowRoot?.querySelector('.renderer')?.firstElementChild) &&
        !host.shadowRoot?.querySelector('[role="alert"]'),
      )
    })

    for (const theme of ['light', 'dark'] as const) {
      await page.evaluate((mode) => new Promise<void>((resolve) => {
        document.addEventListener('leapview-theme-applied', () => requestAnimationFrame(() => resolve()), { once: true })
        document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode } }))
      }), theme)
      await page.waitForFunction((mode) => {
        const root = document.querySelector('lv-site-visual-showcase')?.shadowRoot
        const hosts = Array.from(root?.querySelectorAll('lv-visualization-host') ?? [])
        return document.documentElement.getAttribute('data-color-mode') === mode
          && hosts.length === 26
          && hosts.every((host: any) => host.shadowRoot?.querySelector('.renderer')?.getAttribute('aria-busy') === 'false')
      }, theme)

      const metrics = await page.locator('lv-site-visual-showcase').evaluate((element) =>
        Array.from(element.shadowRoot?.querySelectorAll('article') ?? []).map((card) => {
          const host = card.querySelector('lv-visualization-host') as HTMLElement & {
            envelope?: {
              visualID?: string
              spec?: { kind?: string; mark?: string; y?: Array<{ dataset: string; field: string }> }
              dataState?: { kind?: string; datasets?: Array<{ id: string; columns: string[]; rows: unknown[][] }> }
            }
            shadowRoot: ShadowRoot
          }
          const renderer = host.shadowRoot?.querySelector<HTMLElement>('.renderer')
          const canvases = Array.from(host.shadowRoot?.querySelectorAll<HTMLCanvasElement>('canvas') ?? [])
          const table = renderer?.querySelector<HTMLElement>('lv-report-table')
          const bounds = renderer?.getBoundingClientRect()
          let sampledPixels = 0
          let coloredPixels = 0
          for (const canvas of canvases) {
            if (sampledPixels > 10 && coloredPixels > 0) break
            const context = canvas.getContext('2d', { willReadFrequently: true })
            if (context && canvas.width > 0 && canvas.height > 0) {
              const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data
              for (let index = 0; index < pixels.length; index += 4) {
                if (pixels[index + 3]! < 32) continue
                sampledPixels++
                const maximum = Math.max(pixels[index]!, pixels[index + 1]!, pixels[index + 2]!)
                const minimum = Math.min(pixels[index]!, pixels[index + 1]!, pixels[index + 2]!)
                if (maximum - minimum >= 24) {
                  coloredPixels++
                }
                if (sampledPixels > 10 && coloredPixels > 0) break
              }
            }
          }
          return {
            visualID: host.envelope?.visualID,
            kind: host.envelope?.spec?.kind,
            alert: host.shadowRoot?.querySelector('[role="alert"]')?.textContent?.trim() ?? '',
            width: Math.round(bounds?.width ?? 0),
            height: Math.round(bounds?.height ?? 0),
            canvasWidth: Math.max(0, ...canvases.map((canvas) => canvas.width)),
            canvasHeight: Math.max(0, ...canvases.map((canvas) => canvas.height)),
            sampledPixels,
            coloredPixels,
            mapFrame: host.shadowRoot?.querySelectorAll('.maplibregl-map .maplibregl-canvas').length ?? 0,
            tableText: table?.shadowRoot?.textContent?.replace(/\s+/g, ' ').trim().length ?? 0,
            rendererText: renderer?.textContent?.replace(/\s+/g, ' ').trim().length ?? 0,
          }
        }),
      )

      expect(metrics, `${theme} catalog inventory`).toHaveLength(26)
      for (const metric of metrics) {
        expect(metric.alert, `${theme}/${metric.visualID} renderer alert`).toBe('')
        expect(metric.width, `${theme}/${metric.visualID} renderer width`).toBeGreaterThan(100)
        expect(metric.height, `${theme}/${metric.visualID} renderer height`).toBeGreaterThan(100)
        if (metric.kind === 'table' || metric.kind === 'matrix' || metric.kind === 'pivot') {
          expect(metric.tableText, `${theme}/${metric.visualID} visible table content`).toBeGreaterThan(40)
        } else if (metric.kind === 'kpi') {
          expect(metric.rendererText, `${theme}/${metric.visualID} visible KPI context`).toBeGreaterThan(30)
        } else {
          expect(metric.canvasWidth, `${theme}/${metric.visualID} canvas width`).toBeGreaterThan(100)
          expect(metric.canvasHeight, `${theme}/${metric.visualID} canvas height`).toBeGreaterThan(100)
          if (metric.kind === 'geographic') {
            expect(metric.mapFrame, `${theme}/${metric.visualID} MapLibre frame`).toBe(1)
          } else {
            expect(metric.sampledPixels, `${theme}/${metric.visualID} painted pixels`).toBeGreaterThan(10)
            expect(metric.coloredPixels, `${theme}/${metric.visualID} visible data marks`).toBeGreaterThan(0)
          }
        }
      }
    }
  } finally {
    await page.close()
  }
}, 30_000)

async function waitForSite(siteProcess: Bun.Subprocess, deadline: number): Promise<void> {
  while (Date.now() < deadline) {
    if (siteProcess.exitCode !== null) {
      throw new Error(`LeapView site exited before becoming ready (code ${siteProcess.exitCode})`)
    }
    try {
      const response = await fetch(baseURL)
      if (response.ok) return
    } catch {
      // The directly spawned site is still binding its listener.
    }
    await Bun.sleep(100)
  }
  throw new Error('LeapView site did not become ready')
}
