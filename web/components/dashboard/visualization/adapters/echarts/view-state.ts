import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import { compactScrollLegendGeometry } from './compact-scroll-legend'

const COMPACT_WIDTH = 480
const COMPACT_HEIGHT = 280
const BOUNDED_OUTSIDE_LABEL_WIDTH = 400
const CROWDED_INSIDE_LABEL_WIDTH = 640
const CROWDED_INSIDE_LABEL_HEIGHT = 420
const GAUGE_LABEL_WIDTH = 360
const GAUGE_LABEL_HEIGHT = 220
const RESPONSIVE_LABEL_SEGMENTER = new Intl.Segmenter('en', { granularity: 'grapheme' })

export type EChartsNavigationDefaults = Readonly<{ dataZoom: boolean; roam: boolean }>
export type EChartsViewState = Readonly<{
  dataZoom?: readonly Readonly<Record<string, unknown>>[]
  series?: readonly Readonly<{ id: string; center?: readonly unknown[]; zoom?: number }>[]
}>

export function echartsNavigationDefaults(envelope: VisualizationEnvelope): EChartsNavigationDefaults {
  return {
    dataZoom: envelope.spec.kind === 'cartesian' && envelope.spec.presentation.dataZoom === true,
    roam: envelope.spec.kind === 'hierarchy' && envelope.spec.presentation.roam === true,
  }
}

export function responsiveEChartsLayoutKey(envelope: VisualizationEnvelope, width: number, height: number): string {
  const compact = width < COMPACT_WIDTH || height < COMPACT_HEIGHT
  if (envelope.spec.kind === 'polar' && envelope.spec.mark === 'gauge') {
    return `${compact ? 'compact' : 'roomy'}:gauge-${gaugeTickLabelsHidden(width, height) ? 'quiet' : 'labeled'}`
  }
  if (envelope.spec.kind === 'hierarchy' && envelope.spec.mark === 'graph'
    && (envelope.spec.presentation.layout === 'standard' || envelope.spec.presentation.layout === 'circular')) {
    return `${compact ? 'compact' : 'roomy'}:graph-${graphLabelWidth(width)}`
  }
  if (envelope.spec.kind === 'hierarchy' && envelope.spec.mark === 'tree') {
    return `${compact ? 'compact' : 'roomy'}:tree-${height < 180 ? 'short' : 'normal'}`
  }
  if (envelope.spec.kind !== 'proportional') return compact ? 'compact' : 'roomy'
  if (envelope.spec.mark === 'funnel') {
    const outsideLabels = envelope.spec.presentation.labelPosition !== 'inside'
      && envelope.spec.presentation.labelPolicy.density !== 'hidden'
    if (outsideLabels && compact) {
      const fontSize = envelope.spec.presentation.labelPolicy.density === 'dense' ? 10 : 12
      return `compact:funnel-outside-${funnelOutsideLabelCharacterBudget(width, fontSize)}`
    }
    return compact ? 'compact' : 'roomy'
  }
  if (envelope.spec.presentation.labelPosition === 'inside') {
    return `${compact ? 'compact' : 'roomy'}:inside-${width < CROWDED_INSIDE_LABEL_WIDTH || height < CROWDED_INSIDE_LABEL_HEIGHT ? 'crowded' : 'full'}`
  }
  const bounded = width < BOUNDED_OUTSIDE_LABEL_WIDTH || height < COMPACT_HEIGHT
  return bounded
    ? `${compact ? 'compact' : 'roomy'}:outside-bounded`
    : `${compact ? 'compact' : 'roomy'}:outside-local-${proportionalLabelLineLength(width, height)}-${proportionalLabelLineEndLength(width)}`
}

