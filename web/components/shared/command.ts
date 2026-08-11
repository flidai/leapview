type CommandHeaders = Record<string, string>
type CommandOperation = string | readonly string[]

function csrfToken(): string {
  return document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content.trim() ?? ''
}

function headers(operation?: CommandOperation): CommandHeaders {
  const token = csrfToken()
  // Datastar evaluates headers once per request. The generated transport
  // identity is also the UI mutation's idempotency key on the server.
  const requestID = globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  const operationIDs = (Array.isArray(operation) ? operation : [operation])
    .map((value) => value?.trim())
    .filter((value): value is string => Boolean(value))
  return {
    ...(token ? { 'X-CSRF-Token': token } : {}),
    'X-Request-ID': requestID,
    ...(operationIDs.length > 0 ? { 'X-LeapView-Operation-ID': operationIDs.join(',') } : {}),
  }
}

declare global {
  interface Window {
    LeapViewCommand: {
      headers(operation?: CommandOperation): CommandHeaders
    }
  }
}

window.LeapViewCommand = { headers }

export {}
