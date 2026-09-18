export function testDocument(includeShellScript: boolean, compact = false, history = false, nav = false, admin = false): string {
  const chromeConfig = compact || history || nav || admin ? {
    sidebar: {
      productName: 'LeapView',
      active: admin ? 'principals' : history ? 'chat' : 'sources',
      admin,
      area: admin ? undefined : history ? 'insights' : 'develop',
      areas: admin ? undefined : [
        { id: 'insights', label: 'Insights', href: '/', icon: 'insights' },
        { id: 'develop', label: 'Develop', href: '/sources', icon: 'code' },
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
          { id: 'c3', title: 'Pinned title loading', href: '/chats/c3', pending: true, pinned: true },
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
          { id: 'chat', label: 'Chats', href: '/chats', icon: 'chat' },
        ],
      }] : nav ? [{
        label: 'Catalog',
        items: [
          { id: 'sources', label: 'Sources', href: '/sources', icon: 'database' },
          { id: 'models', label: 'Models', href: '/models', icon: 'boxes' },
          { id: 'semantic-models', label: 'Semantic models', href: '/semantic-models', icon: 'waypoints' },
          { id: 'dashboard-catalog', label: 'Dashboards', href: '/dashboards', icon: 'dashboard' },
          { id: 'pipelines', label: 'Pipelines', href: '/pipelines', icon: 'workflow' },
          { id: 'connections', label: 'Connections', href: '/connections', icon: 'data' },
        ],
      }, {
        label: 'Operations',
        items: [{ id: 'runs', label: 'Runs', href: '/runs', icon: 'activity' }],
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
            --lv-border-default: 1px solid #d0d7de;
            --lv-bg-accent-muted: #ddf4ff;
            --lv-fg-default: #24292f;
            --lv-fg-accent: #0969da;
            --lv-shadow-floating-sm: 0 1px 2px rgb(0 0 0 / 8%);
            --lv-radius-default: 6px;
            --base-size-2: 2px;
            --base-size-4: 4px;
            --base-size-12: 12px;
            --base-size-36: 36px;
            --lv-border-width: 1px;
            --lv-fg-muted: #57606a;
            --lv-shadow-floating: 0 8px 24px rgb(0 0 0 / 12%);
            --spinner-size-small: 16px;
            --spinner-size-medium: 32px;
            --spinner-size-large: 64px;
            --base-duration-1000: 1000ms;
            --base-easing-linear: linear;
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
        ${history ? '<script type="module">import { mergePatch } from "/static/vendor/datastar-1.0.2.js?v=dev"; window.testMergePatch = mergePatch</script>' : ''}
      </body>
    </html>
  `
}


export function escapeHTML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}
