import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/login-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument({
        localAuth: url.searchParams.get('localAuth') === 'true',
        ssoAuth: url.searchParams.has('ssoAuth') ? url.searchParams.get('ssoAuth') === 'true' : true,
        mustChangePassword: url.searchParams.get('mustChangePassword') === 'true',
        error: url.searchParams.get('error') ?? '',
      }))
      return
    }
    if (url.pathname === '/loader-test') {
      response.setHeader('content-type', 'text/html')
      response.end(loaderTestDocument())
      return
    }
    if (url.pathname === '/fake-topology-background.js') {
      response.setHeader('content-type', 'text/javascript')
      response.end(`window.__loginBackgroundModuleLoaded = true`)
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') || url.pathname === '/static/app.css' || url.pathname === '/static/login-background-loader.js' ? projectRoot : root
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', url.pathname.endsWith('.css') ? 'text/css' : 'text/javascript')
      response.end(await readFile(file))
    } catch {
      response.writeHead(404)
      response.end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('login page composes branded route UI', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-login-page').evaluate((element: any) => {
      const root = (element.shadowRoot as ShadowRoot)
      const panel = root.querySelector('.panel') as HTMLElement
      const hostRect = element.getBoundingClientRect()
      const panelRect = panel.getBoundingClientRect()
      const visibleThemeIcon = root.querySelector('[data-theme-icon]:not([hidden])') as HTMLElement | null
      const brandMark = root.querySelector('lv-brand-mark') as HTMLElement | null
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        hasBrandName: root.textContent?.includes('LeapView') ?? false,
        brandMarkCount: root.querySelectorAll('lv-brand-mark').length,
        apertureCircleCount: brandMark?.shadowRoot?.querySelectorAll('circle[cx="12"][cy="12"][r="10"]').length,
        hasBackground: Boolean(root.querySelector('lv-topology-background[data-login-background]')),
        backgroundRegistered: Boolean(customElements.get('lv-topology-background')),
        moduleSrc: root.querySelector('lv-topology-background')?.getAttribute('data-module-src'),
        hasThemeToggle: Boolean(root.querySelector('[data-theme-toggle]')),
        visibleThemeIcon: visibleThemeIcon?.getAttribute('data-theme-icon'),
        visibleThemeIconHasSvg: Boolean(visibleThemeIcon?.querySelector('svg')),
        panelFitsViewport: panelRect.left >= hostRect.left - 1 && panelRect.right <= hostRect.right + 1,
        hostHeight: Math.round(hostRect.height),
        provider: root.querySelector('.provider')?.textContent?.trim(),
      }
    })

    expect(state).toEqual({
      title: 'Welcome back',
      hasBrandName: true,
      brandMarkCount: 1,
      apertureCircleCount: 1,
      hasBackground: true,
      backgroundRegistered: false,
      moduleSrc: '/static/topology-background.js?v=dev',
      hasThemeToggle: true,
      visibleThemeIcon: 'system',
      visibleThemeIconHasSvg: true,
      panelFitsViewport: true,
      hostHeight: expect.any(Number),
      provider: 'Sign in with Azure Active Directory',
    })
    expect(state.hostHeight).toBeGreaterThanOrEqual(820)
  } finally {
    await page.close()
  }
})

test('minimal form remains centred and preserves the original background overlay', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 720 } })
  try {
    await page.goto(`${baseURL}/?localAuth=true&ssoAuth=false`)
    await page.getByRole('heading', { name: 'Welcome back' }).waitFor()
    const state = await page.locator('lv-login-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const panelElement = root.querySelector('.panel')
      const panel = panelElement.getBoundingClientRect()
      const brand = root.querySelector('.brand-lockup')
      const brandRect = brand.getBoundingClientRect()
      const themeRect = root.querySelector('.theme').getBoundingClientRect()
      const tokenProbe = document.createElement('span')
      tokenProbe.style.backgroundColor = 'var(--overlay-backdrop-bgColor)'
      document.body.append(tokenProbe)
      const expected = getComputedStyle(tokenProbe).backgroundColor
      tokenProbe.remove()
      return {
        logoAtTopLeft: brandRect.left <= 24 && brandRect.top <= 24,
        logoOutsideForm: !panelElement.contains(brand),
        logoClearsControls: brandRect.bottom < panel.top && brandRect.right < themeRect.left,
        centredX: Math.abs(panel.x + panel.width / 2 - innerWidth / 2) < 1,
        centredY: Math.abs(panel.y + panel.height / 2 - innerHeight / 2) < 1,
        originalOverlay: getComputedStyle(root.querySelector('.scrim')).backgroundColor === expected,
        hasMarketing: Boolean(root.querySelector('.product-preview, .suggestion, .feature-options')),
      }
    })
    expect(state).toEqual({ centredX: true, centredY: true, originalOverlay: true, hasMarketing: false, logoAtTopLeft: true, logoOutsideForm: true, logoClearsControls: true })
  } finally {
    await page.close()
  }
})

