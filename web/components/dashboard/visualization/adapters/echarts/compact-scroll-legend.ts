import { format } from 'echarts'

/**
 * Give native horizontal scroll legends fixed, whole-item slots on compact
 * canvases. The returned patch intentionally omits data, formatter, selection,
 * and scroll position so resizing cannot replace native legend state.
 */
export function compactScrollLegendGeometry(source: Record<string, any> | undefined, width: number): Record<string, any> {
  if (!source || typeof source !== 'object' || Array.isArray(source) || !Number.isFinite(width) || width <= 0) return {}
  const legend = source
  if (legend.type !== 'scroll' || legend.orient !== 'horizontal' || typeof legend.formatter !== 'function') return {}
  if (!Array.isArray(legend.data) || legend.data.length === 0) return {}

  const textStyle = record(legend.textStyle)
  if (typeof textStyle.fontFamily !== 'string' || textStyle.fontFamily.length === 0) return {}

  const labels = legend.data.map((item: unknown) => {
    const formatted = legend.formatter(legendName(item))
    return typeof formatted === 'string' ? formatted : String(formatted ?? '')
  })
  const labelsByName = new Map<string, string>(legend.data.map((item: unknown, index: number) => [legendName(item), labels[index] ?? '']))
  const measuredTextWidth = labels.reduce((longest: number, label: string) => {
    const measured = format.getTextRect(label, legendFont(textStyle)).width
    return Number.isFinite(measured) ? Math.max(longest, measured) : longest
  }, 0)

  const padding = 5
  const itemGap = finite(legend.itemGap, 8)
  const itemWidth = 12
  const itemHeight = 12
  const iconTextGap = 5
  const pageButtonItemGap = finite(legend.pageButtonItemGap, 5)
  const pageButtonGap = finite(legend.pageButtonGap, 8)
  const pageIconSize = finite(legend.pageIconSize, 15)
  const pageTextStyle = Object.keys(record(legend.pageTextStyle)).length > 0 ? record(legend.pageTextStyle) : textStyle
  const pageControllerWidth = fixedPageControllerWidth(pageIconSize, pageButtonItemGap, pageTextStyle)
  const left = finite(legend.left, 8)
  const right = finite(legend.right, 8)
  const maxLegendWidth = Math.max(0, Math.floor(width - left - right))
  const reservePager = legend.data.length > 1 ? pageControllerWidth + pageButtonGap : 0
  const availableTextWidth = maxLegendWidth - reservePager - itemWidth - iconTextGap
  if (availableTextWidth < 1) return {}

  const maxTextWidth = Math.max(1, Math.min(160, Math.ceil(measuredTextWidth), Math.floor(availableTextWidth)))
  const slotWidth = itemWidth + iconTextGap + maxTextWidth
  const availableForSlots = Math.max(0, maxLegendWidth - padding * 2 - reservePager)
  const slotCount = Math.max(1, Math.floor((availableForSlots + itemGap) / (slotWidth + itemGap)))
  const legendWidth = Math.min(
    maxLegendWidth,
    slotCount * slotWidth + Math.max(0, slotCount - 1) * itemGap + reservePager,
  )

  return {
    type: 'scroll',
    orient: 'horizontal',
    width: Math.max(1, Math.ceil(legendWidth)),
    itemWidth,
    itemHeight,
    itemGap,
    pageButtonItemGap,
    pageButtonGap,
    pageIconSize,
    padding,
    textStyle: {
      ...textStyle,
      width: maxTextWidth,
      overflow: 'truncate',
      ellipsis: '…',
      backgroundColor: textStyle.backgroundColor || 'transparent',
    },
    tooltip: {
      show: true,
      formatter: (params: { name?: unknown }) => {
        const name = typeof params?.name === 'string' || typeof params?.name === 'number' ? String(params.name) : ''
        return format.encodeHTML(labelsByName.get(name) ?? name)
      },
    },
  }
}

function record(value: unknown): Record<string, any> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, any> : {}
}

function legendName(item: unknown): string {
  if (typeof item === 'string' || typeof item === 'number') return String(item)
  if (item && typeof item === 'object' && !Array.isArray(item) && 'name' in item) {
    const name = (item as { name?: unknown }).name
    return typeof name === 'string' || typeof name === 'number' ? String(name) : String(name ?? '')
  }
  return ''
}

function legendFont(textStyle: Record<string, any>): string {
  const fontStyle = typeof textStyle.fontStyle === 'string' ? textStyle.fontStyle : 'normal'
  const fontWeight = textStyle.fontWeight === undefined ? 'normal' : String(textStyle.fontWeight)
  const fontSize = Number.isFinite(textStyle.fontSize) ? `${textStyle.fontSize}px` : '12px'
  return `${fontStyle} ${fontWeight} ${fontSize} ${textStyle.fontFamily}`
}

function fixedPageControllerWidth(pageIconSize: number, pageButtonItemGap: number, pageTextStyle: Record<string, any>): number {
  const pageTextWidth = format.getTextRect('xx/xx', legendFont({ ...textStyleDefaults, ...pageTextStyle })).width
  const pageIconWidth = pageIconSize * 0.6
  return Math.ceil(pageIconWidth * 2 + pageButtonItemGap * 2 + pageTextWidth)
}

const textStyleDefaults = { fontStyle: 'normal', fontWeight: 'normal', fontSize: 12, fontFamily: 'sans-serif' }

function finite(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : fallback
}
