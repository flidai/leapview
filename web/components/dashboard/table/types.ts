import type { InteractionMapping, InteractionSelectionEntry } from '../interaction-selection'
import type { VisualizationConditionalFormat, VisualizationFormat } from '../../../generated/visualization'

export type {
  InteractionMapping,
  InteractionSelectionEntry,
  InteractionSelectionValue,
} from '../interaction-selection'

export type SortDirection = 'asc' | 'desc'
export type BlockID = 'a' | 'b' | 'c'
export type TabularVisualType = 'table' | 'matrix' | 'pivot'
export type TableCardinalityKind = 'unknown' | 'lower_bound' | 'estimated' | 'exact'

export interface TableCardinality {
  kind: TableCardinalityKind
  value: number
}

export interface TableSort {
  key: string
  direction: SortDirection
}

export interface TableColumn {
  key: string
  label: string
  align?: 'left' | 'right'
  role?: 'row_header' | 'metric'
  group?: string
	metric?: string
  columnValue?: string
  width?: number
  format?: 'text' | 'integer' | 'decimal' | 'currency' | 'days'
  visualizationFormat?: VisualizationFormat
  formatting?: TableFormattingRule[]
  conditionalFormatting?: VisualizationConditionalFormat[]
}

export interface TableFormattingRule {
  kind: 'badge' | 'text_color' | 'background_scale' | 'data_bar'
  values?: Record<string, string>
  min?: number
  max?: number
  color?: string
  background?: string
  lowColor?: string
  highColor?: string
}

export interface TableStyle {
  density: 'compact' | 'comfortable' | 'spacious'
  zebra: boolean
  grid: 'none' | 'rows' | 'columns' | 'full'
}

export interface InteractionConfig {
  kind?: string
  toggle?: boolean
  mappings?: InteractionMapping[]
  targets?: Array<{ visualID: string; effect: 'none' | 'filter' | 'highlight' }>
}

export type TableRow = Record<string, unknown>

export interface TableBlock {
  start: number
  requestSeq: number
  resetVersion: number
  sort: TableSort
  rows: TableRow[]
}

export interface TableSignal {
  id?: string
  version: number
  type: TabularVisualType
  title: string
  style: TableStyle
	interaction?: InteractionConfig
  selection?: InteractionSelectionEntry[]
  highlight?: { active: boolean; announcement: string }
  columns: TableColumn[]
  cardinality: TableCardinality
  availableRows: number
  isCapped: boolean
  rowCap: number
  chunkSize: number
  rowHeight: number
  resetVersion: number
  sort: TableSort
  blocks: Record<BlockID, TableBlock>
  loadingBlock: string
  error: string
}

export interface VisualWindowCommand {
  visual: string
  block: BlockID | 'all'
  start: number
  count: number
  requestSeq: number
  sort: TableSort
  resetVersion: number
}

export type VisualAction = 'focus' | 'show-data' | 'copy-data' | 'export-csv' | 'clear-selection'
export type VisibleRowSlot = { kind: 'row'; row: TableRow; index: number } | { kind: 'skeleton'; index: number }

export interface ExpectedBlockRequest {
  start: number
  requestSeq: number
  resetVersion: number
  sort: TableSort
}

export type TanStackTableRow = TableRow & {
  __absoluteIndex: number
  __rowKey: string
}

export const blockIDs: BlockID[] = ['a', 'b', 'c']
export const defaultChunkSize = 50
export const defaultRowHeight = 34
export const defaultSort: TableSort = { key: 'purchase_date', direction: 'desc' }
export const defaultTableStyle: TableStyle = { density: 'comfortable', zebra: true, grid: 'rows' }
