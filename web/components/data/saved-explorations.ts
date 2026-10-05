export type SavedExploration = {
  id: string
  title: string
  href: string
  updatedAt: string
}

type SavedExplorationsResponse = { items: SavedExploration[] }
type SavedExplorationsFetcher = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>

/** Loads the current actor's saved explorations from the same-origin route. */
export async function loadSavedExplorations(fetcher: SavedExplorationsFetcher = fetch): Promise<SavedExploration[]> {
  const response = await fetcher('/explore/saved', {
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  })
  if (!response.ok) throw new Error(`Saved explorations could not be loaded (${response.status}).`)

  const payload = await response.json() as Partial<SavedExplorationsResponse> | null
  if (!payload || !Array.isArray(payload.items)) throw new Error('Saved explorations returned an invalid response.')

  return payload.items.flatMap((item) => {
    if (!item || typeof item.id !== 'string' || !item.id.trim()
      || typeof item.title !== 'string' || !item.title.trim()
      || typeof item.href !== 'string' || typeof item.updatedAt !== 'string') return []

    return [{
      id: item.id.trim(),
      title: item.title.trim(),
      href: sameOriginExploreHref(item.href, item.id.trim()),
      updatedAt: item.updatedAt,
    }]
  })
}

function sameOriginExploreHref(href: string, id: string): string {
  try {
    const url = new URL(href, 'https://leapview.invalid')
    if (url.origin === 'https://leapview.invalid' && url.pathname === '/explore' && url.searchParams.get('saved') === id) {
      return `${url.pathname}${url.search}`
    }
  } catch {
    // Fall back to the canonical same-origin saved route below.
  }
  return `/explore?saved=${encodeURIComponent(id)}`
}
