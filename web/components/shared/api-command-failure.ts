import {
  matchFinalizeReleaseFailure,
  type APIGenProblemDetails,
  type FinalizeReleaseFailure,
} from '../../generated/api/failures'

function failureMessage(label: string): (problem: APIGenProblemDetails) => string {
  return (problem) => `${label}: ${problem.detail} (${problem.code})`
}

// finalizeReleaseFailureMessage is the UI proof that every authored failure
// variant must be handled when the generated contract changes. Keep generated
// API failures separate from browser transport failures so route bundles only
// load the contracts they execute.
export function finalizeReleaseFailureMessage(failure: FinalizeReleaseFailure): string {
  return matchFinalizeReleaseFailure(failure, {
    conflict: failureMessage('Release finalization conflict'),
    immutable: failureMessage('Release is immutable'),
    incomplete: failureMessage('Release artifacts are incomplete'),
    not_found: failureMessage('Release not found'),
    queue_unavailable: failureMessage('Release finalization queue unavailable'),
  })
}
