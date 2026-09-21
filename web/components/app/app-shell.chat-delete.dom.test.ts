import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testDocument } from './app-shell.dom.fixture'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const tmpRoot = join(projectRoot, '.tmp/app-shell-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/sidebar-history') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true, false, true, false, false, url.searchParams.getAll('deleted')))
      return
    }
    if (url.pathname === '/admin-sidebar') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true, true, false, false, true))
      return
    }
    if (url.pathname === '/chats/new') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(true, false, true))
      return
    }
    const fileRoot = url.pathname.startsWith('/tmp/') ? tmpRoot : projectRoot
    const requestPath = url.pathname.startsWith('/tmp/') ? url.pathname.replace('/tmp/', '/') : url.pathname
    const file = normalize(join(fileRoot, requestPath))
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

test('ordinary chat hover actions expose Pin and Delete without Archive or an overflow menu', async () => {
  const page = await browser.newPage()
  await page.goto(`${baseURL}/sidebar-history`)
  const row = page.locator('.history-row').filter({ hasText: 'Revenue check' })
  await row.hover()
  expect(await row.getByRole('button', { name: 'Pin Revenue check', exact: true }).count()).toBe(1)
  expect(await row.getByRole('button', { name: 'Delete Revenue check', exact: true }).count()).toBe(1)
  expect(await row.locator('summary[aria-label="More actions for Revenue check"]').count()).toBe(0)
  expect(await row.getByRole('button', { name: /Archive/ }).count()).toBe(0)
  await page.close()
})

test('delete confirmation keeps its danger color on hover', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.evaluate(() => {
      document.documentElement.style.setProperty('--lv-fg-danger', '#d1242f')
      document.documentElement.style.setProperty('--lv-bg-panel', '#ffffff')
      document.documentElement.style.setProperty('--lv-bg-panel-muted', '#f6f8fa')
    })
    await page.evaluate(() => document.querySelector('lv-app-shell')!.dispatchEvent(new CustomEvent('lv-chat-action', { detail: { action: 'delete', conversationId: 'c2', title: 'Inventory status' } })))
    const button = page.getByRole('dialog', { name: 'Delete chat?' }).getByRole('button', { name: 'Delete', exact: true })
    await button.waitFor()
    const resting = await button.evaluate((element) => getComputedStyle(element).backgroundColor)
    expect(resting).toBe('rgb(209, 36, 47)')
    await button.hover()
    const hovered = await button.evaluate((element) => ({ background: getComputedStyle(element).backgroundColor, filter: getComputedStyle(element).filter }))
    expect(hovered.background).toBe(resting)
    expect(hovered.filter).not.toBe('none')
  } finally {
    await page.close()
  }
})

test('account hover highlights the whole menu trigger', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.waitForFunction(() => customElements.get('lv-app-shell') && customElements.get('lv-sidebar'))
    const card = page.locator('lv-app-shell').locator('lv-sidebar').locator('.footer .user-card')
    await card.hover()
    const hovered = await card.evaluate((element) => {
      const icon = element.querySelector('.user-chevron') as HTMLElement
      return {
        cardBackground: getComputedStyle(element).backgroundColor,
        iconBackground: getComputedStyle(icon).backgroundColor,
      }
    })
    expect(hovered.cardBackground).not.toBe('rgba(0, 0, 0, 0)')
    expect(hovered.iconBackground).toBe('rgba(0, 0, 0, 0)')
  } finally {
    await page.close()
  }
})

