import { typographyTestTokens } from '../test-typography-tokens'

export function testDocument(): string {
  const page = {
    kind: 'catalog',
    title: 'Dashboards',
    description: 'Reports backed by semantic models.',
    dashboards: [
      {
        id: 'executive-sales',
        dashboardId: 'executive-sales',
        appearanceIcon: 'chart-no-axes-combined',
        appearanceColor: 'purple',
        catalogScope: 'managed',
        status: 'published',
        owner: 'Analytics',
        title: 'Executive Sales Dashboard',
        description: 'Fixture report',
        semanticModel: 'olist',
        pageCount: 1,
        tags: ['sales'],
        href: '/dashboards/executive-sales',
        popularity: 'high',
        lastRefreshedAt: '2026-08-12T09:42:00Z',
      },
      {
        id: 'operations-health',
        dashboardId: 'operations-health',
        appearanceIcon: 'package-check',
        appearanceColor: 'orange',
        catalogScope: 'managed',
        status: 'published',
        owner: 'Operations',
        title: 'Operations Health',
        description: 'Fulfillment and delivery performance.',
        semanticModel: 'operations',
        pageCount: 3,
        tags: ['operations'],
        href: '/dashboards/operations-health',
        popularity: 'medium',
        lastRefreshedAt: '2026-08-11T16:20:00Z',
      },
      {
        id: 'inventory-risk',
        dashboardId: 'inventory-risk',
        appearanceIcon: 'layout-dashboard',
        appearanceColor: 'purple',
        catalogScope: 'managed',
        status: 'published',
        owner: 'Supply chain',
        title: 'Inventory Risk',
        description: 'Stock exposure and replenishment.',
        semanticModel: 'inventory',
        pageCount: 2,
        tags: ['inventory'],
        href: '/dashboards/inventory-risk',
        popularity: 'low',
        lastRefreshedAt: '2026-08-10T11:05:00Z',
      },
      {
        id: 'customer-detail',
        dashboardId: 'customer-detail',
        appearanceIcon: 'layout-dashboard',
        appearanceColor: 'purple',
        catalogScope: 'managed',
        status: 'published',
        title: 'Customer Detail',
        description: 'Customer profile details.',
        semanticModel: 'customers',
        pageCount: 1,
        tags: ['customers'],
        href: '/dashboards/customer-detail',
      },
    ],
  }
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-page: #eef2f6; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-page-content-max-width: 72rem; --lv-asset-dashboard-bg: #fbefff; --lv-asset-dashboard-accent: #8250df; --lv-asset-dashboard-border: #d2bfff; --fgColor-disabled: #818b98; --display-blue-fgColor: #1f6feb; --display-purple-fgColor: #783ae4; --display-orange-fgColor: #a24610; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --borderWidth-default: 1px; --borderWidth-thick: 2px; --control-medium-size: 32px; --motion-transition-stateChange: 160ms ease; }
          body[data-color-mode='dark'] { --fgColor-disabled: #656c76; --display-blue-fgColor: #4da0ff; }
        </style>
      </head>
      <body>
        <main data-signals="${escapeHTML(JSON.stringify({ page, chrome: { sidebar: { userName: 'Jacob Nielsen', userAvatarUrl: '/profile/avatars/jacob/avatar-digest' } } }))}">
          <lv-catalog-page></lv-catalog-page>
        </main>
        <script type="module" src="/catalog-page-under-test.js"></script>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
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
