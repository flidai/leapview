import {
  blockIDs,
  defaultChunkSize,
  defaultRowHeight,
  defaultSort,
  defaultTableStyle,
  type BlockID,
  type TableBlock,
  type TableSignal,
  type TableStyle,
  type TableSort,
} from './types'

export function emptyBlocks(): Record<BlockID, TableBlock> {
  return {
    a: { start: 0, requestSeq: 0, resetVersion: 0, sort: defaultSort, rows: [] },
    b: { start: defaultChunkSize, requestSeq: 0, resetVersion: 0, sort: defaultSort, rows: [] },
    c: { start: defaultChunkSize * 2, requestSeq: 0, resetVersion: 0, sort: defaultSort, rows: [] },
  }
}

export const emptyTable: TableSignal = {
  version: 2,
  type: 'table',
  title: 'Orders',
  style: defaultTableStyle,
  interaction: { kind: 'row_selection', toggle: true, mappings: [] },
  columns: [],
  cardinality: { kind: 'unknown', value: 0 },
  availableRows: 0,
  isCapped: false,
  rowCap: 10000,
  chunkSize: defaultChunkSize,
  rowHeight: defaultRowHeight,
  resetVersion: 0,
  sort: defaultSort,
  blocks: emptyBlocks(),
  loadingBlock: '',
  error: '',
}

export const tableConverter = {
  fromAttribute(value: string | null): TableSignal {
    if (!value) return emptyTable
    try {
      return normalizeTable(JSON.parse(value) as Partial<TableSignal>)
    } catch {
      return { ...emptyTable, error: 'Could not parse table signal.' }
    }
  },
  toAttribute(value: TableSignal | null): string {
    return JSON.stringify(value ?? emptyTable)
  },
}

export function normalizeTable(value: Partial<TableSignal>): TableSignal {
  const chunkSize = positiveNumber(value.chunkSize, defaultChunkSize)
  return {
    ...emptyTable,
    ...value,
    version: 2,
    type: value.type === 'matrix' || value.type === 'pivot' ? value.type : 'table',
    style: normalizeStyle(value.style),
    cardinality: normalizeCardinality(value.cardinality),
    availableRows: positiveNumber(value.availableRows, positiveNumber(value.cardinality?.value, 0)),
    rowCap: positiveNumber(value.rowCap, 10000),
    chunkSize,
    rowHeight: positiveNumber(value.rowHeight, defaultRowHeight),
    resetVersion: positiveNumber(value.resetVersion, 0),
    sort: value.sort?.key ? value.sort : defaultSort,
    columns: Array.isArray(value.columns) ? value.columns : [],
    blocks: {
      a: normalizeBlock(value.blocks?.a, 0),
      b: normalizeBlock(value.blocks?.b, chunkSize),
      c: normalizeBlock(value.blocks?.c, chunkSize * 2),
    },
    loadingBlock: value.loadingBlock ?? '',
    error: value.error ?? '',
  }
}

// A scrolling-window payload intentionally omits the expensive exact count.
// Keep cardinality already resolved for the same reset/sort identity while
// still allowing a new reset to discard stale metadata immediately.
export function preserveCardinality(previous: TableSignal, incoming: TableSignal): TableSignal {
	if (previous.resetVersion !== incoming.resetVersion || !sameSort(previous.sort, incoming.sort)) return incoming
	if (incoming.cardinality.kind === 'exact') return incoming
	const previousRank = cardinalityRank(previous.cardinality.kind)
	const incomingRank = cardinalityRank(incoming.cardinality.kind)
	if (previousRank < incomingRank) return incoming
	if (previousRank === incomingRank && previous.cardinality.value <= incoming.cardinality.value) return incoming
  return {
    ...incoming,
    cardinality: previous.cardinality,
	availableRows: previous.cardinality.kind === 'exact' ? previous.availableRows : Math.max(previous.availableRows, incoming.availableRows),
    isCapped: previous.isCapped,
  }
}

function normalizeCardinality(value: Partial<TableSignal['cardinality']> | undefined): TableSignal['cardinality'] {
	const kind = value?.kind
	if (kind !== 'lower_bound' && kind !== 'estimated' && kind !== 'exact') return { kind: 'unknown', value: 0 }
	return { kind, value: positiveNumber(value?.value, 0) }
}

function cardinalityRank(kind: TableSignal['cardinality']['kind']): number {
	switch (kind) {
		case 'exact': return 3
		case 'estimated': return 2
		case 'lower_bound': return 1
		default: return 0
	}
}

export function normalizeStyle(style: Partial<TableStyle> | undefined): TableStyle {
  const density = style?.density === 'compact' || style?.density === 'spacious' ? style.density : defaultTableStyle.density
  const grid = style?.grid === 'none' || style?.grid === 'columns' || style?.grid === 'full' ? style.grid : defaultTableStyle.grid
  return {
    density,
    grid,
    zebra: typeof style?.zebra === 'boolean' ? style.zebra : defaultTableStyle.zebra,
  }
}

export function normalizeBlock(block: TableBlock | undefined, fallbackStart: number): TableBlock {
  return {
    start: positiveNumber(block?.start, fallbackStart),
    requestSeq: positiveNumber(block?.requestSeq, 0),
    resetVersion: positiveNumber(block?.resetVersion, 0),
    sort: block?.sort?.key ? block.sort : defaultSort,
    rows: Array.isArray(block?.rows) ? block.rows : [],
  }
}

export function positiveNumber(value: unknown, fallback: number): number {
  const next = Number(value)
  return Number.isFinite(next) && next >= 0 ? next : fallback
}

export function sameSort(a: TableSort, b: TableSort): boolean {
  return a.key === b.key && a.direction === b.direction
}

export function blockStartsForAll(start: number, chunkSize: number): number[] {
  const currentStart = Math.max(0, Math.floor(start / chunkSize) * chunkSize)
  if (currentStart <= 0) return [0, chunkSize, chunkSize * 2]
  return [Math.max(0, currentStart - chunkSize), currentStart, currentStart + chunkSize]
}

export function sortedBlockRows(blocks: Record<BlockID, TableBlock>, availableRows: number) {
  return blockIDs
    .map((id) => blocks[id])
    .sort((a, b) => a.start - b.start)
    .flatMap((block) => block.rows.map((row, offset) => ({ row, index: block.start + offset })))
    .filter((item) => item.index < availableRows)
}