test('login form remains reachable on a short mobile viewport without horizontal overflow', async () => {
  const page = await browser.newPage({ viewport: { width: 320, height: 240 } })
  try {
    await page.goto(`${baseURL}/?localAuth=true&ssoAuth=false`)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-login-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const host = element as HTMLElement
      const form = root.querySelector('form') as HTMLElement | null
      const panel = root.querySelector('.panel') as HTMLElement | null
      const hostRect = host.getBoundingClientRect()
      const panelRect = panel?.getBoundingClientRect()
      const documentWidth = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth)
      form?.scrollIntoView({ block: 'center', inline: 'nearest' })
      const formRect = form?.getBoundingClientRect()
      return {
        hasForm: Boolean(form),
        noHorizontalOverflow: documentWidth <= window.innerWidth && host.scrollWidth <= host.clientWidth,
        panelFitsViewport: Boolean(panelRect && panelRect.left >= hostRect.left - 1 && panelRect.right <= hostRect.right + 1),
        formIntersectsViewport: Boolean(formRect && formRect.bottom > 0 && formRect.top < window.innerHeight),
      }
    })

    expect(state).toEqual({
      hasForm: true,
      noHorizontalOverflow: true,
      panelFitsViewport: true,
      formIntersectsViewport: true,
    })
  } finally {
    await page.close()
  }
})

test('login fits laptop and phone viewports without page scrolling', async () => {
  const page = await browser.newPage()
  try {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 1280, height: 720 }, { width: 900, height: 600 }, { width: 390, height: 700 }]) {
      await page.setViewportSize(viewport)
      await page.goto(`${baseURL}/?localAuth=true&ssoAuth=true`)
      await page.getByRole('heading', { name: 'Welcome back' }).waitFor()
      const size = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight }))
      expect(size.width).toBeLessThanOrEqual(viewport.width)
      expect(size.height).toBeLessThanOrEqual(viewport.height)
      expect(await page.getByRole('button', { name: 'Sign in', exact: true }).isVisible()).toBe(true)
    }
  } finally {
    await page.close()
  }
})

test('mobile keyboard navigation moves directly through the sign-in form', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 740 } })
  try {
    await page.goto(`${baseURL}/?localAuth=true&ssoAuth=false`)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const focusedNames: Array<string | undefined> = []
    for (let index = 0; index < 5; index += 1) {
      await page.keyboard.press('Tab')
      focusedNames.push(await page.locator('lv-login-page').evaluate((element: any) => {
        const focused = element.shadowRoot.activeElement as HTMLElement | null
        if (!focused) return undefined
        if (focused.matches('[data-theme-toggle]')) return 'theme'
        if (focused.matches('input')) return focused.getAttribute('name') ?? undefined
        if (focused.matches('button')) return focused.getAttribute('aria-label') ?? focused.textContent?.replace(/\s+/g, ' ').trim()
        return focused.tagName.toLowerCase()
      }))
    }

    expect(focusedNames).toEqual([
      'theme',
      'email',
      'password',
      'Show password',
      'Sign in',
    ])
  } finally {
    await page.close()
  }
})

test('local authentication renders the local login contract', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(`${baseURL}/?localAuth=true&ssoAuth=false`)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-login-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const form = root.querySelector('form') as HTMLFormElement | null
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        action: form?.getAttribute('action'),
        method: form?.getAttribute('method'),
        email: form?.querySelector('input[name="email"]')?.getAttribute('autocomplete'),
        password: form?.querySelector('input[name="password"]')?.getAttribute('autocomplete'),
        submit: form?.querySelector('button[type="submit"]')?.textContent?.trim(),
        providerCount: root.querySelectorAll('.provider').length,
      }
    })

    expect(state).toEqual({
      title: 'Welcome back',
      action: '/auth/local/login',
      method: 'post',
      email: 'username',
      password: 'current-password',
      submit: 'Sign in',
      providerCount: 0,
    })
  } finally {
    await page.close()
  }
})

