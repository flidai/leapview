import { css, html, nothing } from 'lit'

const storageKey = 'leapview-playground:navigation:v1'
export type NavigationPreferences = { favorites: string[]; recent: string[]; lastVisited: string }
export type ShortcutExample = { label: string; groupLabel: string }
export const emptyNavigation = (): NavigationPreferences => ({ favorites: [], recent: [], lastVisited: '' })

/** Persist only known catalog routes; never accept labels or destinations from storage. */
export function readNavigation(examples: ReadonlyMap<string, ShortcutExample>): NavigationPreferences {
  try {
    const raw = localStorage.getItem(storageKey)
    if (!raw || raw.length > 30000) return emptyNavigation()
    const value: unknown = JSON.parse(raw)
    if (!value || typeof value !== 'object' || Array.isArray(value)) return emptyNavigation()
    const stored = value as Record<string, unknown>
    if (stored.version !== 1) return emptyNavigation()
    const routes = (value: unknown) => Array.isArray(value)
      ? [...new Set(value.filter((route): route is string => typeof route === 'string' && examples.has(route)))].slice(0, examples.size)
      : []
    const favorites = routes(stored.favorites)
    const lastVisited = typeof stored.lastVisited === 'string' && examples.has(stored.lastVisited) ? stored.lastVisited : ''
    return { favorites, lastVisited, recent: routes(stored.recent).filter(route => route !== lastVisited && !favorites.includes(route)).slice(0, 5) }
  } catch { return emptyNavigation() }
}

export function writeNavigation(preferences: NavigationPreferences): void {
  try { localStorage.setItem(storageKey, JSON.stringify({ version: 1, ...preferences })) } catch { /* Keep the in-memory shortcuts when storage is unavailable. */ }
}

export function visitExample(preferences: NavigationPreferences, route: string, examples: ReadonlyMap<string, ShortcutExample>): NavigationPreferences {
  if (!examples.has(route) || preferences.lastVisited === route) return preferences
  const previous = preferences.lastVisited
  const candidates = previous && examples.has(previous) ? [previous, ...preferences.recent] : preferences.recent
  return {
    ...preferences,
    lastVisited: route,
    recent: [...new Set(candidates)].filter(item => item !== route && !preferences.favorites.includes(item)).slice(0, 5),
  }
}

export function toggleFavorite(preferences: NavigationPreferences, route: string, examples: ReadonlyMap<string, ShortcutExample>): NavigationPreferences {
  if (!examples.has(route)) return preferences
  const favorites = preferences.favorites.includes(route) ? preferences.favorites.filter(item => item !== route) : [...preferences.favorites, route]
  return { ...preferences, favorites, recent: preferences.recent.filter(item => item !== route && !favorites.includes(item)).slice(0, 5) }
}

export function navigationShortcuts(preferences: NavigationPreferences, current: string, examples: ReadonlyMap<string, ShortcutExample>, revisit: (route: string) => void) {
  const favorites = preferences.favorites.filter(route => examples.has(route))
  const recent = preferences.recent.filter(route => examples.has(route) && route !== current && !favorites.includes(route)).slice(0, 5)
  if (!favorites.length && !recent.length) return nothing
  const links = (routes: string[]) => routes.map(route => {
    const example = examples.get(route)!
    return html`<a href=${`#${route}`} title=${`${example.groupLabel} · ${example.label}`} aria-current=${route === current ? 'page' : nothing} @click=${() => revisit(route)}>${example.label}</a>`
  })
  return html`<nav class="shortcuts" aria-label="Example shortcuts">
    ${favorites.length ? html`<section aria-labelledby="favorite-examples"><h2 id="favorite-examples">Favorites</h2>${links(favorites)}</section>` : nothing}
    ${recent.length ? html`<section aria-labelledby="recent-examples"><h2 id="recent-examples">Recent</h2>${links(recent)}</section>` : nothing}
  </nav>`
}

export const navigationShortcutStyles = css`
  nav.shortcuts { gap: var(--base-size-12); padding-bottom: var(--base-size-16); border-bottom: var(--lv-border-muted); }
  .shortcuts h2 { margin: 0 0 var(--base-size-4); padding-inline: var(--base-size-8); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); }
  .shortcuts a { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title-line { display: flex; align-items: center; gap: var(--base-size-8); }
  .title-line h1 { min-width: 0; }
  .favorite-toggle { width: var(--control-medium-size); padding: 0; flex-shrink: 0; }
  .favorite-toggle[aria-pressed='true'] { color: var(--lv-fg-accent); background: var(--lv-bg-accent-muted); }
  .favorite-toggle[aria-pressed='true'] svg { fill: currentColor; }
`
