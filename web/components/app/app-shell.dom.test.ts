import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

const root = process.cwd()
const tmpRoot = join(root, '.tmp/app-shell-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/fallback') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(false))
      return
    }
    if (url.pathname === '/upgraded-shell') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true))
      return
    }
    if (url.pathname === '/upgraded-compact-shell') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true, true))
      return
    }
    if (url.pathname === '/sidebar-history') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true, false, true))
      return
    }
    if (url.pathname === '/sidebar-active-nav') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true, false, false, true))
      return
    }
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true, false, true))
      return
    }
    if (url.pathname === '/admin-sidebar') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true, true, false, false, true))
      return
    }
    if (url.pathname === '/signal-shell') {
      response.setHeader('content-type', 'text/html')
      response.end(signalShellDocument())
      return
    }
    if (url.pathname === '/chats') {
      response.setHeader('content-type', 'text/html')
      response.end('<!doctype html><title>Chat list</title><main>Chat list</main>')
      return
    }

    const fileRoot = url.pathname.startsWith('/tmp/') ? tmpRoot : root
    const path = url.pathname.startsWith('/tmp/') ? url.pathname.replace('/tmp/', '/') : url.pathname
    const file = normalize(join(fileRoot, path))
    if (!file.startsWith(fileRoot)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', file.endsWith('.css') ? 'text/css' : 'text/javascript')
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

test('global CSS reserves app shell geometry before custom elements upgrade', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/fallback`)

    const state = await shellGeometry(page)

    expect(state.shell.display).toBe('grid')
    expect(state.shell.x).toBe(0)
    expect(state.shell.width).toBe(1320)
    expect(state.shell.height).toBe(900)
    expect(state.route.display).toBe('block')
    expect(state.route.x).toBe(248)
    expect(state.route.width).toBe(1072)
    expect(state.route.height).toBe(900)
  } finally {
    await page.close()
  }
})

test('app shell preserves slotted route geometry before route component upgrade', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/upgraded-shell`)
    await page.waitForFunction(() => customElements.get('lv-app-shell'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const state = await shellGeometry(page)

    expect(state.routeDefined).toBe(false)
    expect(state.shell.display).toBe('grid')
    expect(state.route.display).toBe('block')
    expect(state.route.x).toBe(248)
    expect(state.route.width).toBe(1072)
    expect(state.route.height).toBe(900)
  } finally {
    await page.close()
  }
})

test('app shell renders a restrained text-only LeapView identity', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/upgraded-shell`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    const identity = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      return {
        navigationLabel: root.querySelector('aside')?.getAttribute('aria-label'),
        name: root.querySelector('.brand .name')?.textContent?.trim(),
        mobileName: root.querySelector('.mobile-drawer-title')?.textContent?.trim(),
        markCount: root.querySelectorAll('lv-brand-mark').length,
      }
    })

    expect(identity).toEqual({
      navigationLabel: 'LeapView navigation',
      name: 'LeapView',
      mobileName: 'LeapView',
      markCount: 0,
    })
  } finally {
    await page.close()
  }
})

test('app shell renders custom identity with permanent LeapView attribution', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/upgraded-shell`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    const identity = await page.locator('lv-app-shell').evaluate(async (element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as any
      sidebar.config = { ...sidebar.config, productName: 'Northstar Analytics', productLogoUrl: '/instance-logo.png' }
      await sidebar.updateComplete
      const root = sidebar.shadowRoot!
      return {
        navigationLabel: root.querySelector('aside')?.getAttribute('aria-label'),
        name: root.querySelector('.brand .name')?.textContent?.trim(),
        logo: root.querySelector('.product-logo')?.getAttribute('src'),
        attribution: root.querySelector('.powered-by')?.textContent?.trim(),
        attributionHref: root.querySelector('.powered-by')?.getAttribute('href'),
      }
    })
    expect(identity).toEqual({
      navigationLabel: 'Northstar Analytics navigation',
      name: 'Northstar Analytics',
      logo: '/instance-logo.png',
      attribution: 'Powered by LeapView',
      attributionHref: 'https://leapview.dev',
    })
  } finally {
    await page.close()
  }
})

