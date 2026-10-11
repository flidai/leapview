import { format } from 'echarts'

const GAUGE_LABEL_WIDTH = 360
const GAUGE_LABEL_HEIGHT = 220

export function responsiveRadar(option: Record<string, any>, width: number, height: number): any {
  if (option.radar === undefined) return undefined
  const radars = Array.isArray(option.radar) ? option.radar : [option.radar]
  const result = radars.map((source: Record<string, any>) => {
    const label = source.axisName ?? {}
    const gap = finiteNumber(source.axisNameGap) ?? 15
    const originalRadius = source.radius ?? '50%'
    const radius = layoutPixels(originalRadius, Math.min(width, height) / 2)
    const gutter = Math.max(0, width / 2 - radius - gap - 8)
    const font = `${label.fontSize ?? 12}px ${label.fontFamily ?? option.textStyle?.fontFamily ?? 'sans-serif'}`
    const namesFit = (source.indicator ?? []).every((indicator: { name: string }) => {
      const name = typeof label.formatter === 'function' ? label.formatter(indicator.name, indicator) : indicator.name
      return format.getTextRect(String(name), font).width <= gutter
    })
    const budget = namesFit ? undefined : Math.max(0, Math.floor(Math.min(160, width * 0.2)))
    const responsiveRadius = budget === undefined ? radius : Math.max(0, Math.min(radius, width / 2 - budget - gap - 8))
    const visibleNames = source.__lv_axis_name_policy?.density === 'automatic'
      ? radarVisibleNameIndexes(source, width, height, responsiveRadius + gap, budget, font) : undefined
    return { ...source,
      radius: budget === undefined ? originalRadius : responsiveRadius,
      // Radar names are rendered by AxisBuilder, which reads nameTruncate
      // from each indicator rather than axisName.width.
      indicator: (source.indicator ?? []).map((indicator: Record<string, any>, index: number) => ({ ...indicator,
        ...(visibleNames ? { showName: indicator.showName !== false && visibleNames.has(index) } : {}),
        nameTruncate: { ...indicator.nameTruncate,
          maxWidth: budget === undefined ? indicator.nameTruncate?.maxWidth ?? null : budget,
          ellipsis: indicator.nameTruncate?.ellipsis ?? '…',
        },
      })),
    }
  })
  return Array.isArray(option.radar) ? result : result[0]
}

function radarVisibleNameIndexes(source: Record<string, any>, width: number, height: number, radius: number, budget: number | undefined, font: string): Set<number> {
  const indicators = source.indicator ?? []
  const padding = (finiteNumber(source.__lv_axis_name_policy?.minimumSpacing) ?? 6) / 2
  const rectangles = indicators.map((indicator: Record<string, any>, index: number) => {
    const name = typeof source.axisName?.formatter === 'function' ? source.axisName.formatter(indicator.name, indicator) : indicator.name
    const measured = format.getTextRect(String(name), font)
    const textWidth = Math.min(measured.width, budget ?? Infinity)
    const angle = ((finiteNumber(source.startAngle) ?? 90) * Math.PI / 180)
      + (source.clockwise ? -1 : 1) * index * Math.PI * 2 / indicators.length
    const cosine = Math.cos(angle), sine = Math.sin(angle)
    const centered = Math.abs(cosine) < 0.0001
    const x = width / 2 + radius * cosine - (centered ? textWidth / 2 : cosine < 0 ? textWidth : 0)
    const y = height / 2 - radius * sine - (centered ? sine > 0 ? measured.height : 0 : measured.height / 2)
    return { x: x - padding, y: y - padding, right: x + textWidth + padding, bottom: y + measured.height + padding }
  })
  for (let stride = 1; stride <= indicators.length; stride++) {
    const visible = rectangles.map((_: unknown, index: number) => index).filter((index: number) => index % stride === 0)
    const overlaps = visible.some((index: number, offset: number) => visible.slice(offset + 1).some((other: number) => {
      const a = rectangles[index], b = rectangles[other]
      return a.x < b.right && a.right > b.x && a.y < b.bottom && a.bottom > b.y
    }))
    if (!overlaps) return new Set(visible)
  }
  return new Set()
}

export function responsiveBoxplotCategoryAxis(option: Record<string, any>, width: number, height: number): unknown {
  if (!Array.isArray(option.series) || !option.series.some((series: Record<string, any>) => series?.type === 'boxplot')) return undefined
  const axes = Array.isArray(option.xAxis) ? option.xAxis : [option.xAxis]
  let hasCategoryAxis = false
  const budget = boxplotLabelWidth(width, height)
  const result = axes.map((axis: Record<string, any> | undefined) => {
    if (!axis || axis.type !== 'category') return axis
    hasCategoryAxis = true
    const label = axis.axisLabel ?? {}
    // Bound display labels while keeping full category names, formatters,
    // and tooltip values. Rotated labels must leave room for the plot itself.
    return { ...axis, axisLabel: {
      ...label,
      width: Math.min(typeof label.width === 'number' && Number.isFinite(label.width) ? label.width : budget, budget),
      overflow: 'truncate', ellipsis: label.ellipsis ?? '…',
    } }
  })
  return hasCategoryAxis ? Array.isArray(option.xAxis) ? result : result[0] : undefined
}

