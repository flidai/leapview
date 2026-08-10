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
      navigationLabel: 'LeapView workspace',
      name: 'LeapView',
      mobileName: 'LeapView',
      markCount: 0,
    })
  } finally {
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
          const navItem = root.querySelector('a[href="/chats"]') as HTMLElement
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
          const navIcon = root.querySelector('a[href="/chats"] .nav-icon') as HTMLElement
          const navText = root.querySelector('a[href="/chats"] .nav-text') as HTMLElement
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
    expect(state.links).toContainEqual({ href: '/chats', text: 'Chats', current: 'page', ariaLabel: 'Chats', title: 'Chats' })
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
      const active = root.querySelector('a[href="/workspaces"]') as HTMLElement
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

    expect(state.text).toBe('Workspaces')
    expect(state.label).toBe('Workspaces')
    expect(state.title).toBe('Workspaces')
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

    const state = await page.locator('lv-app-shell').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sidebar') as HTMLElement
      const root = sidebar.shadowRoot!
      return {
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
      }
    })

    expect(state.groupLabels).toEqual(['Personal', 'Access', 'Data', 'Operations'])
    expect(state.visibleGroupLabels).toEqual(['Personal', 'Access', 'Data', 'Operations'])
    expect(state.links).toEqual(expect.arrayContaining([
      { href: '/admin/profile', text: 'Profile', current: 'false' },
      { href: '/admin/principals', text: 'Principals', current: 'page' },
      { href: '/admin/groups', text: 'Groups', current: 'false' },
      { href: '/admin/agent', text: 'Agent', current: 'false' },
      { href: '/admin/storage', text: 'Storage', current: 'false' },
      { href: '/admin/queries', text: 'Query History', current: 'false' },
      { href: '/admin/publications', text: 'Publications', current: 'false' },
    ]))
    expect(state.brandAction).toEqual({ href: '/', text: 'Back to app' })
    expect(state.brandItemStyle.brand).toEqual(state.brandItemStyle.nav)
    expect(state.search).toEqual({ display: 'grid', placeholder: 'Search...', ariaLabel: 'Search admin navigation' })
    expect(state.hasLeapViewName).toBe(false)
    expect(state.collapseControlCount).toBe(0)
    expect(state.hasNavPrimaryAction).toBe(false)
    expect(state.hasHistory).toBe(false)

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
        links: Array.from(root.querySelectorAll('a[href^="/admin/"]')).map((link) => link.getAttribute('href')),
      }
    })
    expect(filtered).toEqual({ groupLabels: ['Data'], links: ['/admin/storage'] })
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

test('active chat nav item navigates to the chat list href', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    await page.locator('lv-app-shell').evaluate((element: any) => element.updateComplete)

    const link = page.locator('lv-app-shell lv-sidebar a[href="/chats"]')
    expect(await link.count()).toBe(1)
    await link.click()
    await page.waitForURL(`${baseURL}/chats`)

    expect(new URL(page.url()).pathname).toBe('/chats')
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
      const link = sidebar.shadowRoot.querySelector('a[href="/chats"]') as HTMLElement
      const rect = link.getBoundingClientRect()
      element.dispatchEvent(new MouseEvent('click', {
        bubbles: true,
        composed: true,
        button: 0,
        clientX: rect.left + rect.width / 2,
        clientY: rect.top + rect.height / 2,
      }))
    })
    await page.waitForURL(`${baseURL}/chats`)

    expect(new URL(page.url()).pathname).toBe('/chats')
  } finally {
    await page.close()
  }
})


async function shellGeometry(page: any) {
  return await page.evaluate(() => {
    const shell = document.querySelector('lv-app-shell') as HTMLElement
    const route = document.querySelector('lv-workspace-page') as HTMLElement
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
      routeDefined: Boolean(customElements.get('lv-workspace-page')),
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
        workspaceTitle: 'LeapView Workspace',
        active: 'chat',
        dashboardId: '',
        dashboardTitle: '',
        pageTitle: '',
        modelId: '',
        modelTitle: '',
        compact: false,
        groups: [{
          label: 'Navigation',
          items: [
            { id: 'dashboards', label: 'Dashboards', href: '/', icon: 'dashboard' },
            { id: 'chat', label: 'Chats', href: '/chats', icon: 'chat' },
            { id: 'workspaces', label: 'Workspaces', href: '/workspaces', icon: 'catalog' },
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
            <lv-workspace-page slot="page"></lv-workspace-page>
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
      workspaceTitle: 'LeapView Workspace',
      active: admin ? 'principals' : history ? 'chat' : 'workspaces',
      admin,
      dashboardId: '',
      dashboardTitle: '',
      pageTitle: '',
      modelId: '',
      modelTitle: '',
      compact,
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
          ],
        },
        {
          label: 'Access',
          items: [
            { id: 'principals', label: 'Principals', href: '/admin/principals', icon: 'users' },
            { id: 'groups', label: 'Groups', href: '/admin/groups', icon: 'users-round' },
          ],
        },
        {
          label: 'Data',
          items: [
            { id: 'storage', label: 'Storage', href: '/admin/storage', icon: 'database' },
            { id: 'publications', label: 'Publications', href: '/admin/publications', icon: 'globe' },
          ],
        },
        {
          label: 'Operations',
          items: [
            { id: 'agent', label: 'Agent', href: '/admin/agent', icon: 'bot' },
            { id: 'queries', label: 'Query History', href: '/admin/queries', icon: 'history' },
          ],
        },
      ] : history || nav ? [{
        label: 'Navigation',
        items: [
          { id: 'dashboards', label: 'Dashboards', href: '/', icon: 'dashboard' },
          { id: 'chat', label: 'Chats', href: '/chats', icon: 'chat' },
          { id: 'workspaces', label: 'Workspaces', href: '/workspaces', icon: 'catalog' },
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
            <lv-workspace-page slot="page"></lv-workspace-page>
          </lv-app-shell>
        </main>
        ${includeShellScript ? '<script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script><script type="module" src="/tmp/app-shell-under-test.js"></script>' : ''}
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
