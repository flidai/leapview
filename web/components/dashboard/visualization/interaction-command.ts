import type { VisualizationEnvelope } from '../../../generated/visualization'
import {
  interactionSelectionLabel,
  interactionSelectionValue,
  type OptimisticInteractionCommand,
} from '../interaction-selection'

export type InteractionOption = Readonly<{
  key: string
  label: string
  command?: OptimisticInteractionCommand
  refinement?: Readonly<{ center: readonly [number, number]; zoom: number }>
  selected: boolean
}>

export function interactionCommandForRowIndex(
  envelope: VisualizationEnvelope,
  datasetID: string,
  rowIndex: number,
): OptimisticInteractionCommand | undefined {
  if (!Number.isSafeInteger(rowIndex) || rowIndex < 0) return undefined
  const dataset = envelope.dataState.kind === 'inline'
    ? envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
    : undefined
  const row = dataset?.rows[rowIndex]
  if (!dataset || !row || row.length !== dataset.columns.length) return undefined
  return interactionCommandForRow(envelope, datasetID, row)
}

export function interactionCommandForRow(
  envelope: VisualizationEnvelope,
  datasetID: string,
  row: readonly unknown[],
): OptimisticInteractionCommand | undefined {
  const interaction = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')
  if (!interaction || interaction.mappings.length === 0) return undefined
  const schema = envelope.spec.datasets.find((candidate) => candidate.id === datasetID)
  if (!schema) return undefined
  const fieldOrder = envelope.dataState.kind === 'inline'
    ? envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)?.columns
    : envelope.dataState.schema.id === datasetID ? envelope.dataState.schema.fields.map((field) => field.id) : undefined
  if (!fieldOrder || row.length < fieldOrder.length) return undefined

  const mappings = interaction.mappings.map((mapping) => {
    if (mapping.source.dataset !== datasetID || (mapping.label && mapping.label.dataset !== datasetID)) return undefined
    const sourceField = schema.fields.find((field) => field.id === mapping.source.field)
    if (!sourceField || (interaction.requiresStableIdentity && sourceField.role !== 'identity')) return undefined
    const valueIndex = fieldOrder.indexOf(mapping.source.field)
    const value = valueIndex >= 0 ? interactionSelectionValue(row[valueIndex]) : undefined
    if (value === undefined || (interaction.requiresStableIdentity && value === null)) return undefined
    const labelIndex = mapping.label ? fieldOrder.indexOf(mapping.label.field) : valueIndex
    if (mapping.label && !schema.fields.some((field) => field.id === mapping.label?.field)) return undefined
    const labelValue = labelIndex >= 0 ? interactionSelectionValue(row[labelIndex]) : undefined
    if (labelValue === undefined) return undefined
    return {
      field: mapping.targetFieldID,
      ...(mapping.targetDatasetID ? { dataset: mapping.targetDatasetID } : {}),
      ...(mapping.grain ? { grain: mapping.grain } : {}),
      value,
      label: interactionSelectionLabel(labelValue),
    }
  })
  if (mappings.some((mapping) => mapping === undefined)) return undefined
  return {
    sourceKind: 'visual', sourceId: envelope.visualID, interactionKind: interaction.id,
    action: 'set', toggle: interaction.mode === 'multiple',
    mappings: mappings as OptimisticInteractionCommand['mappings'],
  }
}

export function interactionOptions(envelope: VisualizationEnvelope): InteractionOption[] {
  const interaction = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')
  const datasetID = interaction?.mappings[0]?.source.dataset
  if (!interaction || !datasetID) return []
  const dataset = envelope.dataState.kind === 'inline'
    ? envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
    : undefined
  if (!dataset) return []
  const unique = new Map<string, InteractionOption>()
  for (let rowIndex = 0; rowIndex < dataset.rows.length; rowIndex++) {
    const command = interactionCommandForRowIndex(envelope, datasetID, rowIndex)
    if (!command) continue
    const key = commandIdentityKey(command)
    if (unique.has(key)) continue
    const label = command.mappings.map((mapping) => mapping.label || interactionSelectionLabel(mapping.value)).filter(Boolean).join(' · ')
    const selected = interactionCommandSelected(envelope, command)
    unique.set(key, { key, label: label || 'Unlabeled value', command, selected })
  }
  return [...unique.values()]
}

export function interactionCommandSelected(
  envelope: VisualizationEnvelope,
  command: Pick<OptimisticInteractionCommand, 'mappings'>,
): boolean {
  const interaction = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')
  if (!interaction || interaction.mappings.length !== command.mappings.length) return false
  return envelope.selection.some(({ datum }) => {
    if (datum.dataRevision !== envelope.dataRevision) return false
    return interaction.mappings.every((mapping, index) =>
      datum.dataset === mapping.source.dataset
      && Object.is(datum.identity[mapping.source.field], command.mappings[index]?.value))
  })
}

export function commandIdentityKey(command: Pick<OptimisticInteractionCommand, 'mappings'>): string {
  return JSON.stringify(command.mappings.map((mapping) => [mapping.field, mapping.dataset ?? null, mapping.grain ?? null, mapping.value]))
}

export function clearInteractionCommand(envelope: VisualizationEnvelope): OptimisticInteractionCommand | undefined {
  const interaction = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')
  if (!interaction) return undefined
  return {
    sourceKind: 'visual', sourceId: envelope.visualID, interactionKind: interaction.id,
    action: 'clear', toggle: interaction.mode === 'multiple', mappings: [],
  }
}
