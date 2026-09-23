import { uuidv7 } from './command-identity'

type CommandHeaders = Record<string, string>
type CommandOperation = string | readonly string[]

function csrfToken(): string {
  if (typeof document === 'undefined') return ''
  return document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content.trim() ?? ''
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