test('chat deletion persists after the undo window, refreshes the list, and stays deleted on reload', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.evaluate(() => {
      sessionStorage.removeItem('lv-chat-manager.pending-undo')
      ;(window as any).chatActions = []
      ;(window as any).managementLoads = []
      document.addEventListener('lv-chat-management', (event: Event) => (window as any).chatActions.push((event as CustomEvent).detail))
      document.addEventListener('lv-chat-management-load', (event: Event) => (window as any).managementLoads.push((event as CustomEvent).detail))
      document.querySelector('lv-app-shell')!.dispatchEvent(new CustomEvent('lv-chat-action', { detail: { action: 'delete', conversationId: 'c2', title: 'Inventory status' } }))
    })
    await page.getByRole('dialog', { name: 'Delete chat?' }).getByRole('button', { name: 'Delete', exact: true }).click()
    await page.waitForFunction(() => (window as any).chatActions.length === 1)
    const pendingRequest = await page.evaluate(() => (window as any).chatActions[0].requestId)
    expect(await page.evaluate(() => (window as any).chatActions[0].action)).toBe('delete_pending')
    await page.evaluate((requestId) => {
      ;(window as any).testMergePatch({ chatManagement: { action: 'delete_pending', completedRequestId: requestId, undoDeadline: new Date(Date.now() + 80).toISOString(), archivedConversations: [] } })
    }, pendingRequest)
    await page.locator('button.undo[data-conversation-id="c2"]').waitFor()
    expect(await page.locator('a[href="/chats/c2"]').count()).toBe(0)
    await page.waitForFunction(() => (window as any).managementLoads.length === 1)
    const refreshRequest = await page.evaluate(() => (window as any).managementLoads[0].requestId)
    await page.evaluate((requestId) => {
      ;(window as any).testMergePatch({
        chatManagement: { action: '', completedRequestId: requestId, archivedConversations: [] },
        chrome: { sidebar: { history: { items: [
          { id: 'c1', title: 'Revenue check', href: '/chats/c1', active: true, pending: true },
          { id: 'c3', title: 'Pinned title loading', href: '/chats/c3', pending: true, pinned: true },
        ] } } },
      })
    }, refreshRequest)
    await page.waitForFunction(() => (window as any).chatActions.length === 1 && !(document.querySelector('lv-app-shell') as any).shadowRoot.querySelector('lv-sidebar').shadowRoot.querySelector('a[href="/chats/c2"]'))
    expect(await page.evaluate(() => sessionStorage.getItem('lv-chat-manager.pending-undo'))).toBeNull()
    await page.goto(`${baseURL}/sidebar-history?deleted=c2`)
    expect(await page.locator('a[href="/chats/c2"]').count()).toBe(0)
  } finally {
    await page.close()
  }
})

test('failed chat deletion keeps the conversation visible and reports the persistence failure', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.evaluate(() => {
      document.querySelector('lv-app-shell')!.dispatchEvent(new CustomEvent('lv-chat-action', { detail: { action: 'delete', conversationId: 'c2', title: 'Inventory status' } }))
    })
    await page.getByRole('dialog', { name: 'Delete chat?' }).getByRole('button', { name: 'Delete', exact: true }).click()
    await page.locator('lv-chat-manager').evaluate((element: any) => document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } })))
    await page.getByRole('alert').filter({ hasText: 'temporarily unavailable' }).waitFor()
    expect(await page.locator('a[href="/chats/c2"]').count()).toBe(1)
    expect(await page.locator('button.undo[data-conversation-id="c2"]').count()).toBe(0)
  } finally {
    await page.close()
  }
})

test('deleting the open chat returns to a new conversation after persistence commits', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.evaluate(() => {
      history.replaceState({}, '', '/chats/c2')
      ;(window as any).chatActions = []
      document.addEventListener('lv-chat-management', (event: Event) => (window as any).chatActions.push((event as CustomEvent).detail))
      document.querySelector('lv-app-shell')!.dispatchEvent(new CustomEvent('lv-chat-action', { detail: { action: 'delete', conversationId: 'c2', title: 'Inventory status' } }))
    })
    await page.getByRole('dialog', { name: 'Delete chat?' }).getByRole('button', { name: 'Delete', exact: true }).click()
    await page.waitForFunction(() => Boolean((window as any).chatActions?.length))
    const pendingRequest = await page.evaluate(() => (window as any).chatActions[0].requestId)
    await page.evaluate((requestId) => {
      ;(window as any).testMergePatch({ chatManagement: { action: 'delete_pending', completedRequestId: requestId, undoDeadline: new Date(Date.now() + 80).toISOString(), archivedConversations: [] } })
    }, pendingRequest)
    await page.locator('button.undo[data-conversation-id="c2"]').waitFor()
    await page.waitForURL(url => new URL(url).pathname === '/chats/new')
  } finally {
    await page.close()
  }
})