test('desktop sidebar exposes an accessible persisted resize handle', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/admin-sidebar`)
    await page.evaluate(() => localStorage.removeItem('leapview-admin-sidebar-width'))
    await page.reload()
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))

    const initial = await page.locator('lv-app-shell').evaluate(async (element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as any
      await sidebar.updateComplete
      const handle = sidebar.shadowRoot.querySelector('.resize-handle') as HTMLElement
      handle.focus()
      handle.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }))
      await sidebar.updateComplete
      return {
        label: handle.getAttribute('aria-label'),
        orientation: handle.getAttribute('aria-orientation'),
        role: handle.getAttribute('role'),
        tabIndex: handle.tabIndex,
        value: handle.getAttribute('aria-valuenow'),
      }
    })

    expect(initial).toEqual({
      label: 'Resize navigation sidebar',
      orientation: 'vertical',
      role: 'separator',
      tabIndex: 0,
      value: '200',
    })
    await page.waitForFunction(() => {
      const shell = document.querySelector('lv-app-shell') as HTMLElement
      const sidebar = shell?.shadowRoot?.querySelector('lv-sidebar') as HTMLElement
      return Math.round(sidebar?.getBoundingClientRect().width ?? 0) === 200
    })

    const handleBox = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const handle = sidebar.shadowRoot!.querySelector('.resize-handle') as HTMLElement
      const box = handle.getBoundingClientRect()
      return { x: box.x, y: box.y, width: box.width, height: box.height }
    })
    await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + Math.min(80, handleBox.height / 2))
    await page.mouse.down()
    await page.mouse.move(handleBox.x + handleBox.width / 2 + 32, handleBox.y + Math.min(80, handleBox.height / 2))
    await page.mouse.up()

    const resized = await shellGeometry(page)
    expect(resized.sidebar.width).toBe(232)
    expect(resized.shellMain.x).toBe(resized.sidebar.right)

    await page.reload()
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.waitForFunction(() => {
      const shell = document.querySelector('lv-app-shell') as HTMLElement
      const sidebar = shell?.shadowRoot?.querySelector('lv-sidebar') as HTMLElement
      return Math.round(sidebar?.getBoundingClientRect().width ?? 0) === 232
    })
    const persistedWidth = (await shellGeometry(page)).sidebar.width
    expect(persistedWidth).toBe(232)
  } finally {
    await page.evaluate(() => localStorage.removeItem('leapview-admin-sidebar-width')).catch(() => undefined)
    await page.close()
  }
})

test('compact app shell keeps the primary sidebar collapsible', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/upgraded-compact-shell`)
    await page.evaluate(() => localStorage.setItem('leapview-sidebar-collapsed', 'true'))
    await page.reload()
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.waitForFunction(() => (document.querySelector('lv-app-shell') as any)?.chrome?.sidebar?.compact === true)
    await page.waitForFunction(() => ((document.querySelector('lv-app-shell') as any)?.shadowRoot?.querySelector('lv-sidebar') as any)?.config?.compact === true)
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const state = await shellGeometry(page)
    const compactIdentity = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      return {
        name: root.querySelector('.brand .name')?.textContent?.trim() ?? null,
        nameDisplay: getComputedStyle(root.querySelector('.brand .name') as HTMLElement).display,
        markCount: root.querySelectorAll('lv-brand-mark').length,
        collapseControl: (() => {
          const button = root.querySelector('.collapse-button') as HTMLButtonElement | null
          return button ? { label: button.getAttribute('aria-label'), disabled: button.disabled } : null
        })(),
        collapsedAttribute: sidebar.hasAttribute('data-collapsed'),
        visibleAreaSwitcherCount: Array.from(root.querySelectorAll('.area-switcher')).filter((item) => {
          const rect = item.getBoundingClientRect()
          const style = getComputedStyle(item)
          return rect.width > 0 && rect.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'
        }).length,
      }
    })

    expect(state.routeDefined).toBe(false)
    expect(state.sidebar.width).toBe(48)
    expect(state.shellMain.x).toBe(state.sidebar.right)
    expect(state.route.x).toBe(state.sidebar.right)
    expect(state.route.gridColumnStart).toBe('auto')
    expect(compactIdentity).toEqual({
      name: 'LeapView',
      nameDisplay: 'none',
      markCount: 0,
      collapseControl: { label: 'Expand navigation', disabled: false },
      collapsedAttribute: true,
      visibleAreaSwitcherCount: 0,
    })

    await page.locator('lv-app-shell').evaluate(async (element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as any
      const button = sidebar.shadowRoot.querySelector('.collapse-button') as HTMLButtonElement
      button.click()
      await sidebar.updateComplete
    })
    const expanded = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      const button = root.querySelector('.collapse-button') as HTMLButtonElement
      return {
        width: Math.round(sidebar.getBoundingClientRect().width),
        name: root.querySelector('.brand .name')?.textContent?.trim() ?? null,
        label: button.getAttribute('aria-label'),
        collapsedAttribute: sidebar.hasAttribute('data-collapsed'),
      }
    })
    expect(expanded).toEqual({ width: expect.any(Number), name: 'LeapView', label: 'Collapse navigation', collapsedAttribute: false })
    expect(expanded.width).toBeGreaterThan(48)
  } finally {
    await page.close()
  }
})

