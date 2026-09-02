import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { escapeHTML, formatField, inlineDataset, legend, type EChartsTranslation } from './common'
import { echartsLabelPolicy } from './label-policy'

type HierarchyNode = { name: string; value?: unknown; __lv_dataset: string; __lv_row_index: number; __lv_synthetic?: boolean; children?: HierarchyNode[] }

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
      const sourceLabel = sourceIndex >= 0 ? String(row[sourceIndex] ?? '').trim() : ''
      const targetLabel = targetIndex >= 0 ? String(row[targetIndex] ?? '').trim() : ''
      const value = valueIndex >= 0 ? Number(row[valueIndex]) : 1
      if (!sourceLabel || !targetLabel || !Number.isFinite(value) || value <= 0) return []
      return [{
        source: spec.mark === 'sankey' ? `source:${sourceLabel}` : sourceLabel,
        target: spec.mark === 'sankey' ? `target:${targetLabel}` : targetLabel,
        sourceLabel, targetLabel, value,
        __lv_dataset: dataset?.id ?? 'primary', __lv_row_index: rowIndex,
      }]
    })
    const graphCircular = spec.mark === 'graph' && spec.presentation.layout === 'circular'
    const nodes = spec.mark === 'sankey'
      ? [...new Map(links.flatMap((link) => [[link.source, link.sourceLabel], [link.target, link.targetLabel]])).entries()].map(([name, displayName]) => ({ name, displayName }))
      : graphCircular
        ? [...new Set(links.flatMap((link) => [link.source, link.target]))].map((name) => ({ name }))
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
        return `${escapeHTML(String(link.sourceLabel ?? link.source))} → ${escapeHTML(String(link.targetLabel ?? link.target))}: ${escapeHTML(value)}`
      } },
    }
    if (spec.mark === 'graph') {
      series.roam = spec.presentation.roam
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
        series.labelLayout = { moveOverlap: 'shiftY' }
      }
      if (spec.presentation.focus === 'adjacency') series.emphasis = { focus: 'adjacency' }
    } else {
      series.orient = spec.presentation.orientation
      if (spec.presentation.nodeGap !== undefined) series.nodeGap = spec.presentation.nodeGap
      series.left = spec.presentation.orientation === 'horizontal' ? '4%' : '3%'
      series.right = spec.presentation.orientation === 'horizontal' ? '30%' : '21%'
      series.top = '8%'
      series.bottom = '8%'
      series.nodeWidth = 18
      series.label = { ...(series.label ?? {}), width: 96 }
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
    id: `series:hierarchy:${spec.mark}`, type: spec.mark, data, roam: spec.presentation.roam,
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
    common.nodeClick = spec.presentation.roam ? 'rootToNode' : false
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
      textBorderColor: context.colors.surface,
      textBorderWidth: 2,
    }
    common.labelLayout = { hideOverlap: false }
  }
  return { legend: legend(spec.presentation.legend, context), series: [common] }
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
  for (let rowIndex = 0; rowIndex < dataset.rows.length; rowIndex++) {
    const row = dataset.rows[rowIndex]!
    const name = String(row[nodeIndex])
    const parent = parentIndex >= 0 && row[parentIndex] !== null && row[parentIndex] !== undefined && row[parentIndex] !== '' ? String(row[parentIndex]) : undefined
    const id = parent ? `${parent}\u001f${escapeSegment(name)}` : escapeSegment(name)
    if (byID.has(id)) throw new Error(`duplicate hierarchy node ${JSON.stringify(id)}`)
    byID.set(id, { name, value: valueIndex >= 0 ? row[valueIndex] : undefined, __lv_dataset: dataset.id, __lv_row_index: rowIndex })
    parentByID.set(id, parent)
  }
  const roots: HierarchyNode[] = []
  for (const [id, node] of byID) {
    const parentID = parentByID.get(id)
    if (!parentID) { roots.push(node); continue }
    const parent = byID.get(parentID)
    if (!parent) throw new Error(`hierarchy node ${JSON.stringify(id)} references missing parent ${JSON.stringify(parentID)}`)
    ;(parent.children ??= []).push(node)
  }
  return roots
}

export function hierarchyTooltipValue(envelope: VisualizationEnvelope, node: HierarchyNode, context: RendererContext): string {
  const spec = envelope.spec
  return spec.kind === 'hierarchy' ? formatField(envelope, spec.value, node.value, context) : String(node.value ?? '—')
}

function escapeSegment(value: string): string { return value.replaceAll('\u001f', '\u001f\u001f') }

function layeredGraphNodes(links: readonly { source: string; target: string }[]) {
  const sources = [...new Set(links.map((link) => link.source))]
  const targets = [...new Set(links.map((link) => link.target))]
  const sourceSet = new Set(sources)
  const targetSet = new Set(targets)
  const sourceOnly = sources.filter((name) => !targetSet.has(name))
  const targetOnly = targets.filter((name) => !sourceSet.has(name))
  const shared = sources.filter((name) => targetSet.has(name))
  const column = (names: readonly string[], x: number, position: 'left' | 'right' | 'top', align: 'left' | 'right' | 'center') => names.map((name, index) => ({
    name,
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
