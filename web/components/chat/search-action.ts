import type { ChatTranscriptItemSignal } from '../../generated/signals'

const catalogKinds = new Set(['project', 'connection', 'source', 'model', 'semantic_model', 'pipeline', 'dashboard'])

/** Preserve the tool's exact catalog scope and pagination in a browser URL. */
export function searchActionHref(item: ChatTranscriptItemSignal): string | null {
  if (item.name !== 'catalog_search' || item.status !== 'complete' || item.error) return null
  try {
    const envelope = JSON.parse(item.inputJson || '{}')
    const input: unknown = item.argumentsJson ? JSON.parse(item.argumentsJson) : typeof envelope?.arguments === 'string' ? JSON.parse(envelope.arguments) : envelope?.arguments ?? envelope
    if (!input || typeof input !== 'object' || Array.isArray(input)) return null
    const request = input as Record<string, unknown>
    if (typeof request.query !== 'string' || !request.query.trim()) return null
    const params = new URLSearchParams({ q: request.query.trim() })
    if (request.kinds !== undefined) {
      if (!Array.isArray(request.kinds)) return null
      for (const kind of request.kinds) {
        if (typeof kind !== 'string' || !catalogKinds.has(kind)) return null
        params.append('kind', kind)
      }
    }
    for (const key of ['domain', 'cursor']) {
      const value = request[key]
      if (value !== undefined && typeof value !== 'string') return null
      if (typeof value === 'string' && value) params.set(key, value)
    }
    const limit = request.limit === 0 ? 10 : request.limit ?? 10
    if (typeof limit !== 'number' || !Number.isInteger(limit) || limit < 1 || limit > 25) return null
    params.set('limit', String(limit))
    return `/search?${params.toString()}`
  } catch {
    return null
  }
}
