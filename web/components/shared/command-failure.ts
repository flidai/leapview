export type BrowserCommandFailureKind =
  | 'session-expired'
  | 'forbidden'
  | 'not-found'
  | 'conflict'
  | 'rate-limited'
  | 'unavailable'
  | 'network'
  | 'unknown'

export type BrowserCommandFailure = {
  kind: BrowserCommandFailureKind
  status: number | null
  retryable: boolean
  message: string
}

type DatastarFetchDetail = {
  type?: string
  argsRaw?: { status?: string | number }
}

/**
 * Normalizes Datastar's terminal fetch events into LeapView's browser failure
 * contract. Components still own the domain-specific action text, but they do
 * not need to reinterpret transport statuses or retry exhaustion independently.
 */
export function browserCommandFailure(event: Event, action = 'This action'): BrowserCommandFailure | null {
  const detail = (event as CustomEvent<DatastarFetchDetail>).detail
  if (detail?.type !== 'error' && detail?.type !== 'retries-failed') return null

  const status = normalizeStatus(detail.argsRaw?.status)
  const prefix = action.trim() || 'This action'
  if (status === 401) return failure('session-expired', status, false, `${prefix} could not continue because your session expired. Sign in again, then retry.`)
  if (status === 403) return failure('forbidden', status, false, `${prefix} is not permitted for your account.`)
  if (status === 404) return failure('not-found', status, false, `${prefix} could not continue because the resource changed or is no longer available. Reload the page.`)
  if (status === 409 || status === 412) return failure('conflict', status, false, `${prefix} conflicted with a newer change. Reload the latest state before retrying.`)
  if (status === 429) return failure('rate-limited', status, true, `${prefix} was temporarily rate limited. Wait a moment, then retry.`)
  if (status !== null && status >= 500) return failure('unavailable', status, true, `${prefix} could not be completed because the service is temporarily unavailable. Your previous state was kept; retry when ready.`)
  if (detail.type === 'retries-failed' || status === null || status === 0) return failure('network', status, true, `${prefix} could not reach the server. Your previous state was kept; check the connection and retry.`)
  return failure('unknown', status, false, `${prefix} could not be completed. Your previous state was kept; review the page and retry.`)
}

function normalizeStatus(value: string | number | undefined): number | null {
  if (typeof value === 'number' && Number.isInteger(value) && value >= 0) return value
  if (typeof value !== 'string' || value.trim() === '') return null
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed >= 0 ? parsed : null
}

function failure(kind: BrowserCommandFailureKind, status: number | null, retryable: boolean, message: string): BrowserCommandFailure {
  return { kind, status, retryable, message }
}

function failureMessage(label: string): (problem: APIGenProblemDetails) => string {
  return (problem) => `${label}: ${problem.detail} (${problem.code})`
}

// finalizeReleaseFailureMessage is the UI proof that every authored failure
// variant must be handled when the generated contract changes.
export function finalizeReleaseFailureMessage(failure: FinalizeReleaseFailure): string {
  return matchFinalizeReleaseFailure(failure, {
    conflict: failureMessage('Release finalization conflict'),
    immutable: failureMessage('Release is immutable'),
    incomplete: failureMessage('Release artifacts are incomplete'),
    not_found: failureMessage('Release not found'),
    queue_unavailable: failureMessage('Release finalization queue unavailable'),
  })
}
import {
  matchFinalizeReleaseFailure,
  type APIGenProblemDetails,
  type FinalizeReleaseFailure,
} from '../../generated/api/failures'
