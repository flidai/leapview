import { reportViewPathChangedEvent } from '../report-view-state'

const dataStarURLSyncEvent = 'datastar-url-params-sync'
export {}

type URLParamsShape = Record<string, string | string[]>

type URLSyncDetail = {
  params: URLParamsShape
  url: string
}

function normalizeURLParams(value: unknown): URLParamsShape {
  const record = typeof value === 'object' && value !== null ? (value as Record<string, unknown>) : {}
  const out: URLParamsShape = {}

  for (const [key, raw] of Object.entries(record)) {
    if (Array.isArray(raw)) {
      const seen = new Set<string>()
      out[key] = raw.flatMap((item) => {
        if (typeof item !== 'string') return []
        const trimmed = item.trim()
        if (!trimmed || seen.has(trimmed)) return []
        seen.add(trimmed)
        return [trimmed]
      })
      continue
    }

    out[key] = typeof raw === 'string' ? raw.trim() : ''
  }

  return out
}

function toQueryString(value: unknown): string {
  const params = normalizeURLParams(value)
  const search = new URLSearchParams()

  for (const [key, raw] of Object.entries(params)) {
    if (Array.isArray(raw)) {
      for (const item of raw) search.append(key, item)
      continue
    }
    if (raw) search.set(key, raw)
  }

  return search.toString()
}

function toURL(path: string, value: unknown): string {
  const query = toQueryString(value)
  return query ? `${path}?${query}` : path
}

function readLocation(shape: unknown): URLParamsShape {
  const base = normalizeURLParams(shape)
  const url = new URL(window.location.href)
  const next: URLParamsShape = {}

  for (const [key, raw] of Object.entries(base)) {
    if (Array.isArray(raw)) {
      next[key] = url.searchParams.getAll(key).map((item) => item.trim()).filter(Boolean)
      continue
    }
    next[key] = url.searchParams.get(key)?.trim() ?? raw
  }

  return next
}

function emit(params: URLParamsShape): URLParamsShape {
  window.dispatchEvent(new CustomEvent<URLSyncDetail>(dataStarURLSyncEvent, {
    detail: {
      params,
      url: `${window.location.pathname}${window.location.search}`,
    },
  }))
  return params
}

function updateHistory(method: 'pushState' | 'replaceState', value: unknown, path = window.location.pathname): string {
  const next = toURL(path, value)
  const current = `${window.location.pathname}${window.location.search}`
  const currentPathname = window.location.pathname
  if (next !== current) {
    window.history[method]({}, '', next)
    const nextPathname = new URL(next, window.location.href).pathname
    if (nextPathname !== currentPathname) {
      window.dispatchEvent(new CustomEvent(reportViewPathChangedEvent, {
        detail: { pathname: nextPathname },
      }))
    }
  }
  return next
}

function replace(value: unknown, path = window.location.pathname): string {
  return updateHistory('replaceState', value, path)
}

function push(value: unknown, path = window.location.pathname): string {
  return updateHistory('pushState', value, path)
}

let popstateBound = false

function bindPopstate(fallback: unknown): void {
  if (popstateBound) return
  popstateBound = true
  window.addEventListener('popstate', () => {
    emit(readLocation(fallback))
    window.location.reload()
  })
}

const datastarURLSync = {
  bindPopstate,
  push,
  replace,
}

declare global {
  interface Window {
    DatastarURLSync?: typeof datastarURLSync
  }
}

window.DatastarURLSync = datastarURLSync

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', () => bindPopstate({}), { once: true })
} else {
  bindPopstate({})
}
