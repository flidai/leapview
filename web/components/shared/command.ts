type CommandHeaders = Record<string, string>

function csrfToken(): string {
  return document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content.trim() ?? ''
}

function headers(): CommandHeaders {
  const token = csrfToken()
  // Datastar evaluates headers once per request. The generated transport
  // identity is also the UI mutation's idempotency key on the server.
  const requestID = globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return {
    ...(token ? { 'X-CSRF-Token': token } : {}),
    'X-Request-ID': requestID,
  }
}

declare global {
  interface Window {
    LeapViewCommand: {
      headers(): CommandHeaders
    }
  }
}

window.LeapViewCommand = { headers }

export {}
