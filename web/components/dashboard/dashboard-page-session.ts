export type DevelopmentSessionDiagnostic = {
  code?: string
  message?: string
  path?: string
  line?: number
  column?: number
}

export type DevelopmentSessionRecord = {
  revision?: number
  attempted?: { candidateId?: string, artifactDigest?: string, graphDigest?: string }
  lastValid?: { candidateId?: string, artifactDigest?: string, graphDigest?: string }
  diagnostics?: DevelopmentSessionDiagnostic[]
}

export type DevelopmentSessionTransition = {
  candidateID: string
  diagnostics: DevelopmentSessionDiagnostic[]
  outOfDate: boolean
  shouldReload: boolean
  revision: number
}

export function developmentSessionEventsPath(pathname: string): string | null {
  const marker = '/development-session/candidate/preview'
  const markerIndex = pathname.indexOf(marker)
  if (markerIndex < 0) return null
  return `${pathname.slice(0, markerIndex)}${marker}/events`
}

function identityKey(identity: DevelopmentSessionRecord['lastValid']): string {
  return [identity?.candidateId, identity?.artifactDigest, identity?.graphDigest].map((value) => value?.trim() ?? '').join('\u0000')
}

export class DevelopmentSessionViewController {
  private initialized = false
  private lastValidKey = ''

  consume(record: DevelopmentSessionRecord, currentCandidateID = ''): DevelopmentSessionTransition {
    const attempted = record.attempted ?? {}
    const lastValid = record.lastValid ?? {}
    const candidateID = lastValid.candidateId?.trim() ?? ''
    const nextKey = identityKey(lastValid)
    const shouldReload = this.initialized
      ? nextKey !== '' && nextKey !== this.lastValidKey
      : Boolean(currentCandidateID && candidateID && currentCandidateID !== candidateID)
    const diagnostics = Array.isArray(record.diagnostics)
      ? record.diagnostics.filter((diagnostic) => diagnostic && typeof diagnostic === 'object').slice(0, 64)
      : []
    const attemptedDiffers = Boolean(
      attempted.artifactDigest && attempted.artifactDigest !== lastValid.artifactDigest
      || attempted.graphDigest && attempted.graphDigest !== lastValid.graphDigest,
    )
    this.initialized = true
    this.lastValidKey = nextKey
    return {
      candidateID,
      diagnostics,
      outOfDate: attemptedDiffers || diagnostics.length > 0,
      shouldReload,
      revision: typeof record.revision === 'number' ? record.revision : 0,
    }
  }
}