test('mobile navigation opens in an accessible drawer', async () => {
  const page = await browser.newPage({ viewport: { width: 553, height: 793 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.evaluate(() => localStorage.setItem('leapview-sidebar-collapsed', 'true'))
    await page.reload()
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot
      const nav = root.querySelector('nav') as HTMLElement
      const main = element.shadowRoot.querySelector('main') as HTMLElement
      const menuButton = root.querySelector('.mobile-menu-button') as HTMLButtonElement
      const sidebarBox = sidebar.getBoundingClientRect()
      const mainBox = main.getBoundingClientRect()
      return {
        documentOverflow: document.documentElement.scrollHeight - window.innerHeight,
        sidebarWidth: Math.round(sidebarBox.width),
        mainX: Math.round(mainBox.x),
        mainY: Math.round(mainBox.y),
        sidebarBottom: Math.round(sidebarBox.bottom),
        menu: {
          display: getComputedStyle(menuButton).display,
          expanded: menuButton.getAttribute('aria-expanded'),
        },
        mobileHeader: {
          display: getComputedStyle(root.querySelector('.mobile-header')).display,
          title: root.querySelector('.mobile-header-title')?.textContent?.trim(),
          containsMenu: Boolean(root.querySelector('.mobile-header')?.contains(menuButton)),
        },
        navVisibility: getComputedStyle(nav).visibility,
        navInert: nav.inert,
      }
    })

    expect(state.documentOverflow).toBe(0)
    expect(state.sidebarWidth).toBe(553)
    expect(state.mainX).toBe(0)
    expect(state.mainY).toBe(state.sidebarBottom)
    expect(state.menu.display).not.toBe('none')
    expect(state.menu.expanded).toBe('false')
    expect(state.mobileHeader).toEqual({ display: 'flex', title: 'LeapView', containsMenu: true })
    expect(state.navVisibility).toBe('hidden')
    expect(state.navInert).toBe(true)

    await page.locator('lv-app-shell').evaluate(async (element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot
      ;(root.querySelector('.mobile-menu-button') as HTMLButtonElement).click()
      await sidebar.updateComplete
    })
    await page.waitForFunction(() => {
      const shell = document.querySelector('lv-app-shell') as HTMLElement
      const sidebar = shell.shadowRoot?.querySelector('lv-sidebar') as HTMLElement
      const nav = sidebar.shadowRoot?.querySelector('nav') as HTMLElement
      return getComputedStyle(nav).visibility === 'visible'
    })

    const openState = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot
      const nav = root.querySelector('nav') as HTMLElement
      const menuButton = root.querySelector('.mobile-menu-button') as HTMLButtonElement
      const backdrop = root.querySelector('.mobile-backdrop') as HTMLButtonElement
      const drawerHeader = root.querySelector('.mobile-drawer-header') as HTMLElement
      const drawer = root.querySelector('aside') as HTMLElement
      const visible = (target: Element) => {
        const box = target.getBoundingClientRect()
        const style = getComputedStyle(target)
        return box.width > 0 && box.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'
      }
      const mobileSettings = root.querySelector('.mobile-footer .user-card') as HTMLAnchorElement | null
      return {
        drawerOpen: root.querySelector('aside')?.hasAttribute('data-mobile-open'),
        expanded: menuButton.getAttribute('aria-expanded'),
        navVisibility: getComputedStyle(nav).visibility,
        navInert: nav.inert,
        backdropVisibility: getComputedStyle(backdrop).visibility,
        drawerBackground: getComputedStyle(drawer).backgroundColor,
        navBackground: getComputedStyle(nav).backgroundColor,
        headerBorderBottomWidth: getComputedStyle(drawerHeader).borderBottomWidth,
        navBoxShadow: getComputedStyle(nav).boxShadow,
        closeControlCount: root.querySelectorAll('button[aria-label="Close navigation"]:not([inert])').length,
        visibleAreaSwitcherCount: Array.from(root.querySelectorAll('.area-switcher')).filter(visible).length,
        mobileSettings: mobileSettings && visible(mobileSettings) ? {
          href: mobileSettings.getAttribute('href'),
          label: mobileSettings.getAttribute('aria-label'),
        } : null,
      }
    })

    expect(openState.drawerOpen).toBe(true)
    expect(openState.expanded).toBe('true')
    expect(openState.navVisibility).toBe('visible')
    expect(openState.navInert).toBe(false)
    expect(openState.backdropVisibility).toBe('visible')
    expect(openState.navBackground).toBe(openState.drawerBackground)
    expect(openState.headerBorderBottomWidth).not.toBe('0px')
    expect(openState.navBoxShadow).not.toBe('none')
    expect(openState.closeControlCount).toBe(1)
    expect(openState.visibleAreaSwitcherCount).toBe(1)
    expect(openState.mobileSettings).toEqual({ href: '/admin/profile', label: 'Open settings for Current User' })

    await page.locator('lv-app-shell').evaluate(async (element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      await sidebar.updateComplete
    })
    await page.waitForFunction(() => {
      const shell = document.querySelector('lv-app-shell') as HTMLElement
      const sidebar = shell.shadowRoot?.querySelector('lv-sidebar') as HTMLElement
      const nav = sidebar.shadowRoot?.querySelector('nav') as HTMLElement
      return getComputedStyle(nav).visibility === 'hidden'
    })

    const closedState = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot
      const nav = root.querySelector('nav') as HTMLElement
      const menuButton = root.querySelector('.mobile-menu-button') as HTMLButtonElement
      return {
        expanded: menuButton.getAttribute('aria-expanded'),
        navInert: nav.inert,
      }
    })

    expect(closedState.expanded).toBe('false')
    expect(closedState.navInert).toBe(true)
  } finally {
    await page.close()
  }
})

