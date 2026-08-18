import type { InteractionConfig, InteractionSelectionEntry, TableRow } from './types'
import {
  interactionMappingIdentityEqual,
  interactionSelectionLabel,
  interactionSelectionValue,
  type InteractionSelectionMapping,
} from '../interaction-selection'

export const UI_ROW_SELECTION_FIELD = '__leapview.rowKey'

export type RowSelectionState = Record<string, boolean>

export interface RowSelectionCommand {
  sourceKind: 'visual'
  sourceId: string
  interactionKind: string
  action: 'replace' | 'set'
  toggle: boolean
  mappings: Array<{
    field: string
    value: InteractionSelectionMapping['value']
    label: string
    dataset?: string
    grain?: string
  }>
}

export interface RowClickSelectionInput {
  selected: boolean
  selectedCount: number
  metaKey: boolean
  ctrlKey: boolean
}

export interface RowClickSelectionAction {
  action: 'replace' | 'set'
  toggle: boolean
}

export function rowClickSelectionAction(input: RowClickSelectionInput): RowClickSelectionAction {
  const onlySelectedRow = input.selected && input.selectedCount === 1
  const toggle = input.metaKey || input.ctrlKey || onlySelectedRow
  return {
    action: toggle ? 'set' : 'replace',
    toggle,
  }
}

export function buildRowSelectionCommand(input: {
  sourceId: string
  interaction: InteractionConfig | undefined
  key: string
  row: TableRow
  selectionAction: RowClickSelectionAction
}): RowSelectionCommand | null {
  const sourceId = input.sourceId.trim()
  const interaction = input.interaction
  if (!sourceId || !interaction) return null

  const mappings = interaction.mappings ?? []
  const commandMappings = mappings.length > 0
    ? semanticCommandMappings(input.row, mappings)
    : [{
      field: UI_ROW_SELECTION_FIELD,
      value: input.key,
      label: input.key,
    }]
  if (mappings.length > 0 && commandMappings.length !== mappings.length) return null

  return {
    sourceKind: 'visual',
    sourceId,
    interactionKind: interaction.kind || 'row_selection',
    action: input.selectionAction.action,
    toggle: input.selectionAction.toggle,
    mappings: commandMappings,
  }
}

function semanticCommandMappings(row: TableRow, mappings: NonNullable<InteractionConfig['mappings']>): RowSelectionCommand['mappings'] {
  return mappings.flatMap((mapping) => {
    const value = interactionSelectionValue(row[mapping.value])
    if (value === undefined) return []
    const configuredLabel = mapping.label ? interactionSelectionValue(row[mapping.label]) : undefined
    return [{
      field: mapping.field,
      ...(mapping.dataset !== undefined ? { dataset: mapping.dataset } : {}),
      ...(mapping.grain !== undefined ? { grain: mapping.grain } : {}),
      value,
      label: interactionSelectionLabel(configuredLabel === undefined ? value : configuredLabel),
    }]
  })
}

export function rowSelectionFromEntries(
  rows: Array<{ row: TableRow; key: string }>,
  interaction: InteractionConfig | undefined,
  selection: InteractionSelectionEntry[] | undefined,
): RowSelectionState {
  const entries = selection ?? []
  if (entries.length === 0) return {}
  const next: RowSelectionState = {}
  for (const item of rows) {
    if (rowIsSelected(item.row, item.key, interaction, entries)) {
      next[item.key] = true
    }
  }
  return next
}

export function rowIsSelected(
  row: TableRow,
  key: string,
  interaction: InteractionConfig | undefined,
  selection: InteractionSelectionEntry[] | undefined,
): boolean {
  const entries = selection ?? []
  if (entries.length === 0) return false
  return rowMatchesSelection(row, key, interaction, entries)
}

function rowMatchesSelection(
  row: TableRow,
  key: string,
  interaction: InteractionConfig | undefined,
  selection: InteractionSelectionEntry[],
): boolean {
  const mappings = interaction?.mappings ?? []
  if (mappings.length === 0) {
    return selection.some((entry) => entry.mappings?.some((mapping) => mapping.field === UI_ROW_SELECTION_FIELD && mapping.value === key))
  }
  return selection.some((entry) => mappings.every((mapping) => {
    const selected = entry.mappings?.find((candidate) => interactionMappingIdentityEqual(candidate, mapping))
    const value = interactionSelectionValue(row[mapping.value])
    return selected !== undefined && value !== undefined && selected.value === value
  }))
}

export function selectedRowCount(selection: InteractionSelectionEntry[] | undefined): number {
  return selection?.length ?? 0
}

export function selectionLabels(selection: InteractionSelectionEntry[] | undefined): string[] {
  const entries = selection ?? []
  if (entries.length === 0) return []
  return entries.map((entry) => {
    if (entry.label) return entry.label
    return (entry.mappings ?? []).map((mapping) => mapping.label || interactionSelectionLabel(mapping.value)).filter(Boolean).join(', ')
  }).filter(Boolean)
}
