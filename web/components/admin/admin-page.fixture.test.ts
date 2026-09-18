import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

export type AdminPageTestFixture = {
  server: Server
  baseURL: string
  browser: Browser
}

export async function startAdminPageTestFixture(): Promise<AdminPageTestFixture> {
  const projectRoot = process.cwd()
  const root = join(projectRoot, '.tmp/admin-page-test')
  const server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : root
    const file = normalize(join(fileRoot, url.pathname))
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
  return { server, baseURL: `http://127.0.0.1:${address.port}`, browser: await chromium.launch() }
}

export async function stopAdminPageTestFixture(fixture: AdminPageTestFixture): Promise<void> {
  await fixture.browser.close()
  await new Promise<void>((resolve, reject) => fixture.server.close((error) => error ? reject(error) : resolve()))
}

function testDocument(): string {
  const fiveMinutesAgo = new Date(Date.now() - 5 * 60_000).toISOString()
  const page = {
    kind: 'admin',
    title: 'Principals',
    active: 'principals',
    sidebar: {
      label: 'Admin',
      railLabel: 'Admin',
      ariaLabel: 'Admin navigation',
      storageKey: 'leapview-admin-sidebar-collapsed',
      activeId: 'principals',
      collapsible: false,
      numbered: false,
      items: [
        { id: 'principals', title: 'Principals', href: '/admin/principals', active: true },
        { id: 'groups', title: 'Groups', href: '/admin/groups', active: false },
        { id: 'agent', title: 'Agent', href: '/admin/agent', active: false },
        { id: 'storage', title: 'Storage', href: '/admin/storage', active: false },
        { id: 'queries', title: 'Queries', href: '/admin/queries', active: false },
      ],
    },
    headerTitle: 'Members',
    headerDetail: '',
    directoryList: {
      searchPlaceholder: 'Search by name or email',
      filterLabel: 'Filter members',
      items: [
        { id: 'p1', name: 'Analyst', username: 'analyst', avatarUrl: '/profile/avatars/p1/avatar-digest', email: 'analyst@example.com', href: '/admin/principals/p1', status: 'active', groupCount: 1, joinedAt: '2026-07-20', lastSeenAt: fiveMinutesAgo },
        { id: 'p2', name: 'Local Developer', username: 'dev', email: 'dev@localhost', href: '/admin/principals/p2', status: 'active', groupCount: 0, joinedAt: '2026-07-20', lastSeenAt: '' },
      ],
    },
  }
  const signals = escapeHTML(JSON.stringify({ page }))
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-page: #fff; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-bg-accent: #0969da; --lv-bg-accent-muted: #ddf4ff; --lv-sidebar-bg: #f1f3f5; --lv-report-rail-bg: #ffffff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-accent: #0969da; --lv-fg-link: #0969da; --lv-fg-success: #1a7f37; --lv-fg-warning: #9a6700; --lv-fg-danger: #d1242f; --lv-fg-on-accent: #fff; --lv-icon-muted: #57606a; --lv-line-muted: #d8dee4; --lv-border-width: 1px; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-page-content-max-width: 72rem; --lv-settings-content-max-width: 40rem; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --base-size-24: 24px; --base-size-32: 32px; --base-size-40: 40px; --base-size-48: 48px; --base-size-64: 64px; --lv-transition-fast: 160ms ease; }
          lv-admin-page { min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="${signals}">
          <lv-admin-page></lv-admin-page>
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/admin-page-under-test.js"></script>
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
