import type { DataExploreCommand, DataExploreFieldSignal, DataExploreResultSignal } from '../../generated/signals'
import type { ExplorationSpec } from '../../generated/exploration'
import type { VisualizationEnvelope, VisualizationWindowRequest } from '../../generated/visualization'
import type { OptimisticInteractionCommand } from '../dashboard/interaction-selection'
import { explorationSpecFromInteraction, type ExplorationInteractionMode } from './data-explorer-drill'
import { explorationSortFieldForResult, explorationSpecFor } from './data-explorer-spec'

type ExploreInteractionCallbacks = {
  setError: (message: string) => void
  clearError: () => void
  run: (spec: ExplorationSpec, command: DataExploreCommand) => void
  emit: (spec: ExplorationSpec, command: DataExploreCommand) => void
}

export function handleDataExploreInteraction(
  event: CustomEvent<{ command: OptimisticInteractionCommand; mode: ExplorationInteractionMode }>,
  command: DataExploreCommand,
  fields: readonly DataExploreFieldSignal[],
  grainFields: readonly string[],
  callbacks: ExploreInteractionCallbacks,
): void {
  const applied = explorationSpecFromInteraction(explorationSpecFor(command), fields, event.detail.command, event.detail.mode, grainFields)
  if (!applied.ok) {
    callbacks.setError(applied.error)
    return
  }
  callbacks.clearError()
  if (event.detail.mode === 'drill') {
    callbacks.run(applied.spec, command)
    return
  }
  callbacks.emit(applied.spec, command)
}

type ExploreWindowRequestCallbacks = {
  setError: (message: string) => void
  clearError: () => void
  run: (spec: ExplorationSpec, command: DataExploreCommand) => void
}

export function handleDataExploreWindowRequest(
  event: CustomEvent<VisualizationWindowRequest>,
  command: DataExploreCommand,
  views: Record<string, VisualizationEnvelope>,
  result: DataExploreResultSignal,
  callbacks: ExploreWindowRequestCallbacks,
): void {
  const validationError = validateDataExploreWindowRequest(event.detail, views, result)
  if (validationError) {
    callbacks.setError(`Ignored visualization window request: ${validationError}`)
    return
  }
  // Explorer envelopes materialize the complete bounded result in one block;
  // only a start-zero request can represent a user sort. Paging requests are
  // never translated into a partial, differently governed query.
  if ((event.detail.start ?? 0) > 0) return
  const requestedSort = event.detail.sort[0]
  const spec = explorationSpecFor(command)
  const field = explorationSortFieldForResult(spec, requestedSort.field.field)
  if (!field) {
    callbacks.setError('This generated pivot column cannot be used as a governed query sort.')
    return
  }
  const direction = requestedSort.direction === 'descending' ? 'desc' : 'asc'
  const sort = [{ field, direction }] as ExplorationSpec['sort']
  const nextSpec = spec.pivot
    ? { ...spec, pivot: { ...spec.pivot, sort } }
    : { ...spec, sort }
  callbacks.clearError()
  callbacks.run(nextSpec, command)
}

export function validateDataExploreWindowRequest(
  request: unknown,
  views: Record<string, VisualizationEnvelope> | undefined,
  result: Pick<DataExploreResultSignal, 'requestSeq'>,
): string | undefined {
  if (!request || typeof request !== 'object') return 'the request payload is invalid'
  const candidate = request as Partial<VisualizationWindowRequest>
  if (typeof candidate.visualID !== 'string' || !candidate.visualID.trim()) return 'the visual ID is missing'
  const envelope = Object.values(views ?? {}).find((view) => view.visualID === candidate.visualID)
  if (!envelope) return 'the visual ID does not match the presented visualization'
  if (typeof candidate.specRevision !== 'string' || candidate.specRevision !== envelope.specRevision) return 'the spec revision is no longer current'
  if (typeof candidate.dataRevision !== 'number' || !Number.isSafeInteger(candidate.dataRevision) || candidate.dataRevision !== envelope.dataRevision) return 'the data revision is no longer current'
  if (envelope.dataState.kind !== 'windowed') return 'the presented visualization does not support windows'
  if (envelope.dataState.specRevision !== envelope.specRevision || envelope.dataState.dataRevision !== envelope.dataRevision) return 'the presented visualization envelope is internally inconsistent'

  const requestSeq = candidate.requestSeq
  const resultSeq = result.requestSeq
  if (typeof requestSeq !== 'number' || !Number.isSafeInteger(requestSeq) || requestSeq < 0) return 'the request sequence is invalid'
  if (typeof resultSeq !== 'number' || !Number.isSafeInteger(resultSeq) || resultSeq < 0) return 'the presented result sequence is invalid'
  if (resultSeq > 0 && envelope.dataRevision !== resultSeq) return 'the presented result sequence does not match the visualization'
  const blockID = candidate.blockID
  if (typeof blockID !== 'string' || !blockID.trim()) return 'the block ID is missing'
  // Window adapters may allocate a new rotating block (a/b/c) while paging,
  // so presence in the currently materialized envelope is not lineage proof.
  // Identity is carried by the visual/spec/data/reset revisions instead.
  const block = blockID === 'all' ? undefined : envelope.dataState.blocks[blockID]
  if (block && block.id !== blockID) return 'the block ID does not match the presented block'
  const presentedSequences = Object.values(envelope.dataState.blocks).map((entry) => entry.requestSeq).filter((value) => Number.isSafeInteger(value) && value >= 0)
  const minimumCurrentSequence = Math.max(resultSeq, envelope.dataRevision, ...presentedSequences)
  if (requestSeq < minimumCurrentSequence) return 'the request sequence is superseded'

  const resetVersion = candidate.resetVersion
  const currentResetVersion = envelope.dataState.resetVersion
  if (typeof resetVersion !== 'number' || !Number.isSafeInteger(resetVersion) || resetVersion < 0) return 'the reset version is invalid'
  // A table sort intentionally increments resetVersion before the next
  // envelope arrives; any larger jump is from a superseded table instance.
  if (resetVersion !== currentResetVersion && resetVersion !== currentResetVersion + 1) return 'the reset version is no longer current'
  if (typeof candidate.start !== 'number' || !Number.isSafeInteger(candidate.start) || candidate.start < 0) return 'the window start is invalid'
  if (typeof candidate.limit !== 'number' || !Number.isSafeInteger(candidate.limit) || candidate.limit < 1) return 'the window limit is invalid'
  if (!Array.isArray(candidate.sort) || candidate.sort.length === 0) return 'the sort is missing'
  const sort = candidate.sort[0]
  if (!sort || typeof sort !== 'object' || !sort.field || typeof sort.field.field !== 'string' || !sort.field.field.trim()) return 'the sort field is invalid'
  if (sort.direction !== 'ascending' && sort.direction !== 'descending') return 'the sort direction is invalid'
  return undefined
}
