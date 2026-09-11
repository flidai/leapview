import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { categoryIdentity } from './category-colors'
import { escapeHTML, formatField, inlineDataset, legend, type EChartsTranslation } from './common'
import { echartsLabelPolicy } from './label-policy'

type HierarchyNode = { name: string; value?: unknown; __lv_dataset: string; __lv_row_index: number; __lv_synthetic?: boolean; children?: HierarchyNode[]; label?: EChartsTranslation }

export function hierarchyOption(envelope: VisualizationEnvelope, context: RendererContext): EChartsTranslation {
  const spec = envelope.spec
  if (spec.kind !== 'hierarchy') return {}
  const dataset = inlineDataset(envelope, spec.node.dataset)
  const labels = echartsLabelPolicy(
    envelope,
    spec.node.dataset,
    spec.presentation.labelPolicy,
    (params) => String((params as { data?: { name?: unknown; displayName?: unknown } }).data?.displayName ?? (params as { data?: { name?: unknown } }).data?.name ?? ''),
    context,
  )
  if (spec.mark === 'sankey' || spec.mark === 'graph') {
    const columns = dataset?.columns ?? []
    const sourceIndex = spec.source ? columns.indexOf(spec.source.field) : -1
    const targetIndex = spec.target ? columns.indexOf(spec.target.field) : -1
    const valueIndex = spec.value ? columns.indexOf(spec.value.field) : -1
    const links = (dataset?.rows ?? []).flatMap((row, rowIndex) => {
      const sourceValue = sourceIndex >= 0 ? row[sourceIndex] : undefined
      const targetValue = targetIndex >= 0 ? row[targetIndex] : undefined
      const sourceLabel = String(sourceValue ?? '').trim()
      const targetLabel = String(targetValue ?? '').trim()
      const value = valueIndex >= 0 ? Number(row[valueIndex]) : 1
      if (!sourceLabel || !targetLabel || !Number.isFinite(value) || value <= 0) return []
      return [{
        source: spec.mark === 'sankey' ? `source:${categoryIdentity(sourceValue)}` : categoryIdentity(sourceValue),
        target: spec.mark === 'sankey' ? `target:${categoryIdentity(targetValue)}` : categoryIdentity(targetValue),
        sourceLabel, targetLabel, value,
        __lv_dataset: dataset?.id ?? 'primary', __lv_row_index: rowIndex,
      }]
    })
    const graphCircular = spec.mark === 'graph' && spec.presentation.layout === 'circular'
    const verticalSankey = spec.mark === 'sankey' && spec.presentation.orientation === 'vertical'
    const nodes = spec.mark === 'sankey'
      ? [...new Map(links.flatMap((link) => [[link.source, link.sourceLabel], [link.target, link.targetLabel]])).entries()].map(([name, displayName]) => ({
          name,
          displayName,
          ...(verticalSankey && name.startsWith('target:') ? { label: { position: 'bottom', rotate: 45, distance: 5, align: 'left' } } : {}),
        }))
      : graphCircular
        ? [...new Map(links.flatMap((link) => [[link.source, link.sourceLabel], [link.target, link.targetLabel]])).entries()].map(([name, displayName]) => ({ name, displayName }))
        : layeredGraphNodes(links)
    const series: EChartsTranslation = {
      id: `series:hierarchy:${spec.mark}`, type: spec.mark, data: nodes, links,
      lineStyle: spec.mark === 'sankey'
        ? { color: 'gradient', opacity: 0.45, ...(spec.presentation.curveness === undefined ? {} : { curveness: spec.presentation.curveness }) }
        : spec.presentation.curveness === undefined ? {} : { curveness: spec.presentation.curveness },
      ...labels,
      tooltip: { formatter: (params: { data?: { source?: unknown; target?: unknown; sourceLabel?: unknown; targetLabel?: unknown; value?: unknown } }) => {
        const link = params.data
        if (!link || link.source === undefined || link.target === undefined) return ''
        const value = formatField(envelope, spec.value, link.value, context)
        return `${escapeHTML(String(link.sourceLabel ?? link.source))} to ${escapeHTML(String(link.targetLabel ?? link.target))}: ${escapeHTML(value)}`
      } },
    }
    if (spec.mark === 'graph') {
      series.roam = spec.presentation.roam === true
      series.layout = graphCircular ? 'circular' : 'none'
      series.left = graphCircular ? '8%' : '30%'
      series.right = graphCircular ? '8%' : '30%'
      series.top = graphCircular ? '8%' : '12%'
      series.bottom = graphCircular ? '8%' : '12%'
      series.symbolSize = 16
      series.label = { ...(series.label ?? {}), position: 'right', distance: 8, fontSize: 13 }
      series.itemStyle = { borderColor: context.colors.surface, borderWidth: 2 }
      series.lineStyle = { ...(series.lineStyle ?? {}), color: context.colors.muted, opacity: 0.7, width: 1.5 }
      if (graphCircular) {
        series.center = ['50%', '52%']
        series.zoom = 0.76
        series.labelLayout = withLabelMove(series.labelLayout, 'shiftY')
      }
      if (spec.presentation.focus === 'adjacency') series.emphasis = { focus: 'adjacency' }
    } else {
      series.orient = spec.presentation.orientation
      if (spec.presentation.nodeGap !== undefined) series.nodeGap = spec.presentation.nodeGap
      series.left = spec.presentation.orientation === 'horizontal' ? '4%' : '3%'
      series.right = spec.presentation.orientation === 'horizontal' ? '30%' : '21%'
      series.top = '8%'
      series.bottom = verticalSankey ? '18%' : '8%'
      series.nodeWidth = 18
      series.label = { ...(series.label ?? {}), width: 56, overflow: 'truncate', ellipsis: '…', fontSize: 11 }
      series.itemStyle = { borderColor: context.colors.surface, borderWidth: 1 }
    }
    return {
      legend: legend(spec.presentation.legend, context),
      graphic: links.length === 0 ? [{ type: 'text', left: 'center', top: 'middle', silent: true, style: { text: 'No flow data', fill: context.colors.muted, fontFamily: context.fontFamily, textAlign: 'center' } }] : undefined,
      series: [series],
    }
  }
  const roots = hierarchyData(envelope)
  const data = spec.mark === 'tree' && roots.length > 1 && dataset
    ? [{ name: 'All', __lv_dataset: dataset.id, __lv_row_index: -1, __lv_synthetic: true, children: roots }]
    : roots
  const common: EChartsTranslation = {
    id: `series:hierarchy:${spec.mark}`, type: spec.mark, data, roam: spec.presentation.roam === true,
    ...labels,
    tooltip: { formatter: (params: { data?: HierarchyNode }) => params.data ? `${escapeHTML(params.data.name)}: ${escapeHTML(hierarchyTooltipValue(envelope, params.data, context))}` : '' },
  }
  if (spec.mark === 'tree') {
    common.orient = spec.presentation.orientation === 'vertical' ? 'TB' : 'LR'
    common.layout = spec.presentation.layout === 'circular' ? 'radial' : 'orthogonal'
    common.initialTreeDepth = spec.presentation.initialDepth
    if (spec.presentation.orientation === 'horizontal') {
      const label = common.label ?? {}
      const background = { backgroundColor: context.colors.surface, borderRadius: 2, padding: [2, 4] }
      common.label = { ...label, ...background, position: 'left', align: 'right' }
      common.leaves = { label: { ...label, ...background, position: 'right', align: 'left' } }
      common.left = '8%'
      common.right = '25%'
      common.top = '8%'
      common.bottom = '8%'
    }
    if (countHierarchyLeaves(data) > 16) {
      const topLevelLabel = { ...(common.label ?? {}), show: common.label?.show !== false }
      common.label = { ...(common.label ?? {}), show: false }
      for (const node of data) node.label = topLevelLabel
      common.leaves = { ...(common.leaves ?? {}), label: { ...(common.leaves?.label ?? {}), show: false } }
    }
  }
  if (spec.mark === 'treemap') {
    common.breadcrumb = { show: spec.presentation.breadcrumb }
    common.leafDepth = spec.presentation.initialDepth
    common.label = {
      ...(common.label ?? {}),
      color: '#fff',
      ...(context.theme === 'light' ? {
        textBorderColor: 'rgba(0, 0, 0, 0.55)',
        textBorderWidth: 2,
      } : {
        textBorderColor: 'rgba(255, 255, 255, 0.45)',
        textBorderWidth: 1,
      }),
    }
  }
  if (spec.mark === 'sunburst') {
    common.nodeClick = spec.presentation.roam === true ? 'rootToNode' : false
    common.radius = ['10%', '92%']
    common.label = {
      ...(common.label ?? {}),
      position: 'inside',
      rotate: 'radial',
      width: 68,
      overflow: 'truncate',
      ellipsis: '...',
      fontSize: 10,
      fontWeight: 600,
      lineHeight: 13,
      minAngle: 8,
      textBorderColor: context.colors.surface,
      textBorderWidth: 2,
    }
  }
  return { legend: legend(spec.presentation.legend, context), series: [common] }
}