export function responsiveEChartsPatch(option: Record<string, any>, width: number, height: number): Record<string, any> {
  if (!option || typeof option !== 'object' || !Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return {}
  const compact = width < COMPACT_WIDTH || height < COMPACT_HEIGHT
  const proportionalSeries = responsiveProportionalSeries(option.series, width, height)
  const gaugeSeries = responsiveGaugeSeries(option.series, width, height)
  const graphSeries = responsiveGraphSeries(option.series, width, compact)
  const treeSeries = responsiveSingleNodeTreeSeries(option.series, width, height)
  const gaugeGraphic = responsiveGaugeGraphic(option.graphic, width)
  const responsivePie = hasPieSeries(option.series)
  const patch: Record<string, any> = {}
  const bottomLegend = compact && hasBottomLegend(option.legend)
  if (option.grid !== undefined) {
    const grids = Array.isArray(option.grid) ? option.grid : [option.grid]
    const slider = compact && hasSliderDataZoom(option.dataZoom)
    const compactBottom = 12 + (bottomLegend ? 28 : 0) + (slider ? 42 : 0)
    const grid = grids.map((value: Record<string, any>) => {
      const source = value && typeof value === 'object' && !Array.isArray(value) ? value : {}
      return {
        ...source,
        ...(compact ? {
          left: compactInset(source.left, 8),
          right: compactInset(source.right, 8),
          top: compactInset(source.top, 10),
          bottom: compactBottomInset(source.bottom, compactBottom, option.visualMap !== undefined),
        } : {}),
      }
    })
    patch.grid = Array.isArray(option.grid) ? grid : grid[0]
  }
  if (proportionalSeries !== undefined) patch.series = proportionalSeries
  if (gaugeSeries !== undefined) patch.series = gaugeSeries
  if (graphSeries !== undefined) patch.series = graphSeries
  if (treeSeries !== undefined) patch.series = treeSeries
  if (gaugeGraphic !== undefined) patch.graphic = gaugeGraphic
  if (option.legend !== undefined && (option.grid !== undefined || responsivePie)) {
    patch.legend = compact ? compactLegend(option.legend, width) : desktopLegend(option.legend)
  }
  if (option.dataZoom !== undefined) patch.dataZoom = compact
    ? compactDataZoom(option.dataZoom, bottomLegend, option.visualMap !== undefined)
    : stripDataZoomNavigation(option.dataZoom)
  return patch
}

function gaugeTickLabelsHidden(width: number, height: number): boolean {
  return width < GAUGE_LABEL_WIDTH || height < GAUGE_LABEL_HEIGHT
}

function responsiveGaugeSeries(value: unknown, width: number, height: number): unknown[] | undefined {
  const series = Array.isArray(value) ? value : [value]
  if (!series.some((entry) => entry && typeof entry === 'object' && !Array.isArray(entry)
    && (entry as Record<string, unknown>).type === 'gauge' && (entry as Record<string, unknown>).silent !== true)) return undefined
  const hideTicks = gaugeTickLabelsHidden(width, height)
  return series.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const source = entry as Record<string, any>
    if (source.type !== 'gauge' || source.silent === true) return source
    return {
      ...source,
      splitNumber: hideTicks ? 3 : source.splitNumber ?? 5,
      axisLabel: { ...source.axisLabel, show: !hideTicks },
      axisTick: { ...source.axisTick, show: !hideTicks },
      splitLine: { ...source.splitLine, show: !hideTicks },
    }
  })
}

function responsiveGraphSeries(value: unknown, width: number, compact: boolean): unknown[] | undefined {
  if (!Array.isArray(value)) return undefined
  let hasResponsiveGraph = false
  const series = value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const source = entry as Record<string, any>
    const label = source.label
    if (source.type !== 'graph' || !['none', 'circular'].includes(source.layout) || !label || typeof label !== 'object' || label.show === false) return entry
    hasResponsiveGraph = true
    const graphNodes = Array.isArray(source.data) ? source.data : []
    const compactCircular = compact && source.layout === 'circular' && graphNodes.length > 8
    const visibleCircularLabelIndexes = compactCircular
      ? new Set(Array.from({ length: 4 }, (_, index) => Math.floor(index * graphNodes.length / 4)))
      : undefined
    return {
      ...source,
      label: {
        ...label,
        width: graphLabelWidth(width),
        overflow: 'truncate',
        ellipsis: typeof label.ellipsis === 'string' ? label.ellipsis : '…',
        ...(visibleCircularLabelIndexes ? {
          formatter: (params: { dataIndex?: number }) => visibleCircularLabelIndexes.has(params.dataIndex ?? -1)
            ? label.formatter?.(params)
            : '',
        } : {}),
      },
      ...(compactCircular ? {
        // Dense circular layouts have too little circumference for every node
        // label. Keep four evenly spaced labels visible; the rest are exposed
        // when emphasized and through each node's tooltip.
        emphasis: {
          ...source.emphasis,
          label: {
            ...source.emphasis?.label,
            show: true,
            ...(typeof label.formatter === 'function' ? { formatter: label.formatter } : {}),
          },
        },
      } : {}),
    }
  })
  return hasResponsiveGraph ? series : undefined
}

