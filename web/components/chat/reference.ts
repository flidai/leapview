import {
  Box,
  ChartArea,
  ChartBar,
  ChartCandlestick,
  ChartColumn,
  ChartColumnBig,
  ChartColumnDecreasing,
  ChartLine,
  ChartNetwork,
  ChartNoAxesCombined,
  ChartPie,
  ChartScatter,
  CircleDot,
  Columns3,
  Database,
  File,
  Filter,
  Funnel,
  Gauge,
  GitBranch,
  Grid2X2,
  Grid3X3,
  LayoutDashboard,
  Map,
  PanelsTopLeft,
  Plug,
  Radar,
  Search,
  Sigma,
  SigmaSquare,
  Table2,
  TableCellsSplit,
  TableProperties,
  Workflow,
  Waypoints,
  type IconNode,
} from 'lucide'
import type { AgentContextSignal, AgentReferenceSignal, ChatTranscriptItemSignal } from '../../generated/signals'
import { lucideIcon } from '../shared/lucide-icons'

export type ChatContextReference = AgentReferenceSignal

export interface ChatReferenceSearchDetail {
  query: string
  requestId: number
}

export interface ChatReferencesChangeDetail {
  references: AgentReferenceSignal[]
}

export const defaultAgentReferenceLimit = 12

export function latestAcceptedRunId(transcript: ChatTranscriptItemSignal[]): string {
	for (let index = transcript.length - 1; index >= 0; index -= 1) {
		const item = transcript[index]
		if (item?.kind === 'user' && item.runId?.trim()) return item.runId.trim()
	}
	return ''
}

export function normalizeReferenceLimit(limit: number | null | undefined): number {
  return Number.isFinite(limit) && Number(limit) > 0
    ? Math.floor(Number(limit))
    : defaultAgentReferenceLimit
}

export function referenceIdentity(reference: AgentReferenceSignal): string {
  return `${reference.reference.projectId}:${reference.reference.type}:${reference.reference.id}`
}

export function referenceKindLabel(kind: string): string {
  return kind
    .trim()
    .split(/[_\s-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toLocaleUpperCase() + part.slice(1))
    .join(' ')
}

export function uniqueReferences(references: AgentReferenceSignal[]): AgentReferenceSignal[] {
  const seen = new Set<string>()
  return references.filter((reference) => {
    const key = referenceIdentity(reference)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

const mentionStopWords = new Set(['a', 'an', 'and', 'by', 'for', 'in', 'of', 'on', 'the', 'to'])

export function normalizedReferenceQuery(query: string): string {
  return query.trim().toLocaleLowerCase()
}

export function matchesReferenceQuery(reference: AgentReferenceSignal, query: string): boolean {
  const tokens = normalizedReferenceQuery(query)
    .split(/[^\p{L}\p{N}_]+/u)
    .filter((token) => token !== '' && !mentionStopWords.has(token))
  if (tokens.length === 0) return true
  const haystack = `${reference.name} ${reference.description ?? ''} ${reference.reference.type} ${referenceHierarchy(reference).join(' ')}`.toLocaleLowerCase()
  return tokens.every((token) => haystack.includes(token))
}

export function referenceHierarchy(reference: AgentReferenceSignal): string[] {
	const projected = (reference.hierarchy ?? []).map((part) => part.trim()).filter(Boolean)
	if (projected.length > 0) return projected

	const hierarchy = [reference.project.name.trim()].filter(Boolean)
	const location = reference.locations[0]
	if (reference.reference.type === 'page' || reference.reference.type === 'visual') {
		if (location?.dashboardName?.trim()) hierarchy.push(location.dashboardName.trim())
	}
	if (reference.reference.type === 'visual' && location?.pageName?.trim()) {
		hierarchy.push(location.pageName.trim())
	}
	return hierarchy
}

export function isOnPageReference(reference: AgentReferenceSignal, context: AgentContextSignal | null): boolean {
  if (reference.context.includes('current_page')) return true
  return Boolean(context?.projectId && context.dashboardId && context.pageId
    && reference.reference.projectId === context.projectId
    && reference.locations.some((location) => location.dashboardId === context.dashboardId && location.pageId === context.pageId))
}

export function mergeReferences(...groups: AgentReferenceSignal[][]): AgentReferenceSignal[] {
  return uniqueReferences(groups.flat())
}

type ReferenceIcon = { name: string; icon: IconNode }

const visualReferenceIcons: Record<string, ReferenceIcon> = {
  line: { name: 'line', icon: ChartLine },
  area: { name: 'area', icon: ChartArea },
  bar: { name: 'bar', icon: ChartBar },
  column: { name: 'column', icon: ChartColumn },
  pie: { name: 'pie', icon: ChartPie },
  donut: { name: 'donut', icon: ChartPie },
  scatter: { name: 'scatter', icon: ChartScatter },
  funnel: { name: 'funnel', icon: Funnel },
  treemap: { name: 'treemap', icon: Grid2X2 },
  gauge: { name: 'gauge', icon: Gauge },
  heatmap: { name: 'heatmap', icon: Grid3X3 },
  sankey: { name: 'sankey', icon: Workflow },
  graph: { name: 'graph', icon: ChartNetwork },
  map: { name: 'map', icon: Map },
  candlestick: { name: 'candlestick', icon: ChartCandlestick },
  boxplot: { name: 'boxplot', icon: Box },
  combo: { name: 'combo', icon: ChartNoAxesCombined },
  waterfall: { name: 'waterfall', icon: ChartColumnDecreasing },
  histogram: { name: 'histogram', icon: ChartColumnBig },
  radar: { name: 'radar', icon: Radar },
  tree: { name: 'tree', icon: GitBranch },
  sunburst: { name: 'sunburst', icon: CircleDot },
  kpi: { name: 'kpi', icon: SigmaSquare },
  table: { name: 'table', icon: Table2 },
  matrix: { name: 'matrix', icon: TableCellsSplit },
  pivot: { name: 'pivot', icon: TableProperties },
}

export function referenceIcon(kind: string, visualType = '') {
  const normalizedKind = kind.trim().toLocaleLowerCase()
  const normalizedVisualType = visualType.trim().toLocaleLowerCase()
  let resolved: ReferenceIcon
  if (normalizedKind === 'visual') {
		resolved = visualReferenceIcons[normalizedVisualType] ?? { name: 'visual', icon: ChartColumn }
  } else {
    resolved = referenceKindIcon(normalizedKind)
  }
  return lucideIcon(resolved.icon, { className: `reference-icon-${resolved.name}` })
}

function referenceKindIcon(kind: string): ReferenceIcon {
  switch (kind) {
    case 'dashboard': return { name: 'dashboard', icon: LayoutDashboard }
    case 'page': return { name: 'page', icon: PanelsTopLeft }
    case 'filter': return { name: 'filter', icon: Filter }
    case 'semantic_model': return { name: 'semantic-model', icon: Waypoints }
    case 'dataset':
    case 'semantic_table': return { name: 'semantic-table', icon: Database }
    case 'measure': return { name: 'measure', icon: Sigma }
    case 'field': return { name: 'field', icon: Columns3 }
    case 'source': return { name: 'source', icon: Plug }
    case 'table': return { name: 'table', icon: Table2 }
    case 'asset': return { name: 'asset', icon: File }
    default: return { name: 'search', icon: Search }
  }
}