function countHierarchyLeaves(nodes: HierarchyNode[]): number {
  return nodes.reduce((count, node) => count + (node.children?.length ? countHierarchyLeaves(node.children) : 1), 0)
}

function withLabelMove(layout: EChartsTranslation, moveOverlap: 'shiftY'): EChartsTranslation {
  return typeof layout === 'function'
    ? (params: { dataIndex?: number }) => ({ ...layout(params), moveOverlap })
    : { ...layout, moveOverlap }
}

export function hierarchyData(envelope: VisualizationEnvelope): HierarchyNode[] {
  const spec = envelope.spec
  if (spec.kind !== 'hierarchy' || spec.mark === 'graph' || spec.mark === 'sankey') return []
  const dataset = inlineDataset(envelope, spec.node.dataset)
  if (!dataset) return []
  const nodeIndex = dataset.columns.indexOf(spec.node.field)
  const parentIndex = spec.parent ? dataset.columns.indexOf(spec.parent.field) : -1
  const valueIndex = spec.value ? dataset.columns.indexOf(spec.value.field) : -1
  const byID = new Map<string, HierarchyNode>()
  const parentByID = new Map<string, string | undefined>()
  const pending = dataset.rows.map((row, rowIndex) => ({
    row,
    rowIndex,
    rawNode: row[nodeIndex],
    rawParent: parentIndex >= 0 ? row[parentIndex] : undefined,
  }))
  const addNode = (entry: typeof pending[number], parent: string | undefined) => {
    const identity = categoryIdentity(entry.rawNode)
    const id = parent ? `${parent}\u001f${escapeSegment(identity)}` : escapeSegment(identity)
    if (byID.has(id)) throw new Error(`duplicate hierarchy node ${JSON.stringify(id)}`)
    const name = entry.rawNode === null || entry.rawNode === undefined ? '—' : String(entry.rawNode)
    byID.set(id, { name, value: valueIndex >= 0 ? entry.row[valueIndex] : undefined, __lv_dataset: dataset.id, __lv_row_index: entry.rowIndex })
    parentByID.set(id, parent)
  }
  const roots = pending.filter(({ rawParent }) => rawParent === null || rawParent === undefined || rawParent === '')
  for (const entry of roots) addNode(entry, undefined)
  let unresolved = pending.filter(({ rawParent }) => rawParent !== null && rawParent !== undefined && rawParent !== '')
  while (unresolved.length > 0) {
    const next: typeof unresolved = []
    let added = 0
    for (const entry of unresolved) {
      const parent = hierarchyPathIdentity(entry.rawParent).find((candidate) => byID.has(candidate))
      if (!parent) { next.push(entry); continue }
      addNode(entry, parent)
      added++
    }
    if (added === 0) {
      const entry = next[0]!
      throw new Error(`hierarchy node ${JSON.stringify(categoryIdentity(entry.rawNode))} references missing parent ${JSON.stringify(hierarchyPathIdentity(entry.rawParent))}`)
    }
    unresolved = next
  }
  const result: HierarchyNode[] = []
  for (const [id, node] of byID) {
    const parentID = parentByID.get(id)
    if (!parentID) { result.push(node); continue }
    const parent = byID.get(parentID)
    if (!parent) throw new Error(`hierarchy node ${JSON.stringify(id)} references missing parent ${JSON.stringify(parentID)}`)
    ;(parent.children ??= []).push(node)
  }
  return result
}

