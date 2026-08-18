import type {
  VisualizationEnvelope,
  VisualizationHighlightState,
  VisualizationSelectionEntry,
  VisualizationSpatialSelectionState,
} from '../../generated/visualization'

export type InteractionSelectionValue = string | number | boolean | null

export interface InteractionMappingIdentity {
  field: string
  dataset?: string
  grain?: string
}

export interface InteractionMapping extends InteractionMappingIdentity {
  value: string
  label?: string
}

export interface InteractionSelectionMapping extends InteractionMappingIdentity {
  value: InteractionSelectionValue
  label?: string
}

export interface InteractionSelectionEntry {
  mappings?: InteractionSelectionMapping[]
  label?: string
}

export interface CanonicalInteractionSelection {
  id?: string
  sourceKind: string
  sourceId: string
  interactionKind?: string
  entries?: InteractionSelectionEntry[]
  label?: string
  order?: number
}

export interface InteractionConfigLike {
  kind?: string
  toggle?: boolean
  mappings?: InteractionMapping[]
  targets?: Array<{ visualID: string; effect: 'none' | 'filter' | 'highlight' }>
}

export interface OptimisticInteractionCommand {
  sourceKind: 'visual'
  sourceId: string
  interactionKind: string
  action: 'set' | 'replace' | 'clear'
  toggle: boolean
  mappings: InteractionSelectionMapping[]
  specRevision?: string
  dataRevision?: number
  servingStateID?: string
  filterRevision?: number
  interactionRevision?: number
}

export function canonicalSelectionEntriesForSource(
  selections: readonly CanonicalInteractionSelection[] | undefined,
  sourceKind: 'visual',
  sourceId: string,
): InteractionSelectionEntry[] {
  if (!sourceId) return []
  return (selections ?? [])
    .filter((selection) => selection.sourceKind === sourceKind && selection.sourceId === sourceId)
    .flatMap((selection) => selection.entries ?? [])
}

export function visualizationSelectionEntries(
  envelope: VisualizationEnvelope,
  selections: readonly CanonicalInteractionSelection[] | undefined,
): VisualizationSelectionEntry[] {
  const interaction = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')
  if (!interaction || interaction.mappings.length === 0) return []
  const entries = (selections ?? [])
    .filter((selection) => selection.sourceKind === 'visual'
      && selection.sourceId === envelope.visualID
      && (selection.interactionKind ?? interaction.id) === interaction.id)
    .flatMap((selection) => selection.entries ?? [])

  return entries.flatMap((entry) => {
    const identity: Record<string, InteractionSelectionValue> = {}
    let dataset = ''
    for (const mapping of interaction.mappings) {
      if (dataset && dataset !== mapping.source.dataset) return []
      dataset = mapping.source.dataset
      const target = {
        field: mapping.targetFieldID,
        ...(mapping.targetDatasetID ? { dataset: mapping.targetDatasetID } : {}),
        ...(mapping.grain ? { grain: mapping.grain } : {}),
      }
      const selected = entry.mappings?.find((candidate) => interactionMappingIdentityEqual(candidate, target))
      if (!selected || interactionSelectionValue(selected.value) === undefined) return []
      identity[mapping.source.field] = selected.value
    }
    if (!dataset) return []
    return [{
      datum: { dataset, dataRevision: envelope.dataRevision, identity },
      ...(entry.label ? { label: entry.label } : {}),
    }]
  })
}