test('sidebar renders global chat action and recent history', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot
      return {
        links: Array.from(root.querySelectorAll('a')).map((link: any) => ({
          href: link.getAttribute('href'),
          text: link.textContent.trim(),
          current: link.getAttribute('aria-current'),
          ariaLabel: link.getAttribute('aria-label'),
          title: link.getAttribute('title'),
        })),
        spacing: (() => {
          const group = root.querySelector('.nav-group:not(.primary-action)') as HTMLElement
          const navItem = root.querySelector('a[href="/"]') as HTMLElement
          const historyList = root.querySelector('.history-list') as HTMLElement
          return {
            navGroupGap: getComputedStyle(group).gap,
            historyListGap: getComputedStyle(historyList).gap,
            navItemHeight: Math.round(navItem.getBoundingClientRect().height),
          }
        })(),
        primaryStyle: (() => {
          const link = root.querySelector('.primary-action .nav-item') as HTMLElement
          const icon = root.querySelector('.primary-action .nav-icon') as HTMLElement
          return {
            background: getComputedStyle(link).backgroundColor,
            color: getComputedStyle(link).color,
            iconBackground: getComputedStyle(icon).backgroundColor,
            iconRadius: getComputedStyle(icon).borderRadius,
          }
        })(),
        historyLabel: root.querySelector('.history-label')?.textContent?.trim(),
        historySpinner: (() => {
          const spinner = root.querySelector('lv-loading-spinner') as HTMLElement | null
          return {
            present: Boolean(spinner),
            label: spinner?.getAttribute('aria-label'),
          }
        })(),
        hasHistorySearch: Boolean(root.querySelector('.history-search')),
        historyStyle: (() => {
          const history = root.querySelector('.history') as HTMLElement
          const style = getComputedStyle(history)
          return {
            borderTopWidth: style.borderTopWidth,
            paddingTop: style.paddingTop,
          }
        })(),
        historyItemMetrics: (() => {
          const item = root.querySelector('.history-item') as HTMLElement
          const title = item?.querySelector('.history-title') as HTMLElement
          const navIcon = root.querySelector('a[href="/"] .nav-icon') as HTMLElement
          const navText = root.querySelector('a[href="/"] .nav-text') as HTMLElement
          const label = root.querySelector('.history-label') as HTMLElement
          const mutedProbe = document.createElement('span')
          mutedProbe.style.color = 'var(--lv-fg-muted)'
          root.append(mutedProbe)
          const mutedColor = getComputedStyle(mutedProbe).color
          mutedProbe.remove()
          return {
            gridTemplateColumns: getComputedStyle(item).gridTemplateColumns,
            labelLeft: Math.round(label.getBoundingClientRect().left),
            titleLeft: Math.round(title.getBoundingClientRect().left),
            navIconLeft: Math.round(navIcon.getBoundingClientRect().left),
            navTextLeft: Math.round(navText.getBoundingClientRect().left),
            titleWidth: Math.round(title.getBoundingClientRect().width),
            titleScrollWidth: title.scrollWidth,
            labelColor: getComputedStyle(label).color,
            mutedColor,
          }
        })(),
      }
    })

    expect(state.historyLabel).toBe('Chats')
    expect(state.historySpinner).toEqual({ present: true, label: 'Title loading' })
    expect(state.links).toContainEqual({ href: '/chats/new', text: 'New chat', current: 'false', ariaLabel: 'New chat', title: 'New chat' })
    expect(state.links).toContainEqual({ href: '/chats/c1', text: 'Revenue check', current: 'page', ariaLabel: 'Revenue check', title: 'Revenue check' })
    expect(state.spacing).toEqual({ navGroupGap: '2px', historyListGap: '2px', navItemHeight: 32 })
    expect(state.hasHistorySearch).toBe(false)
    expect(state.historyStyle).toEqual({ borderTopWidth: '0px', paddingTop: '8px' })
    expect(state.historyItemMetrics.gridTemplateColumns).not.toMatch(/^26px /)
    expect(state.historyItemMetrics.labelLeft).toBe(state.historyItemMetrics.navIconLeft)
    expect(state.historyItemMetrics.titleLeft).toBe(state.historyItemMetrics.navIconLeft)
    expect(state.historyItemMetrics.titleLeft).toBeLessThan(state.historyItemMetrics.navTextLeft)
    expect(state.historyItemMetrics.titleWidth).toBeGreaterThanOrEqual(state.historyItemMetrics.titleScrollWidth)
    expect(state.historyItemMetrics.labelColor).not.toBe(state.historyItemMetrics.mutedColor)
    expect(state.primaryStyle.background).toBe('rgba(0, 0, 0, 0)')
    expect(state.primaryStyle.iconBackground).not.toBe('rgba(0, 0, 0, 0)')
    expect(state.primaryStyle.iconRadius).not.toBe('0px')
  } finally {
    await page.close()
  }
})