export function hierarchyTooltipValue(envelope: VisualizationEnvelope, node: HierarchyNode, context: RendererContext): string {
  const spec = envelope.spec
  return spec.kind === 'hierarchy' ? formatField(envelope, spec.value, node.value, context) : String(node.value ?? '—')
}

function escapeSegment(value: string): string { return value.replaceAll('\u001f', '\u001f\u001f') }

function hierarchyPathIdentity(value: unknown): string[] {
  if (typeof value !== 'string' || !value.includes('\u001f')) return [categoryIdentity(value)]
  const segments: string[] = []
  let segment = ''
  for (let index = 0; index < value.length; index++) {
    const character = value[index]
    if (character !== '\u001f') {
      segment += character
      continue
    }
    if (value[index + 1] === '\u001f') {
      segment += '\u001f'
      index++
    } else {
      segments.push(segment)
      segment = ''
    }
  }
  segments.push(segment)
  // Go's HierarchyNodeIdentity receives the already canonical parent path and
  // escapes only the newly appended display segment.  Mirror that contract
  // here so an escaped single-segment parent remains distinct from a nested
  // path whose separator is literal (for example A\u001fB).
  const canonical = segments
    .map((candidate) => escapeSegment(categoryIdentity(candidate)))
    .reduce((path, identity) => path ? `${path}\u001f${identity}` : identity, '')
  return [canonical]
}

function layeredGraphNodes(links: readonly { source: string; target: string; sourceLabel: string; targetLabel: string }[]) {
  const sources = [...new Set(links.map((link) => link.source))]
  const targets = [...new Set(links.map((link) => link.target))]
  const labels = new Map(links.flatMap((link) => [[link.source, link.sourceLabel], [link.target, link.targetLabel]]))
  const sourceSet = new Set(sources)
  const targetSet = new Set(targets)
  const sourceOnly = sources.filter((name) => !targetSet.has(name))
  const targetOnly = targets.filter((name) => !sourceSet.has(name))
  const shared = sources.filter((name) => targetSet.has(name))
  const column = (names: readonly string[], x: number, position: 'left' | 'right' | 'top', align: 'left' | 'right' | 'center') => names.map((name, index) => ({
    name, displayName: labels.get(name) ?? name,
    x,
    y: names.length === 1 ? 50 : (index / (names.length - 1)) * 100,
    label: { position, align },
  }))
  return [
    ...column(sourceOnly, 0, 'left', 'right'),
    ...column(shared, 50, 'top', 'center'),
    ...column(targetOnly, 100, 'right', 'left'),
  ]
}
