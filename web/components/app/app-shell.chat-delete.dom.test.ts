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
      response.setHeader('content-type', 'text/javascript')
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

test('ordinary chat menus omit Archive', async () => {
  const page = await browser.newPage()
  await page.goto(`${baseURL}/sidebar-history`)
  const row = page.locator('.history-row').filter({ hasText: 'Revenue check' })
  await row.locator('summary[aria-label="More actions for Revenue check"]').click()
  expect(await row.getByRole('menuitem', { name: 'Archive chat', exact: true }).count()).toBe(0)
  await page.close()
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