test('sidebar active nav item uses a full-row highlight without selector rail', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-active-nav`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot
      const active = root.querySelector('a[href="/data"]') as HTMLElement
      const icon = active.querySelector('.nav-icon') as HTMLElement
      const style = getComputedStyle(active)
      const iconStyle = getComputedStyle(icon)
      const before = getComputedStyle(active, '::before')
      return {
        text: active.textContent.trim(),
        label: active.getAttribute('aria-label'),
        title: active.getAttribute('title'),
        current: active.getAttribute('aria-current'),
        background: style.backgroundColor,
        controlHoverBackground: getComputedStyle(document.documentElement).getPropertyValue('--control-bgColor-hover').trim(),
        border: style.borderTopColor,
        iconBackground: iconStyle.backgroundColor,
        beforeContent: before.content,
        beforeWidth: before.width,
      }
    })

    expect(state.text).toBe('Data')
    expect(state.label).toBe('Data')
    expect(state.title).toBe('Data')
    expect(state.current).toBe('page')
    expect(state.background).toBe('rgb(239, 242, 245)')
    expect(state.controlHoverBackground).toBe('#eff2f5')
    expect(state.border).toBe('rgba(0, 0, 0, 0)')
    expect(state.iconBackground).toBe('rgba(0, 0, 0, 0)')
    expect(state.beforeContent).toBe('none')
    expect(state.beforeWidth).toBe('auto')
  } finally {
    await page.close()
  }
})

test('admin sidebar replaces global navigation and provides a back to app action', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/admin-sidebar`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)
    await page.waitForFunction(() => {
      const shell = document.querySelector('lv-app-shell') as HTMLElement
      const sidebar = shell?.shadowRoot?.querySelector('lv-sidebar') as HTMLElement | null
      return sidebar?.hasAttribute('data-admin') && Math.round(sidebar.getBoundingClientRect().width) === 192
    })

    const state = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      return {
        adminMode: sidebar.hasAttribute('data-admin'),
        width: Math.round(sidebar.getBoundingClientRect().width),
        links: Array.from(root.querySelectorAll('a')).map((link: any) => ({
          href: link.getAttribute('href'),
          text: link.textContent.trim(),
          current: link.getAttribute('aria-current'),
        })),
        groupLabels: Array.from(root.querySelectorAll('.nav-group:not(.primary-action)')).map((group) => group.getAttribute('aria-label')),
        visibleGroupLabels: Array.from(root.querySelectorAll('.nav-group-label')).map((label) => label.textContent?.trim()),
        brandAction: (() => {
          const action = root.querySelector('.brand-back') as HTMLAnchorElement | null
          return action ? { href: action.getAttribute('href'), text: action.textContent.trim() } : null
        })(),
        brandItemStyle: (() => {
          const brand = root.querySelector('.brand-back') as HTMLElement
          const navItem = root.querySelector('a[href="/admin/groups"]') as HTMLElement
          const brandStyle = getComputedStyle(brand)
          const navStyle = getComputedStyle(navItem)
          return {
            brand: {
              color: brandStyle.color,
              fontSize: brandStyle.fontSize,
              fontWeight: brandStyle.fontWeight,
              height: Math.round(brand.getBoundingClientRect().height),
            },
            nav: {
              color: navStyle.color,
              fontSize: navStyle.fontSize,
              fontWeight: navStyle.fontWeight,
              height: Math.round(navItem.getBoundingClientRect().height),
            },
          }
        })(),
        search: (() => {
          const input = root.querySelector('.brand .sidebar-search input') as HTMLInputElement
          const search = root.querySelector('.brand .sidebar-search') as HTMLElement
          return {
            display: getComputedStyle(search).display,
            placeholder: input.getAttribute('placeholder'),
            ariaLabel: input.getAttribute('aria-label'),
          }
        })(),
        hasLeapViewName: Boolean(root.querySelector('.brand .name')),
        collapseControlCount: root.querySelectorAll('.collapse-button').length,
        hasNavPrimaryAction: Boolean(root.querySelector('.primary-action')),
        hasHistory: Boolean(root.querySelector('.history')),
        hasThemeToggle: Boolean(root.querySelector('.theme-button, [data-theme-toggle]')),
        currentUser: (() => {
          const card = root.querySelector('.user-card') as HTMLElement
          const avatar = card.querySelector('lv-user-avatar') as any
          return {
            title: card.getAttribute('title'),
            name: card.querySelector('.user-name')?.textContent?.trim(),
            initials: avatar?.shadowRoot?.textContent?.trim(),
            avatarSrc: avatar?.shadowRoot?.querySelector('img')?.getAttribute('src'),
            role: card.querySelector('.user-role')?.textContent?.trim(),
          }
        })(),
      }
    })

    expect(state.groupLabels).toEqual(['Personal', 'Product', 'Access', 'Data & sharing', 'Operations'])
    expect(state.adminMode).toBe(true)
    expect(state.width).toBe(192)
    expect(state.visibleGroupLabels).toEqual(['Personal', 'Product', 'Access', 'Data & sharing', 'Operations'])
    expect(state.links).toEqual(expect.arrayContaining([
      { href: '/admin/profile', text: 'Profile', current: 'false' },
      { href: '/admin/principals', text: 'Principals', current: 'page' },
      { href: '/admin/groups', text: 'Groups', current: 'false' },
      { href: '/admin/agent', text: 'Agent', current: 'false' },
      { href: '/admin/storage', text: 'Storage', current: 'false' },
      { href: '/admin/queries', text: 'Query history', current: 'false' },
      { href: '/admin/publications', text: 'Publications', current: 'false' },
    ]))
    expect(state.brandAction).toEqual({ href: '/', text: 'Back to app' })
    expect(state.brandItemStyle.brand).toEqual(state.brandItemStyle.nav)
    expect(state.search).toEqual({ display: 'grid', placeholder: 'Search...', ariaLabel: 'Search admin navigation' })
    expect(state.hasLeapViewName).toBe(false)
    expect(state.collapseControlCount).toBe(0)
    expect(state.hasNavPrimaryAction).toBe(false)
    expect(state.hasHistory).toBe(false)
    expect(state.hasThemeToggle).toBe(false)
    expect(state.currentUser).toEqual({
      title: 'Ada Lovelace', name: 'Ada Lovelace', initials: '',
      avatarSrc: '/profile/avatars/ada/avatar-digest', role: 'Platform admin',
    })

    const updatedAvatarSrc = await page.locator('lv-app-shell').evaluate(async (element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement & { updateComplete: Promise<unknown> }
      document.dispatchEvent(new CustomEvent('leapview-avatar-change', { detail: { url: '/profile/avatars/ada/new-digest' } }))
      await sidebar.updateComplete
      return (sidebar.shadowRoot!.querySelector('lv-user-avatar') as any)?.shadowRoot?.querySelector('img')?.getAttribute('src')
    })
    expect(updatedAvatarSrc).toBe('/profile/avatars/ada/new-digest')

    await page.locator('lv-app-shell').evaluate(async (element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement & { updateComplete: Promise<unknown> }
      const input = sidebar.shadowRoot!.querySelector('.brand .sidebar-search input') as HTMLInputElement
      input.value = 'storage'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await sidebar.updateComplete
    })
    const filtered = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      return {
        groupLabels: Array.from(root.querySelectorAll('.nav-group:not(.primary-action)')).map((group) => group.getAttribute('aria-label')),
        links: Array.from(root.querySelectorAll('#mobile-navigation a[href^="/admin/"]')).map((link) => link.getAttribute('href')),
      }
    })
    expect(filtered).toEqual({ groupLabels: ['Data & sharing'], links: ['/admin/storage'] })
  } finally {
    await page.close()
  }
})

