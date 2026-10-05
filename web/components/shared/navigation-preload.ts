// Warm route code on navigation intent. Module preloads do not evaluate the
// destination's components or open its page stream before navigation.
export function installNavigationPreload(): void {
  if (!document.head || typeof document.addEventListener !== 'function') return
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection
  if (connection?.saveData) return
  const probe = document.createElement('link')
  if (!probe.relList.supports('modulepreload')) return

  const loaded = new Set<string>()
  let version: string | null = null
  for (const script of document.querySelectorAll<HTMLScriptElement>('script[src]')) {
    const url = new URL(script.src, document.baseURI)
    if (url.origin !== window.location.origin) continue
    loaded.add(url.href)
    if (url.pathname === '/static/command.js') version = url.searchParams.get('v')
  }
  let preloaded = 0
  const prepare = (event: Event): void => {
    if (connection?.saveData || preloaded >= 8) return
    const anchor = event.composedPath().find((node): node is HTMLAnchorElement => node instanceof HTMLAnchorElement)
    if (!anchor?.href || anchor.hasAttribute('download') || anchor.relList.contains('external')
      || (anchor.target && anchor.target !== '_self')) return
    const destination = new URL(anchor.href, document.baseURI)
    if (destination.origin !== window.location.origin || destination.href === window.location.href) return
    for (const name of navigationAssets(destination.pathname)) {
      if (preloaded >= 8) break
      const asset = new URL(`/static/${name}.js`, window.location.origin)
      if (version !== null) asset.searchParams.set('v', version)
      if (loaded.has(asset.href)) continue
      loaded.add(asset.href)
      const link = document.createElement('link')
      link.rel = 'modulepreload'
      link.href = asset.href
      document.head.append(link)
      preloaded++
    }
  }
  document.addEventListener('pointerover', prepare, { passive: true })
  document.addEventListener('focusin', prepare)
}

function navigationAssets(path: string): readonly string[] {
  if (path === '/' || path === '/dashboards') return ['catalog-page']
  if (path === '/explore') return ['data-explorer']
  if (path === '/dashboards/new') return ['dashboard-builder']
  if (/^\/dashboards\/[^/]+\/edit$/.test(path)) return ['dashboard-builder']
  if (/^\/dashboards\/[^/]+\/preview$/.test(path)
    || /^\/dashboards\/[^/]+(?:\/pages\/[^/]+)?$/.test(path)) return ['dashboard-page', 'url-sync']
  if (/^\/chats(?:\/[^/]+)?$/.test(path)) return ['chat-page']
  if (/^\/admin(?:\/|$)/.test(path)) return ['admin-page']
  if (/^\/(?:sources|models|semantic-models|pipelines|connections)(?:\/[^/]+\/[^/]+)?$/.test(path)
    || path === '/runs' || /^\/dashboards\/[^/]+\/(?:details|definition|versions|lineage)$/.test(path)) return ['project-page']
  return []
}