function responsiveSingleNodeTreeSeries(value: unknown, width: number, height: number): unknown[] | undefined {
  if (!Array.isArray(value)) return undefined
  let hasSingleNodeTree = false
  const series = value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const source = entry as Record<string, any>
    if (source.type !== 'tree' || !Array.isArray(source.data) || source.data.length !== 1
      || source.data[0]?.children?.length || !source.label || typeof source.label !== 'object') return entry
    hasSingleNodeTree = true
    if (height >= 180) return source
    const label = {
      ...source.label,
      position: 'right',
      align: 'left',
      distance: 10,
      fontSize: 14,
      lineHeight: 20,
      width: Math.max(40, Math.floor(width * 0.67)),
      overflow: 'truncate',
    }
    return {
      ...source,
      left: '15%',
      right: '75%',
      symbolSize: 14,
      label,
      leaves: { ...source.leaves, label },
    }
  })
  return hasSingleNodeTree ? series : undefined
}

function graphLabelWidth(width: number): number {
  return Math.max(0, Math.min(160, Math.floor(width * 0.2) - 2))
}

function responsiveGaugeGraphic(value: unknown, width: number): unknown[] | undefined {
  const graphics = Array.isArray(value) ? value : [value]
  if (!graphics.some((entry) => isGaugeDomainDiagnostic(entry))) return undefined
  const fontSize = width < 360 ? 10 : 11
  const textWidth = Math.max(120, width - 24)
  const maxCharacters = Math.max(18, Math.floor(textWidth / (fontSize * 0.55)))
  return graphics.map((entry) => {
    if (!isGaugeDomainDiagnostic(entry)) return entry
    const source = entry as Record<string, any>
    const style = source.style as Record<string, any>
    return {
      ...source,
      style: {
        ...style,
        text: wrapWords(style.text, maxCharacters),
        width: textWidth,
        overflow: 'break',
        fontSize,
        lineHeight: fontSize + 4,
      },
    }
  })
}

function isGaugeDomainDiagnostic(value: unknown): boolean {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const style = (value as Record<string, unknown>).style
  return Boolean(style && typeof style === 'object' && !Array.isArray(style)
    && typeof (style as Record<string, unknown>).text === 'string'
    && ((style as Record<string, string>).text).includes('outside configured gauge domain'))
}

function wrapWords(value: string, maxCharacters: number): string {
  const lines: string[] = []
  let line = ''
  for (const word of value.split(/\s+/)) {
    if (line.length > 0 && line.length + 1 + word.length > maxCharacters) {
      lines.push(line)
      line = ''
    }
    if (word.length <= maxCharacters) {
      line = line ? `${line} ${word}` : word
      continue
    }
    for (let offset = 0; offset < word.length; offset += maxCharacters) {
      const chunk = word.slice(offset, offset + maxCharacters)
      if (line) lines.push(line)
      line = chunk
    }
  }
  if (line) lines.push(line)
  return lines.join('\n')
}

function hasPieSeries(value: unknown): boolean {
  const series = Array.isArray(value) ? value : [value]
  return series.some((entry) => entry && typeof entry === 'object' && !Array.isArray(entry)
    && (entry as Record<string, unknown>).type === 'pie')
}