test('sidebar switches between Insights and Develop and remembers the last area location', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-active-nav`)
    await page.evaluate(() => {
      localStorage.removeItem('leapview-area-last-insights')
      localStorage.removeItem('leapview-area-last-develop')
    })
    await page.reload()
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))

    const developState = await sidebarAreaState(page)
    expect(developState.area).toBe('develop')
    expect(developState.areas).toEqual([
      { label: 'Insights', current: 'false', href: '/' },
      { label: 'Develop', current: 'page', href: '/sidebar-active-nav' },
    ])
    expect(developState.items).toEqual(['Data', 'Models', 'Semantic models', 'Pipelines', 'Connections'])
    expect(developState.visibleGroupLabels).toEqual([])
    expect(developState.settings).toEqual({ href: '/admin/profile', label: 'Open settings for Current User' })
    expect(developState.visibleAreaSwitcherCount).toBe(1)
    expect(developState.currentAreaClickPrevented).toBe(true)
    expect(developState.switcherStyle).toEqual({ display: 'grid', borderTopWidth: '0px', backgroundColor: 'rgba(0, 0, 0, 0)' })
    expect(developState.currentAreaStyle.boxShadow).toBe('none')
    expect(developState.currentAreaStyle.backgroundColor).not.toBe(developState.switcherStyle.backgroundColor)
    expect(developState.areaIconDisplay).toBe('grid')

    await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      ;(sidebar.shadowRoot!.querySelector('.area-item[aria-label="Insights"]') as HTMLAnchorElement).click()
    })
    await page.waitForURL(`${baseURL}/`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))

    const insightsState = await sidebarAreaState(page)
    expect(insightsState.area).toBe('insights')
    expect(insightsState.items).toEqual(['Dashboards', 'Data Explorer'])
    expect(insightsState.visibleGroupLabels).toEqual([])

    await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      ;(sidebar.shadowRoot!.querySelector('.area-item[aria-label="Develop"]') as HTMLAnchorElement).click()
    })
    await page.waitForURL(`${baseURL}/sidebar-active-nav`)
  } finally {
    await page.close()
  }
})

test('insights and develop navigation expose the stable route contract without subtitles', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-active-nav`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    const navigationState = () => page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      const links = (group: string) => Array.from(root.querySelectorAll(`#mobile-navigation .nav-group[aria-label="${group}"] a`)).map((link: Element) => ({
        label: link.textContent?.trim(), href: link.getAttribute('href'),
      }))
      return {
        insights: links('Insights'),
        develop: links('Develop'),
        subtitles: Array.from(root.querySelectorAll('.nav-group-label')).filter((label: Element) => {
          const style = getComputedStyle(label)
          return style.display !== 'none' && style.visibility !== 'hidden'
        }).map((label: Element) => label.textContent?.trim()),
      }
    })
    const developState = await navigationState()
    expect(developState.insights).toEqual([])
    expect(developState.develop).toEqual([
      { label: 'Data', href: '/data' },
      { label: 'Models', href: '/models' },
      { label: 'Semantic models', href: '/semantic-models' },
      { label: 'Pipelines', href: '/pipelines' },
      { label: 'Connections', href: '/connections' },
    ])
    expect(developState.subtitles).toEqual([])

    await page.goto(`${baseURL}/`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    const insightsState = await navigationState()
    expect(insightsState.insights).toEqual([
      { label: 'Dashboards', href: '/' },
      { label: 'Data Explorer', href: '/explore' },
    ])
    expect(insightsState.develop).toEqual([])
    expect(insightsState.subtitles).toEqual([])
  } finally {
    await page.close()
  }
})

test('admin sidebar keeps chrome fixed while only navigation items scroll', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 520 } })
  try {
    await page.goto(`${baseURL}/admin-sidebar`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      const aside = root.querySelector('aside') as HTMLElement
      const nav = root.querySelector('nav') as HTMLElement
      const header = root.querySelector('.brand') as HTMLElement
      const footer = root.querySelector('.footer') as HTMLElement
      return {
        viewportHeight: window.innerHeight,
        documentHeight: document.documentElement.scrollHeight,
        sidebarHeight: Math.round(sidebar.getBoundingClientRect().height),
        asideHeight: Math.round(aside.getBoundingClientRect().height),
        navOverflowY: getComputedStyle(nav).overflowY,
        navScrolls: nav.scrollHeight > nav.clientHeight,
        headerTop: Math.round(header.getBoundingClientRect().top),
        footerBottom: Math.round(footer.getBoundingClientRect().bottom),
      }
    })

    expect(state).toEqual({
      viewportHeight: 520,
      documentHeight: 520,
      sidebarHeight: 520,
      asideHeight: 520,
      navOverflowY: 'auto',
      navScrolls: true,
      headerTop: 0,
      footerBottom: 520,
    })
  } finally {
    await page.close()
  }
})

test('app shell ignores synthetic file input clicks from page content', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/admin-sidebar`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: HTMLElement) => {
      const input = document.createElement('input')
      input.type = 'file'
      input.slot = 'page'
      element.append(input)
      input.click()
    })
    await page.waitForTimeout(100)

    expect(new URL(page.url()).pathname).toBe('/admin-sidebar')
  } finally {
    await page.close()
  }
})

test('mobile admin sidebar places the menu button beside the back to app title', async () => {
  const page = await browser.newPage({ viewport: { width: 553, height: 793 } })
  try {
    await page.goto(`${baseURL}/admin-sidebar`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      const header = root.querySelector('.mobile-header') as HTMLElement
      const menu = root.querySelector('.mobile-menu-button') as HTMLButtonElement
      return {
        title: root.querySelector('.mobile-header-title')?.textContent?.trim(),
        menuVisible: getComputedStyle(menu).display !== 'none',
        menuInHeader: header.contains(menu),
        collapseControlCount: root.querySelectorAll('.collapse-button').length,
      }
    })

    expect(state).toEqual({
      title: 'Back to app',
      menuVisible: true,
      menuInHeader: true,
      collapseControlCount: 0,
    })
  } finally {
    await page.close()
  }
})

test('active chat history item navigates to its conversation', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const link = page.locator('lv-app-shell lv-sidebar a[href="/chats/c1"]')
    expect(await link.count()).toBe(1)
    await link.click()
    await page.waitForURL(`${baseURL}/chats/c1`)

    expect(new URL(page.url()).pathname).toBe('/chats/c1')
  } finally {
    await page.close()
  }
})

test('app shell reads chrome from Datastar signals without a payload attribute', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/signal-shell`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.waitForFunction(() => (document.querySelector('lv-app-shell') as any)?.chrome?.sidebar?.active === 'chat')
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const state = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as any
      return {
        hasChromeAttr: element.hasAttribute('chrome'),
        active: element.chrome.sidebar.active,
        text: sidebar.shadowRoot.textContent.replace(/\s+/g, ' ').trim(),
      }
    })

    expect(state.hasChromeAttr).toBe(false)
    expect(state.active).toBe('chat')
    expect(state.text).toContain('Chats')
  } finally {
    await page.close()
  }
})