export function boxplotLabelWidth(width: number, height: number): number {
  return Math.max(0, Math.floor(Math.min(240, width * 0.4, height * 0.35)))
}

export function gaugeTickLabelsHidden(width: number, height: number): boolean {
  return width < GAUGE_LABEL_WIDTH || height < GAUGE_LABEL_HEIGHT
}

export function responsiveGaugeSeries(value: unknown, width: number, height: number): unknown[] | undefined {
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

export function responsiveGraphSeries(value: unknown, width: number, compact: boolean): unknown[] | undefined {
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
      ...(compact && source.layout === 'circular' ? {
        left: Math.max(layoutPixels(source.left, width), graphLabelWidth(width) + (finiteNumber(label.distance) ?? 8) + 16),
        right: Math.max(layoutPixels(source.right, width), graphLabelWidth(width) + (finiteNumber(label.distance) ?? 8) + 16),
      } : { left: source.left, right: source.right }),
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

export function responsiveSingleNodeTreeSeries(value: unknown, width: number, height: number): unknown[] | undefined {
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

export function responsiveHierarchySeries(value: unknown, width: number, height: number): unknown[] | undefined {
  if (!Array.isArray(value)) return undefined
  let changed = false
  const series = value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const source = entry as Record<string, unknown>
    if (source.type !== 'tree' && source.type !== 'sankey') return entry
    let responsive = source
    const left = percentNumber(source.left)
    const right = percentNumber(source.right)
    if (left !== undefined && right !== undefined && left !== right) {
      changed = true
      if (expandedCenteredBounds(width, height)) {
        const inset = `${(left + right) / 2}%`
        responsive = { ...source, left: inset, right: inset }
      }
    }
    if (source.type === 'tree' && source.layout === 'orthogonal' && source.orient === 'LR'
      && Array.isArray(source.data) && source.data.some((node) => node?.children?.length)) {
      changed = true
      const narrow = width < 800
      const budget = graphLabelWidth(width)
      const boundLabel = (label: any) => ({ ...label,
        width: narrow ? Math.min(finiteNumber(label?.width) ?? budget, budget) : label?.width ?? null,
        overflow: narrow ? 'truncate' : label?.overflow ?? null, ellipsis: label?.ellipsis ?? '…',
      })
      responsive = { ...responsive,
        ...(narrow ? { left: Math.max(layoutPixels(responsive.left, width), budget + 20),
          right: Math.max(layoutPixels(responsive.right, width), budget + 20) } : {}),
        label: boundLabel(source.label), leaves: { ...(source.leaves as Record<string, unknown>),
          label: boundLabel((source.leaves as any)?.label) },
      }
    }
    if (source.type === 'sankey') {
      changed = true
      responsive = { ...responsive, nodeGap: responsiveSankeyNodeGap(source, width, height) }
    }
    return responsive
  })
  return changed ? series : undefined
}

export function responsiveSankeyNodeGap(series: Record<string, unknown>, width: number, height: number): number {
  const authoredGap = typeof series.nodeGap === 'number' && Number.isFinite(series.nodeGap)
    ? Math.max(0, series.nodeGap) : 8
  if (!Array.isArray(series.links)) return authoredGap
  // The adapter creates separate source and target nodes in two layers.
  const sourceCount = new Set(series.links.map((link) => link.source)).size
  const targetCount = new Set(series.links.map((link) => link.target)).size
  const layerCount = Math.max(sourceCount, targetCount)
  if (layerCount <= 1) return authoredGap
  const vertical = series.orient === 'vertical'
  const extent = vertical ? width : height
  const available = Math.max(0, extent - layoutPixels(vertical ? series.left : series.top, extent)
    - layoutPixels(vertical ? series.right : series.bottom, extent))
  // Reserve node space before gaps. Otherwise ECharts computes a negative
  // scale when the authored gaps alone exceed the available cross axis.
  const maximumGap = Math.max(0, (available - layerCount) / (layerCount - 1))
  return Math.min(authoredGap, maximumGap)
}

function layoutPixels(value: unknown, extent: number): number {
  const percent = percentNumber(value)
  return percent !== undefined ? extent * percent / 100
    : typeof value === 'number' && Number.isFinite(value) ? value : 0
}

export function expandedCenteredBounds(width: number, height: number): boolean {
  return width >= 800 && height >= 400
}

export function percentNumber(value: unknown): number | undefined {
  if (typeof value !== 'string' || !/^\d+(?:\.\d+)?%$/.test(value)) return undefined
  return Number(value.slice(0, -1))
}

export function graphLabelWidth(width: number): number {
  return Math.max(0, Math.min(160, Math.floor(width * 0.2) - 2))
}

export function finiteNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

