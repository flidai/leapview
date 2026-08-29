export type ProductSearchItem = {
  id: string
  kind: string
  resourceKind: string
  label: string
  description: string
  href: string
}

const searchableAssetKinds = new Set([
  'connection',
  'dashboard',
  'model',
  'pipeline',
  'semantic_model',
  'source',
])

type CatalogSearchResponse = {
  items?: Array<{
    reference?: { id?: string; kind?: string }
    name?: string
    displayName?: string
    description?: string
    href?: string
  }>
}

export class ProductSearchService {
  constructor(private readonly fetcher: typeof fetch = window.fetch.bind(window)) {}

  async search(query: string, signal?: AbortSignal): Promise<ProductSearchItem[]> {
    const normalized = query.trim()
    if (!normalized) return []

    const response = await this.fetcher(`/search?q=${encodeURIComponent(normalized)}`, {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
      signal,
    })
    if (!response.ok) throw new Error(`Search request failed with status ${response.status}`)

    const body = await response.json() as CatalogSearchResponse
    const catalog = (body.items ?? []).flatMap((item): ProductSearchItem[] => {
      const id = item.reference?.id?.trim()
      const href = item.href?.trim()
      const kind = item.reference?.kind?.trim()
      const label = item.displayName?.trim() || item.name?.trim()
      if (!id || !href || !kind || !label || !searchableAssetKinds.has(kind)) return []
      return [{
        id: `catalog:${kind}:${id}`,
        kind: formatKind(kind),
        resourceKind: kind,
        label,
        description: item.description?.trim() || item.name?.trim() || id,
        href,
      }]
    })

    const seen = new Set<string>()
    return catalog.filter((item) => {
      if (seen.has(item.href)) return false
      seen.add(item.href)
      return true
    })
  }
}

function formatKind(kind: string): string {
  return kind
    .split('_')
    .filter(Boolean)
    .map((part) => part.charAt(0).toLocaleUpperCase() + part.slice(1))
    .join(' ')
}