test('app shell routes retargeted sidebar clicks to the visual link', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const link = sidebar.shadowRoot.querySelector('a[href="/chats/c1"]') as HTMLElement
      const rect = link.getBoundingClientRect()
      element.dispatchEvent(new MouseEvent('click', {
        bubbles: true,
        composed: true,
        button: 0,
        clientX: rect.left + rect.width / 2,
        clientY: rect.top + rect.height / 2,
      }))
    })
    await page.waitForURL(`${baseURL}/chats/c1`)

    expect(new URL(page.url()).pathname).toBe('/chats/c1')
  } finally {
    await page.close()
  }
})


async function shellGeometry(page: any) {
  return await page.evaluate(() => {
    const shell = document.querySelector('lv-app-shell') as HTMLElement
    const route = document.querySelector('lv-route-page') as HTMLElement
    const sidebar = shell.shadowRoot?.querySelector('lv-sidebar') as HTMLElement
    const shellMain = shell.shadowRoot?.querySelector('main') as HTMLElement
    const box = (element?: HTMLElement | null) => {
      if (!element) return null
      const rect = element.getBoundingClientRect()
      const style = getComputedStyle(element)
      return {
        x: Math.round(rect.x),
        y: Math.round(rect.y),
        width: Math.round(rect.width),
        height: Math.round(rect.height),
        right: Math.round(rect.right),
        display: style.display,
        gridColumnStart: style.gridColumnStart,
      }
    }
    return {
      routeDefined: Boolean(customElements.get('lv-route-page')),
      shell: box(shell),
      sidebar: box(sidebar),
      shellMain: box(shellMain),
      route: box(route),
    }
  })
}

function signalShellDocument(): string {
  const signals = {
    chrome: {
      sidebar: {
        productName: 'LeapView',
        active: 'chat',
        area: 'insights',
        areas: [
          { id: 'insights', label: 'Insights', href: '/', icon: 'insights' },
          { id: 'develop', label: 'Develop', href: '/data', icon: 'code' },
        ],
        dashboardId: '',
        dashboardTitle: '',
        pageTitle: '',
        modelId: '',
        modelTitle: '',
        compact: false,
        userSettingsHref: '/admin/profile',
        groups: [{
          label: 'Navigation',
          items: [
            { id: 'dashboards', label: 'Dashboards', href: '/', icon: 'dashboard' },
            { id: 'chat', label: 'Chats', href: '/chats', icon: 'chat' },
          ],
        }],
      },
    },
  }
  return `
    <!doctype html>
    <html>
      <head>
        <link rel="stylesheet" href="/static/app.css">
      </head>
      <body>
        <main class="min-h-svh bg-app text-fg-default" data-signals="${escapeHTML(JSON.stringify(signals))}">
          <lv-app-shell>
            <lv-route-page slot="page"></lv-route-page>
          </lv-app-shell>
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/tmp/app-shell-under-test.js"></script>
      </body>
    </html>
  `
}