test('account menu supports keyboard actions and submits the real CSRF logout flow', async () => {
  const page = await browser.newPage({ viewport: { width: 1320, height: 900 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    const trigger = page.locator('.footer .user-card')
    await trigger.focus()
    await page.keyboard.press('Enter')
    const menu = page.getByRole('menu', { name: 'Account' })
    await menu.waitFor()
    const settings = menu.getByRole('menuitem', { name: 'Settings', exact: true })
    expect(await settings.getAttribute('href')).toBe('/admin/profile')
    expect(await settings.evaluate(el => el === (el.getRootNode() as ShadowRoot).activeElement)).toBe(true)
    await page.keyboard.press('ArrowUp')
    const logout = menu.getByRole('menuitem', { name: 'Log out', exact: true })
    expect(await logout.evaluate(el => el === (el.getRootNode() as ShadowRoot).activeElement)).toBe(true)
    await page.keyboard.press('Escape')
    expect(await trigger.getAttribute('aria-expanded')).toBe('false')
    expect(await trigger.evaluate(el => el === (el.getRootNode() as ShadowRoot).activeElement)).toBe(true)
    await page.evaluate(() => {
      const meta = document.createElement('meta'); meta.name = 'csrf-token'; meta.content = 'test-account-csrf'; document.head.append(meta)
    })
    await trigger.click()
    await page.route('**/auth/logout', route => route.fulfill({ status: 200, contentType: 'text/html', body: '<p>Signed out</p>' }))
    const request = page.waitForRequest(req => req.url().endsWith('/auth/logout'))
    await logout.click()
    const submitted = await request
    expect(submitted.method()).toBe('POST')
    expect(new URLSearchParams(submitted.postData()!).get('gorilla.csrf.Token')).toBe('test-account-csrf')
  } finally { await page.close() }
})

test('mobile account menu fits above the footer and keeps search available after dismissal', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    for (const route of ['/sidebar-history', '/admin-sidebar']) {
      await page.goto(`${baseURL}${route}`)
      await page.locator('lv-sidebar .mobile-menu-button').click()
      const trigger = page.locator('.mobile-footer .user-card')
      await trigger.click()
      const menu = page.getByRole('menu', { name: 'Account', exact: true })
      const bounds = await menu.boundingBox(), anchor = await trigger.boundingBox()
      expect(bounds!.x).toBeGreaterThanOrEqual(0)
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390)
      expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(anchor!.y)
      await page.keyboard.press('Escape')
      expect(await trigger.getAttribute('aria-expanded')).toBe('false')
      await page.locator('lv-sidebar .mobile-footer .search-button').click()
      await page.getByRole('dialog', { name: 'Search LeapView', exact: true }).waitFor()
      await page.keyboard.press('Escape')
      await trigger.click()
      await page.locator('lv-sidebar .mobile-footer').click({ position: { x: 1, y: 1 } })
      await menu.waitFor({ state: 'hidden' })
      expect(await trigger.getAttribute('aria-expanded')).toBe('false')
    }
  } finally { await page.close() }
})

test('custom sidebar identity has a full row below the header controls', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } })
  try {
    await page.goto(`${baseURL}/sidebar-history`)
    await page.waitForFunction(() => customElements.get('lv-sidebar'))
    const layout = await page.locator('lv-sidebar').evaluate(async (sidebar: any) => {
      sidebar.config = { ...sidebar.config, productName: 'Micro Matic', productLogoUrl: '/micro-matic.svg' }
      await sidebar.updateComplete
      const root = sidebar.shadowRoot as ShadowRoot
      const identity = root.querySelector<HTMLElement>('.brand-identity')!
      const switcher = root.querySelector<HTMLElement>('.brand-row > .area-switcher')!
      const name = root.querySelector<HTMLElement>('.name')!
      const attribution = root.querySelector<HTMLElement>('.powered-by')!
      return {
        controlsAboveIdentity: switcher.getBoundingClientRect().bottom <= identity.getBoundingClientRect().top,
        nameFits: name.scrollWidth <= name.clientWidth,
        attributionFits: attribution.scrollWidth <= attribution.clientWidth,
      }
    })
    expect(layout).toEqual({ controlsAboveIdentity: true, nameFits: true, attributionFits: true })
  } finally {
    await page.close()
  }
})