function responsiveProportionalSeries(value: unknown, width: number, height: number): unknown[] | undefined {
  if (!Array.isArray(value)) return undefined
  const boundedOutsideLabels = width < BOUNDED_OUTSIDE_LABEL_WIDTH || height < COMPACT_HEIGHT
  let hasResponsiveLabels = false
  const series = value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const source = entry as Record<string, unknown>
    const label = source.label
    if (source.type === 'funnel') {
      if (!label || typeof label !== 'object' || Array.isArray(label)) return entry
      const labelOption = label as Record<string, unknown>
      const formatter = labelOption.formatter
      if (
        labelOption.position !== 'outside'
        || labelOption.show === false
        || typeof formatter !== 'function'
      ) return entry
      hasResponsiveLabels = true
      const responsiveFormatter = width < COMPACT_WIDTH || height < COMPACT_HEIGHT
        ? (params: unknown) => wrapFunnelOutsideLabel(
          String(formatter(params) ?? ''),
          width,
          finiteNumber(labelOption.fontSize) ?? 12,
        )
        : formatter
      return {
        ...source,
        label: {
          ...labelOption,
          formatter: responsiveFormatter,
        },
      }
    }
    if (
      source.type !== 'pie'
      || !label || typeof label !== 'object' || Array.isArray(label)
      || (label as Record<string, unknown>).show === false
    ) return entry
    const labelOption = label as Record<string, unknown>
    if (labelOption.position === 'inside') {
      const crowded = width < CROWDED_INSIDE_LABEL_WIDTH || height < CROWDED_INSIDE_LABEL_HEIGHT
      hasResponsiveLabels = true
      if (!crowded) {
        return {
          ...source,
          label: { ...labelOption, rotate: null },
        }
      }
      return {
        ...source,
        label: {
          ...labelOption,
          // Horizontal labels near the centre of neighbouring sectors can
          // visually collide even when ECharts' overlap boxes only touch.
          // Use a smaller label size in card-sized pie views while preserving
          // authored density and full-size focus views.
          fontSize: Math.min(finiteNumber(labelOption.fontSize) ?? 12, 11),
          padding: 0,
          // Radial text follows the available sector depth instead of
          // competing horizontally with labels in neighbouring sectors.
          rotate: 'radial',
        },
      }
    }
    if (labelOption.position !== 'outside') return entry
    hasResponsiveLabels = true
    return {
      ...source,
      label: {
        ...labelOption,
        // Edge alignment keeps text inside narrow cards. In roomy views it
        // stretches guide lines to the host boundary, so anchor labels to
        // their natural guide-line ends instead.
        alignTo: boundedOutsideLabels ? 'edge' : 'labelLine',
        ...(boundedOutsideLabels ? {} : { distanceToLabelLine: 12 }),
      },
      ...(boundedOutsideLabels ? {} : {
        labelLine: {
          ...((source.labelLine && typeof source.labelLine === 'object' && !Array.isArray(source.labelLine))
            ? source.labelLine as Record<string, unknown>
            : {}),
          length: proportionalLabelLineLength(width, height),
          length2: proportionalLabelLineEndLength(width),
        },
      }),
    }
  })
  return hasResponsiveLabels ? series : undefined
}

function wrapFunnelOutsideLabel(value: string, width: number, fontSize: number): string {
  const separator = value.lastIndexOf(': ')
  if (separator <= 0 || separator >= value.length - 2) return value
  const availableCharacters = funnelOutsideLabelCharacterBudget(width, fontSize)
  if (value.length <= availableCharacters) return value
  const category = truncateResponsiveLabel(value.slice(0, separator), availableCharacters - 1)
  const amount = truncateResponsiveLabel(value.slice(separator + 2), availableCharacters)
  return `${category}:\n${amount}`
}

function funnelOutsideLabelCharacterBudget(width: number, fontSize: number): number {
  return Math.floor((width * 0.43) / (fontSize * 0.5))
}

function truncateResponsiveLabel(value: string, maxCharacters: number): string {
  const graphemes = [...RESPONSIVE_LABEL_SEGMENTER.segment(value)].map((segment) => segment.segment)
  if (graphemes.length <= maxCharacters) return value
  if (maxCharacters <= 1) return maxCharacters === 1 ? '…' : ''
  return `${graphemes.slice(0, maxCharacters - 1).join('')}…`
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function proportionalLabelLineLength(width: number, height: number): number {
  return Math.min(64, Math.max(24, Math.round(Math.min(width, height) * 0.08)))
}

function proportionalLabelLineEndLength(width: number): number {
  return Math.min(48, Math.max(20, Math.round(width * 0.035)))
}

function compactInset(value: unknown, fallback: number): unknown {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.min(value, fallback)
  if (typeof value === 'string') return value
  return fallback
}

function compactBottomInset(value: unknown, fallback: number, preserveExisting: boolean): unknown {
  if (preserveExisting && typeof value === 'number' && Number.isFinite(value)) return Math.max(value, fallback)
  if (typeof value === 'string') return value
  return fallback
}

function hasBottomLegend(value: unknown): boolean {
  const legends = Array.isArray(value) ? value : [value]
  return legends.some(isHorizontalBottomLegend)
}

function isHorizontalBottomLegend(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value)
    && (value as Record<string, unknown>).orient !== 'vertical'
    && (value as Record<string, unknown>).bottom !== undefined)
}

function hasSliderDataZoom(value: unknown): boolean {
  if (!Array.isArray(value)) return false
  return value.some((entry) => entry && typeof entry === 'object' && !Array.isArray(entry) && (entry as Record<string, unknown>).type === 'slider')
}

