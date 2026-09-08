type CommandHeaders = Record<string, string>
type CommandOperation = string | readonly string[]
const dataExplorerClientStorageKey = 'leapview-data-explorer-client'

function csrfToken(): string {
  if (typeof document === 'undefined') return ''
  return document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content.trim() ?? ''
}

/** A tab-scoped identity lets process-local explorer cancellation distinguish
 * two open tabs for the same signed-in user and project. */
function dataExplorerClientID(): string {
  try {
    if (typeof sessionStorage === 'undefined') return ''
    const existing = sessionStorage.getItem(dataExplorerClientStorageKey)?.trim()
    if (existing) return existing
    const generated = globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
    sessionStorage.setItem(dataExplorerClientStorageKey, generated)
    return generated
  } catch {
    return ''
  }
}

export function headers(operation?: CommandOperation, ifMatch?: string): CommandHeaders {
  const token = csrfToken()
  const explorerClientID = dataExplorerClientID()
  // Datastar evaluates headers once per request. The generated transport
  // identity is also the UI mutation's idempotency key on the server.
  const requestID = globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  const operationIDs = (Array.isArray(operation) ? operation : [operation])
    .map((value) => value?.trim())
    .filter((value): value is string => Boolean(value))
  return {
    ...(token ? { 'X-CSRF-Token': token } : {}),
    ...(explorerClientID ? { 'X-LeapView-Data-Explorer-Client': explorerClientID } : {}),
    'X-Request-ID': requestID,
    'Idempotency-Key': `ui:${requestID}`,
    ...(operationIDs.length > 0 ? { 'X-LeapView-Operation-ID': operationIDs.join(',') } : {}),
    ...(ifMatch?.trim() ? { 'If-Match': ifMatch.trim() } : {}),
  }
}

declare global {
  interface Window {
    LeapViewCommand: {
      headers(operation?: CommandOperation, ifMatch?: string): CommandHeaders
    }
  }
}

if (typeof window !== 'undefined') window.LeapViewCommand = { headers }

export {}