export function visualizationHighlightStates(
  target: VisualizationEnvelope,
  visuals: Readonly<Record<string, VisualizationEnvelope>>,
  selections: readonly CanonicalInteractionSelection[] | undefined,
  spatialSelections: readonly VisualizationSpatialSelectionState[] | undefined,
): VisualizationHighlightState[] {
  const highlights: VisualizationHighlightState[] = []
  for (const selection of selections ?? []) {
    if (selection.sourceKind !== 'visual' || (selection.entries?.length ?? 0) === 0) continue
    const source = visuals[selection.sourceId]
    const interaction = source?.spec.interactions.find((candidate) =>
      candidate.id === selection.interactionKind
      && candidate.targets.some((candidate) => candidate.visualID === target.visualID && candidate.effect === 'highlight'))
    if (!interaction) continue
    highlights.push({
      sourceVisualID: selection.sourceId,
      interactionID: interaction.id,
      entries: (selection.entries ?? []).map((entry) => ({
        mappings: (entry.mappings ?? []).map((mapping) => ({
          targetFieldID: mapping.field,
          ...(mapping.dataset ? { targetDatasetID: mapping.dataset } : {}),
          ...(mapping.grain ? { grain: mapping.grain } : {}),
          value: mapping.value,
          ...(mapping.label ? { label: mapping.label } : {}),
        })),
        label: entry.label ?? '',
      })),
      label: selection.label ?? '',
    })
  }
  for (const selection of spatialSelections ?? []) {
    const source = visuals[selection.visualID]
    if (source?.spec.kind !== 'geographic') continue
    const interaction = source.spec.spatialInteractions.find((candidate) =>
      candidate.id === selection.interactionID
      && candidate.targets.some((candidate) => candidate.visualID === target.visualID && candidate.effect === 'highlight'))
    if (!interaction) continue
    highlights.push({
      sourceVisualID: selection.visualID,
      interactionID: selection.interactionID,
      entries: [],
      spatialGeometry: selection.geometry,
      spatialLatitudeFieldID: interaction.latitude.targetFieldID,
      spatialLongitudeFieldID: interaction.longitude.targetFieldID,
      label: 'Spatial selection',
    })
  }
  return highlights
}

export function interactionSelectionValue(value: unknown): InteractionSelectionValue | undefined {
  if (value === null) return null
  if (typeof value === 'string' || typeof value === 'boolean') return value
  if (typeof value === 'number' && Number.isFinite(value)) return value
  return undefined
}

export function interactionMappingIdentityEqual(
  left: InteractionMappingIdentity,
  right: InteractionMappingIdentity,
): boolean {
  return left.field === right.field
    && (left.dataset ?? '') === (right.dataset ?? '')
    && (left.grain ?? '') === (right.grain ?? '')
}

export function interactionMappingKey(mapping: InteractionMappingIdentity, value: InteractionSelectionValue): string {
  return JSON.stringify([mapping.field, mapping.dataset ?? null, mapping.grain ?? null, value])
}

export function interactionSelectionLabel(value: InteractionSelectionValue): string {
  return value === null ? '' : String(value)
}

export function validateInteractionCommand(
  command: OptimisticInteractionCommand,
  configured: InteractionConfigLike | undefined,
): boolean {
  if (!configured || !command.sourceId || command.sourceKind !== 'visual') return false
  if (!['set', 'replace', 'clear'].includes(command.action)) return false
  if (command.interactionKind !== (configured.kind || 'point_selection')) return false
  if (command.action === 'clear') return command.mappings.length === 0

  const mappings = configured.mappings ?? []
  if (mappings.length === 0) {
    return command.interactionKind === 'row_selection'
      && command.mappings.length === 1
      && command.mappings[0]?.field === '__leapview.rowKey'
      && !command.mappings[0]?.dataset
      && !command.mappings[0]?.grain
      && validCommandMapping(command.mappings[0])
  }
  if (command.mappings.length !== mappings.length) return false
  return mappings.every((mapping) => {
    const matches = command.mappings.filter((candidate) => interactionMappingIdentityEqual(candidate, mapping))
    return matches.length === 1 && validCommandMapping(matches[0])
  })
}