function desktopLegend(value: unknown): unknown {
  const legends = Array.isArray(value) ? value : [value]
  const result = legends.map((entry) => {
    if (!isHorizontalBottomLegend(entry)) return entry
    const legend = entry
    // Clear compact sizing, retaining the scroll component and its selection state.
    return {
      type: 'scroll', left: 'center', right: 'auto', width: 'auto', height: 'auto', ...legend,
      itemWidth: finiteNumber(legend.itemWidth) ?? 25,
      itemHeight: finiteNumber(legend.itemHeight) ?? 14,
      textStyle: {
        ...((legend.textStyle && typeof legend.textStyle === 'object' && !Array.isArray(legend.textStyle))
          ? legend.textStyle as Record<string, unknown>
          : {}),
        width: null, overflow: null, ellipsis: null, backgroundColor: null,
      },
    }
  })
  return Array.isArray(value) ? result : result[0]
}

function compactLegend(value: unknown, width: number): unknown {
  const legends = Array.isArray(value) ? value : [value]
  const result = legends.map((entry) => {
    if (!isHorizontalBottomLegend(entry)) return entry
    const legend = entry
    const responsive = {
      ...legend,
      type: 'scroll', bottom: 0, left: 'center', right: 'auto',
      width: Math.max(0, width - 16), height: 24,
      pageIconColor: (legend.textStyle as Record<string, unknown> | undefined)?.color,
      pageTextStyle: legend.textStyle,
    }
    return { ...responsive, ...compactScrollLegendGeometry(responsive, width) }
  })
  return Array.isArray(value) ? result : result[0]
}

function compactDataZoom(value: unknown, bottomLegend: boolean, preserveExisting: boolean): unknown {
  if (!Array.isArray(value)) return stripDataZoomNavigation(value)
  return value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const layout = stripDataZoomNavigationEntry(entry as Record<string, unknown>)
    if (layout.type !== 'slider') return layout
    const fallback = bottomLegend ? 28 : 12
    const bottom = preserveExisting && typeof layout.bottom === 'number' && Number.isFinite(layout.bottom)
      ? Math.max(layout.bottom, fallback)
      : fallback
    return { ...layout, bottom }
  })
}

function stripDataZoomNavigation(value: unknown): unknown {
  if (!Array.isArray(value)) {
    return value && typeof value === 'object' ? stripDataZoomNavigationEntry(value as Record<string, unknown>) : value
  }
  return value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    return stripDataZoomNavigationEntry(entry as Record<string, unknown>)
  })
}

function stripDataZoomNavigationEntry(entry: Record<string, unknown>): Record<string, unknown> {
  const layout = { ...entry }
  delete layout.start
  delete layout.end
  delete layout.startValue
  delete layout.endValue
  return layout
}

export function overlayDataZoomNavigation(layout: unknown, state: EChartsViewState['dataZoom']): unknown {
  if (!Array.isArray(layout) || !Array.isArray(state)) return layout
  return layout.map((entry, index) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const current = state[index]
    if (!current) return entry
    const result = { ...(entry as Record<string, unknown>) }
    for (const key of ['start', 'end', 'startValue', 'endValue']) {
      if (current[key] !== undefined) result[key] = current[key]
    }
    return result
  })
}

export function captureEChartsViewState(option: Record<string, any>): EChartsViewState {
  if (!option || typeof option !== 'object') return {}
  const dataZoom = Array.isArray(option.dataZoom)
    ? option.dataZoom.flatMap((value: Record<string, any>) => {
        if (!value || typeof value !== 'object' || Array.isArray(value)) return []
        const state = Object.fromEntries(
          ['start', 'end', 'startValue', 'endValue'].flatMap((key) => value[key] === undefined ? [] : [[key, value[key]]]),
        )
        return Object.keys(state).length > 0 ? [state] : []
      })
    : undefined
  const series = Array.isArray(option.series)
    ? option.series.flatMap((value: Record<string, any>) => {
        if (!value || typeof value !== 'object' || Array.isArray(value)) return []
        if (typeof value.id !== 'string') return []
        const center = Array.isArray(value.center) ? [...value.center] : undefined
        const zoom = typeof value.zoom === 'number' && Number.isFinite(value.zoom) ? value.zoom : undefined
        return center === undefined && zoom === undefined ? [] : [{ id: value.id, ...(center ? { center } : {}), ...(zoom === undefined ? {} : { zoom }) }]
      })
    : undefined
  return {
    ...(dataZoom && dataZoom.length > 0 ? { dataZoom } : {}),
    ...(series && series.length > 0 ? { series } : {}),
  }
}