test('password visibility toggles preserve values and never submit the form', async () => {
  const page = await browser.newPage()
  try {
    for (const changePassword of [false, true]) {
      await page.goto(`${baseURL}/?localAuth=true&ssoAuth=false&mustChangePassword=${changePassword}`)
      await page.locator('lv-login-page form').waitFor()
      await page.locator('form').evaluate((form) => {
        (window as any).__submissions = 0
        form.addEventListener('submit', (event) => { event.preventDefault(); (window as any).__submissions++ })
      })
      const fields = changePassword ? ['Temporary password', 'New password'] : ['Password']
      for (const label of fields) {
        const input = page.getByLabel(label, { exact: true })
        await input.fill('example-password-123')
        expect(await input.getAttribute('type')).toBe('password')
        const show = page.getByRole('button', { name: `Show ${label.toLowerCase()}`, exact: true })
        await show.focus()
        await page.keyboard.press('Space')
        expect(await input.getAttribute('type')).toBe('text')
        expect(await input.inputValue()).toBe('example-password-123')
        const hide = page.getByRole('button', { name: `Hide ${label.toLowerCase()}`, exact: true })
        expect(await hide.evaluate((button) => button === (button.getRootNode() as ShadowRoot).activeElement)).toBe(true)
        await hide.click()
        expect(await input.getAttribute('type')).toBe('password')
        expect(await input.inputValue()).toBe('example-password-123')
      }
      expect(await page.evaluate(() => (window as any).__submissions)).toBe(0)
    }
  } finally {
    await page.close()
  }
})

test('SSO authentication renders the configured provider contract', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(`${baseURL}/?localAuth=false&ssoAuth=true`)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-login-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const provider = root.querySelector('.provider') as HTMLAnchorElement | null
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        href: provider?.getAttribute('href'),
        label: provider?.textContent?.trim(),
        formCount: root.querySelectorAll('form').length,
      }
    })

    expect(state).toEqual({
      title: 'Welcome back',
      href: '/auth/azureadv2',
      label: 'Sign in with Azure Active Directory',
      formCount: 0,
    })
  } finally {
    await page.close()
  }
})

test('change-password authentication renders only the password recovery contract', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(`${baseURL}/?localAuth=true&ssoAuth=true&mustChangePassword=true`)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-login-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const form = root.querySelector('form') as HTMLFormElement | null
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        action: form?.getAttribute('action'),
        currentPassword: form?.querySelector('input[name="currentPassword"]')?.getAttribute('autocomplete'),
        newPassword: form?.querySelector('input[name="newPassword"]')?.getAttribute('autocomplete'),
        requirements: form?.querySelector('#new-password-requirements')?.textContent?.trim(),
        submit: form?.querySelector('button[type="submit"]')?.textContent?.trim(),
        providerCount: root.querySelectorAll('.provider').length,
      }
    })

    expect(state).toEqual({
      title: 'Set a new password',
      action: '/auth/local/password',
      currentPassword: 'current-password',
      newPassword: 'new-password',
      requirements: 'Use at least 12 characters.',
      submit: 'Change password',
      providerCount: 0,
    })
  } finally {
    await page.close()
  }
})

test('login errors stay visible in the local form contract', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(`${baseURL}/?localAuth=true&ssoAuth=false&error=The%20email%20or%20password%20is%20incorrect.`)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-login-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const error = root.querySelector('[role="alert"]') as HTMLElement | null
      const form = root.querySelector('form') as HTMLFormElement | null
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        error: error?.textContent?.trim(),
        live: error?.getAttribute('aria-live'),
        formAction: form?.getAttribute('action'),
      }
    })

    expect(state).toEqual({
      title: 'Welcome back',
      error: 'The email or password is incorrect.',
      live: 'assertive',
      formAction: '/auth/local/login',
    })
  } finally {
    await page.close()
  }
})

test('login background loader imports shadow DOM background module during idle time', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(`${baseURL}/loader-test`)
    await page.waitForFunction(() => {
      const host = document.querySelector('lv-login-page')
      const background = host?.shadowRoot?.querySelector('[data-login-background]')
      return Boolean((window as any).__loginBackgroundModuleLoaded && background?.getAttribute('data-background-state') === 'loaded')
    })

    const state = await page.evaluate(() => {
      const host = document.querySelector('lv-login-page')
      const background = host?.shadowRoot?.querySelector('[data-login-background]')
      return {
        moduleLoaded: (window as any).__loginBackgroundModuleLoaded === true,
        backgroundState: background?.getAttribute('data-background-state'),
      }
    })

    expect(state).toEqual({
      moduleLoaded: true,
      backgroundState: 'loaded',
    })
  } finally {
    await page.close()
  }
})