export function applyOptimisticInteraction(
  selections: readonly CanonicalInteractionSelection[] | undefined,
  command: OptimisticInteractionCommand,
): CanonicalInteractionSelection[] {
  const selectionID = `${command.sourceKind}:${command.sourceId}:${command.interactionKind}`
  const next: CanonicalInteractionSelection[] = []
  let maxOrder = 0
  let changed = false
  for (const selection of selections ?? []) {
    maxOrder = Math.max(maxOrder, selection.order ?? 0)
    const sameSource = selection.id === selectionID || (
      selection.sourceKind === command.sourceKind
      && selection.sourceId === command.sourceId
      && selection.interactionKind === command.interactionKind
    )
    if (!sameSource) {
      next.push(copyCanonicalSelection(selection))
      continue
    }
    changed = true
    if (command.action === 'clear') continue
    const entries = command.action === 'replace'
      ? updateOptimisticEntries([], command.mappings, false)
      : updateOptimisticEntries(selection.entries ?? [], command.mappings, command.toggle)
    if (entries.length === 0) continue
    next.push({
      ...copyCanonicalSelection(selection),
      id: selectionID,
      entries,
      label: selectionEntriesLabel(entries),
    })
  }
  if (!changed && command.action !== 'clear') {
    const entries = updateOptimisticEntries([], command.mappings, false)
    if (entries.length > 0) {
      next.push({
        id: selectionID,
        sourceKind: command.sourceKind,
        sourceId: command.sourceId,
        interactionKind: command.interactionKind,
        entries,
        label: selectionEntriesLabel(entries),
        order: maxOrder + 1,
      })
    }
  }
  return next
}

function updateOptimisticEntries(
  existing: readonly InteractionSelectionEntry[],
  mappings: readonly InteractionSelectionMapping[],
  toggle: boolean,
): InteractionSelectionEntry[] {
  if (mappings.length === 0) return []
  const entry: InteractionSelectionEntry = {
    mappings: mappings.map((mapping) => ({
      ...mapping,
      label: mapping.label || optimisticValueLabel(mapping.value),
    })),
  }
  entry.label = selectionEntryLabel(entry)
  const next: InteractionSelectionEntry[] = []
  let found = false
  for (const candidate of existing) {
    if (selectionEntryKey(candidate) === selectionEntryKey(entry)) {
      found = true
      if (toggle) continue
    }
    next.push(copySelectionEntry(candidate))
  }
  if (!found) next.push(entry)
  return next
}

function selectionEntryKey(entry: InteractionSelectionEntry): string {
  return JSON.stringify((entry.mappings ?? [])
    .map((mapping) => interactionMappingKey(mapping, mapping.value))
    .sort())
}

function selectionEntryLabel(entry: InteractionSelectionEntry): string {
  return (entry.mappings ?? [])
    .map((mapping) => mapping.label || optimisticValueLabel(mapping.value))
    .filter(Boolean)
    .join(', ')
}

function selectionEntriesLabel(entries: readonly InteractionSelectionEntry[]): string {
  return entries.map((entry) => entry.label || selectionEntryLabel(entry)).filter(Boolean).join(', ')
}

function copySelectionEntry(entry: InteractionSelectionEntry): InteractionSelectionEntry {
  return { ...entry, mappings: (entry.mappings ?? []).map((mapping) => ({ ...mapping })) }
}

function copyCanonicalSelection(selection: CanonicalInteractionSelection): CanonicalInteractionSelection {
  return { ...selection, entries: (selection.entries ?? []).map(copySelectionEntry) }
}

function validCommandMapping(mapping: InteractionSelectionMapping | undefined): boolean {
  return Boolean(mapping)
    && typeof mapping?.field === 'string'
    && (mapping.dataset === undefined || typeof mapping.dataset === 'string')
    && (mapping.grain === undefined || typeof mapping.grain === 'string')
    && (mapping.label === undefined || typeof mapping.label === 'string')
    && interactionSelectionValue(mapping.value) !== undefined
}

function optimisticValueLabel(value: InteractionSelectionValue): string {
  return value === null ? 'null' : String(value)
}
