type CommandHeaders = Record<string, string>
type CommandOperation = string | readonly string[]

function csrfToken(): string {
  if (typeof document === 'undefined') return ''
  return document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content.trim() ?? ''
}

// Generate a canonical lower-case UUIDv7 for durable command identity. The
// browser's randomUUID() is UUIDv4, which is intentionally not accepted by
// the publication/audit authorities for idempotency keys.
function uuidv7(): string {
  const cryptoAPI = globalThis.crypto
  if (!cryptoAPI?.getRandomValues) throw new Error('secure randomness unavailable')
  const bytes = new Uint8Array(16)
  cryptoAPI.getRandomValues(bytes)
  const timestamp = BigInt(Date.now())
  for (let i = 5; i >= 0; i--) bytes[i] = Number((timestamp >> BigInt((5 - i) * 8)) & 0xffn)
  bytes[6] = (bytes[6] & 0x0f) | 0x70
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

export function headers(operation?: CommandOperation, ifMatch?: string): CommandHeaders {
  const token = csrfToken()
  // Datastar evaluates headers once per request. Keep request and durable
  // idempotency identities distinct while ensuring the latter is UUIDv7.
  const requestID = uuidv7()
  const idempotencyKey = uuidv7()
  const operationIDs = (Array.isArray(operation) ? operation : [operation])
    .map((value) => value?.trim())
    .filter((value): value is string => Boolean(value))
  return {
    ...(token ? { 'X-CSRF-Token': token } : {}),
    'X-Request-ID': requestID,
    'Idempotency-Key': idempotencyKey,
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