test('login theme toggle cycles shadow DOM icon and dispatches theme change', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-login-page').evaluate(async (element: any) => {
      const root = (element.shadowRoot as ShadowRoot)
      const changes: string[] = []
      document.addEventListener('leapview-theme-change', (event: Event) => { changes.push((event as CustomEvent<{ mode?: string }>).detail?.mode ?? '') }, { once: true })
      const toggle = root.querySelector('[data-theme-toggle]') as HTMLButtonElement
      toggle.click()
      await element.updateComplete
      const visibleThemeIcon = root.querySelector('[data-theme-icon]:not([hidden])') as HTMLElement | null
      return {
        mode: toggle.getAttribute('data-theme-mode'),
        visibleThemeIcon: visibleThemeIcon?.getAttribute('data-theme-icon'),
        visibleThemeIconHasSvg: Boolean(visibleThemeIcon?.querySelector('svg')),
        changes,
      }
    })

    expect(state).toEqual({
      mode: 'light',
      visibleThemeIcon: 'light',
      visibleThemeIconHasSvg: true,
      changes: ['light'],
    })
  } finally {
    await page.close()
  }
})

test('login theme toggle preserves an accessibility theme until the user changes it', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview-color-mode', 'dark_colorblind'))
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-login-page'))
    await page.locator('lv-login-page').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-login-page').evaluate((element: any) => {
      const toggle = (element.shadowRoot as ShadowRoot).querySelector('[data-theme-toggle]') as HTMLButtonElement
      const visibleThemeIcon = (element.shadowRoot as ShadowRoot).querySelector('[data-theme-icon]:not([hidden])') as HTMLElement | null
      return {
        mode: toggle.dataset.themeMode,
        label: toggle.getAttribute('aria-label'),
        visibleThemeIcon: visibleThemeIcon?.dataset.themeIcon,
      }
    })

    expect(state).toEqual({
      mode: 'dark_colorblind',
      label: 'Dark protanopia and deuteranopia. Switch to System theme.',
      visibleThemeIcon: 'dark',
    })
  } finally {
    await page.close()
  }
})

type TestDocumentOptions = {
  localAuth?: boolean
  ssoAuth?: boolean
  mustChangePassword?: boolean
  error?: string
}

function testDocument(options: TestDocumentOptions = {}): string {
  const page = {
    kind: 'login',
    title: 'LeapView',
    localAuth: options.localAuth ?? false,
    ssoAuth: options.ssoAuth ?? true,
    mustChangePassword: options.mustChangePassword ?? false,
    providerLabel: 'Sign in with Azure Active Directory',
    backgroundModuleSrc: '/static/topology-background.js?v=dev',
  }
  const status = { error: options.error ?? '' }
  return `
    <!doctype html>
    <html>
      <head>
        <link rel="stylesheet" href="/static/app.css">
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-panel: #fff; --lv-bg-control: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-accent: #0969da; --bgColor-accent-emphasis: #0969da; --bgColor-inverse: #0d1117; --lv-topology-bg: #0d1117; --lv-border-default: 1px solid #d0d7de; --lv-radius-default: 6px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --base-size-24: 24px; --control-medium-size: 32px; --control-xlarge-size: 40px; --shadow-resting-small: 0 1px 2px rgb(0 0 0 / .08); }
        </style>
      </head>
      <body>
        <main data-signals="${escapeHTML(JSON.stringify({ page, status }))}">
          <lv-login-page></lv-login-page>
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/login-page-under-test.js"></script>
      </body>
    </html>
  `
}

function loaderTestDocument(): string {
  return `
    <!doctype html>
    <html>
      <body>
        <lv-login-page></lv-login-page>
        <script>
          customElements.define('lv-login-page', class extends HTMLElement {
            connectedCallback() {
              this.attachShadow({ mode: 'open' }).innerHTML = '<div data-login-background data-module-src="/fake-topology-background.js"></div>'
            }
          })
        </script>
        <script type="module" src="/static/login-background-loader.js"></script>
      </body>
    </html>
  `
}

function escapeHTML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}
