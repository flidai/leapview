import { format } from 'echarts'

const COMPACT_OUTSIDE_LABEL_FRACTION = 0.4
const minimumCanvasDimensionCache = new WeakMap<object, number>()

export function proportionalOutsideLabelsFitCompactCanvas(seriesValue: unknown, datasetValue: unknown, width: number, height: number): boolean {
  const cacheKey = (Array.isArray(seriesValue) || isRecord(seriesValue)) ? seriesValue as object : undefined
  let minimumCanvasDimension = cacheKey ? minimumCanvasDimensionCache.get(cacheKey) : undefined
  if (minimumCanvasDimension === undefined) {
    minimumCanvasDimension = minimumCanvasDimensionForVisibleProportionalLabels(seriesValue, datasetValue)
    if (cacheKey) minimumCanvasDimensionCache.set(cacheKey, minimumCanvasDimension)
  }
  return minimumCanvasDimension === 0 || minimumCanvasDimension <= Math.min(width, height)
}

function minimumCanvasDimensionForVisibleProportionalLabels(seriesValue: unknown, datasetValue: unknown): number {
  const series = Array.isArray(seriesValue) ? seriesValue : [seriesValue]
  let hasOutsideLabels = false
  let hasMeasuredLabels = false
  let maximumLabelWidth = 0
  let totalLabelWidth = 0
  for (const entry of series) {
    if (!isRecord(entry) || entry.type !== 'pie' || !isRecord(entry.label)) continue
    const label = entry.label
    if (label.show === false || label.position !== 'outside') continue
    hasOutsideLabels = true
    if (typeof label.formatter !== 'function') return Number.POSITIVE_INFINITY
    const rows = proportionalLabelRows(entry, datasetValue)
    if (!rows) return Number.POSITIVE_INFINITY
    const valueIndex = rows.header?.findIndex((column) => column === encodedDimension(entry.encode?.value)) ?? -1
    const values = valueIndex < 0 ? [] : rows.rows.map((row) => numericValue(readDimension(row, rows.header?.[valueIndex], valueIndex)))
    const total = values.reduce<number>((sum, value) => sum + (value !== undefined && value > 0 ? value : 0), 0)
    const minimumAngle = finiteNumber(entry.minShowLabelAngle) ?? 0
    const font = labelFont(label)
    for (const [dataIndex, value] of rows.rows.entries()) {
      const numeric = values[dataIndex]
      if (total > 0 && (numeric === undefined || numeric <= 0 || numeric / total * 360 < minimumAngle)) continue
      const text = String(label.formatter({ value, dataIndex }) ?? '')
      if (!text) continue
      hasMeasuredLabels = true
      const labelWidth = format.getTextRect(text, font).width
      maximumLabelWidth = Math.max(maximumLabelWidth, labelWidth)
      totalLabelWidth += labelWidth
    }
  }
  if (!hasOutsideLabels || !hasMeasuredLabels) return 0
  // Each label gets 40% of the limiting dimension; the total budget leaves 20% for the ring and legend gap.
  return Math.max(maximumLabelWidth / COMPACT_OUTSIDE_LABEL_FRACTION, totalLabelWidth / (COMPACT_OUTSIDE_LABEL_FRACTION * 2))
}

function proportionalLabelRows(series: Record<string, any>, datasetValue: unknown): { rows: unknown[]; header?: unknown[] } | undefined {
  const datasets = Array.isArray(datasetValue) ? datasetValue : [datasetValue]
  const dataset = typeof series.datasetId === 'string'
    ? datasets.find((candidate) => isRecord(candidate) && candidate.id === series.datasetId)
    : datasets[finiteNumber(series.datasetIndex) ?? 0]
  const source = isRecord(dataset) ? dataset.source : undefined
  if (!Array.isArray(source)) return undefined
  const first = source[0]
  const header = Array.isArray(first) ? first : undefined
  const encoded = [series.encode?.itemName, series.encode?.value].flatMap((dimension) =>
    Array.isArray(dimension) ? dimension : dimension === undefined ? [] : [dimension],
  )
  const hasHeader = header !== undefined && encoded.some((dimension) => header.includes(dimension))
  return { rows: hasHeader ? source.slice(1) : source, ...(hasHeader ? { header } : {}) }
}

function encodedDimension(value: unknown): unknown {
  return Array.isArray(value) ? value[0] : value
}

function readDimension(row: unknown, field: unknown, index: number): unknown {
  if (Array.isArray(row)) return row[index]
  if (isRecord(row)) return row[String(field)] ?? row.value
  return undefined
}

function numericValue(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim() !== '' && Number.isFinite(Number(value))) return Number(value)
  return undefined
}

function labelFont(label: Record<string, any>): string {
  const fontStyle = typeof label.fontStyle === 'string' ? label.fontStyle : 'normal'
  const fontWeight = label.fontWeight === undefined ? 'normal' : String(label.fontWeight)
  const fontSize = finiteNumber(label.fontSize) ?? 12
  const fontFamily = typeof label.fontFamily === 'string' ? label.fontFamily : 'sans-serif'
  return `${fontStyle} ${fontWeight} ${fontSize}px ${fontFamily}`
}

function isRecord(value: unknown): value is Record<string, any> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value))
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