function testDocument(includeShellScript: boolean, compact = false, history = false, nav = false, admin = false): string {
  const chromeConfig = compact || history || nav || admin ? {
    sidebar: {
      productName: 'LeapView',
      active: admin ? 'principals' : history ? 'chat' : 'data',
      admin,
      area: admin ? undefined : history ? 'insights' : 'develop',
      areas: admin ? undefined : [
        { id: 'insights', label: 'Insights', href: '/', icon: 'insights' },
        { id: 'develop', label: 'Develop', href: '/data', icon: 'code' },
      ],
      dashboardId: '',
      dashboardTitle: '',
      pageTitle: '',
      modelId: '',
      modelTitle: '',
      compact,
      userName: admin ? 'Ada Lovelace' : 'Current User',
      userAvatarUrl: admin ? '/profile/avatars/ada/avatar-digest' : undefined,
      userRole: admin ? 'Platform admin' : 'Member',
      userSettingsHref: '/admin/profile',
      primaryAction: admin ? { label: 'Back to app', href: '/', icon: 'back' } : history ? { label: 'New chat', href: '/chats/new', icon: 'plus' } : undefined,
      history: history ? {
        label: 'Chats',
        emptyText: 'No conversations yet.',
        items: [
          { id: 'c1', title: 'Revenue check', href: '/chats/c1', active: true, pending: true },
          { id: 'c2', title: 'Inventory status', href: '/chats/c2' },
        ],
      } : undefined,
      groups: admin ? [
        {
          label: 'Personal',
          items: [
            { id: 'profile', label: 'Profile', href: '/admin/profile', icon: 'user' },
            { id: 'security', label: 'Security & sessions', href: '/admin/security', icon: 'activity' },
            { id: 'api-tokens', label: 'API tokens', href: '/admin/api-tokens', icon: 'data' },
          ],
        },
        {
          label: 'Product',
          items: [
            { id: 'general', label: 'General', href: '/admin/general', icon: 'settings' },
            { id: 'projects-admin', label: 'Projects', href: '/admin/projects', icon: 'catalog' },
          ],
        },
        {
          label: 'Access',
          items: [
            { id: 'principals', label: 'Principals', href: '/admin/principals', icon: 'users' },
            { id: 'groups', label: 'Groups', href: '/admin/groups', icon: 'users-round' },
            { id: 'service-accounts', label: 'Service accounts', href: '/admin/service-accounts', icon: 'bot' },
            { id: 'authentication', label: 'Authentication', href: '/admin/authentication', icon: 'system' },
          ],
        },
        {
          label: 'Data & sharing',
          items: [
            { id: 'storage', label: 'Storage', href: '/admin/storage', icon: 'database' },
            { id: 'publications', label: 'Publications', href: '/admin/publications', icon: 'globe' },
          ],
        },
        {
          label: 'Operations',
          items: [
            { id: 'agent', label: 'Agent', href: '/admin/agent', icon: 'bot' },
            { id: 'queries', label: 'Query history', href: '/admin/queries', icon: 'history' },
            { id: 'audit', label: 'Audit log', href: '/admin/audit', icon: 'activity' },
            { id: 'system', label: 'System', href: '/admin/system', icon: 'system' },
          ],
        },
      ] : history ? [{
        label: 'Insights',
        items: [
          { id: 'dashboards', label: 'Dashboards', href: '/', icon: 'dashboard' },
          { id: 'data-explorer', label: 'Data Explorer', href: '/explore', icon: 'database' },
        ],
      }] : nav ? [{
        label: 'Develop',
        items: [
          { id: 'data', label: 'Data', href: '/data', icon: 'database' },
          { id: 'models', label: 'Models', href: '/models', icon: 'model' },
          { id: 'semantic-models', label: 'Semantic models', href: '/semantic-models', icon: 'model' },
          { id: 'pipelines', label: 'Pipelines', href: '/pipelines', icon: 'workflow' },
          { id: 'connections', label: 'Connections', href: '/connections', icon: 'data' },
        ],
      }] : [],
    },
  } : null
  const signals = chromeConfig ? ` data-signals="${escapeHTML(JSON.stringify({ chrome: chromeConfig }))}"` : ''
  return `
    <!doctype html>
    <html>
      <head>
        <link rel="stylesheet" href="/static/app.css">
        <style>
          :root {
            --control-bgColor-hover: #eff2f5;
            --lv-border-transparent: 1px solid transparent;
            --lv-border-muted: 1px solid #d8dee4;
            --lv-border-width: 1px;
            --lv-fg-muted: #57606a;
            --lv-shadow-floating: 0 8px 24px rgb(0 0 0 / 12%);
            --lv-spinner-size-md: 16px;
            --lv-spinner-size-sm: 10px;
            --lv-spinner-duration: 1800ms;
          }
        </style>
      </head>
      <body>
        <main class="min-h-svh bg-app text-fg-default"${signals}>
          <lv-app-shell>
            <lv-route-page slot="page"></lv-route-page>
          </lv-app-shell>
        </main>
        ${includeShellScript ? '<script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script><script type="module" src="/tmp/app-shell-under-test.js"></script>' : ''}
      </body>
    </html>
  `
}

async function sidebarAreaState(page: import('@playwright/test').Page) {
  return page.locator('lv-app-shell').evaluate((element: any) => {
    const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
    const root = sidebar.shadowRoot!
    return {
      area: sidebar.getAttribute('data-area'),
      areas: Array.from(root.querySelectorAll('.brand .area-item')).map((item) => ({
        label: item.getAttribute('aria-label'),
        current: item.getAttribute('aria-current'),
        href: item.getAttribute('href'),
      })),
      items: Array.from(root.querySelectorAll('#mobile-navigation > .nav-group:not(.primary-action) .nav-text strong')).map((item) => item.textContent?.trim()),
      visibleGroupLabels: Array.from(root.querySelectorAll('#mobile-navigation > .nav-group .nav-group-label')).filter((item) => {
        const style = getComputedStyle(item)
        return style.display !== 'none' && style.visibility !== 'hidden'
      }).map((item) => item.textContent?.trim()),
      settings: (() => {
        const link = root.querySelector('.user-card') as HTMLAnchorElement
        return { href: link.getAttribute('href'), label: link.getAttribute('aria-label') }
      })(),
      visibleAreaSwitcherCount: Array.from(root.querySelectorAll('.area-switcher')).filter((item) => {
        const box = item.getBoundingClientRect()
        const style = getComputedStyle(item)
        return box.width > 0 && box.height > 0 && style.display !== 'none' && style.visibility !== 'hidden'
      }).length,
      currentAreaClickPrevented: (() => {
        const current = root.querySelector('.brand .area-item[aria-current="page"]') as HTMLAnchorElement
        const event = new MouseEvent('click', { bubbles: true, cancelable: true, composed: true, button: 0 })
        current.dispatchEvent(event)
        return event.defaultPrevented
      })(),
      switcherStyle: (() => {
        const style = getComputedStyle(root.querySelector('.brand .area-switcher') as HTMLElement)
        return { display: style.display, borderTopWidth: style.borderTopWidth, backgroundColor: style.backgroundColor }
      })(),
      currentAreaStyle: (() => {
        const style = getComputedStyle(root.querySelector('.brand .area-item[aria-current="page"]') as HTMLElement)
        return { backgroundColor: style.backgroundColor, boxShadow: style.boxShadow }
      })(),
      areaIconDisplay: getComputedStyle(root.querySelector('.brand .area-icon') as HTMLElement).display,
    }
  })
}

function escapeHTML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}
