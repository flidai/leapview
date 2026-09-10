import { format } from 'echarts'

/**
 * The generated proportional legend is a horizontal scroll legend.  Its
 * content is native ECharts content, so this helper deliberately returns only
 * layout fields.  In particular, it must not replace data, formatter, or
 * native selection/page state while a chart is being resized.
 */
export function proportionalLegendGeometry(source: Record<string, any> | undefined, width: number): Record<string, any> {
  if (!source || typeof source !== 'object' || Array.isArray(source) || !Number.isFinite(width) || width <= 0) return {}
  const legend = source
  if (legend.type !== 'scroll' || legend.orient !== 'horizontal' || typeof legend.formatter !== 'function') return {}
  if (!Array.isArray(legend.data) || legend.data.length === 0) return {}

  const textStyle = legend.textStyle && typeof legend.textStyle === 'object' && !Array.isArray(legend.textStyle)
    ? legend.textStyle
    : {}
  if (typeof textStyle.fontFamily !== 'string' || textStyle.fontFamily.length === 0) return {}

  const font = legendFont(textStyle)
  const labels = legend.data.map((item: unknown) => {
    const name = legendName(item)
    const formatted = legend.formatter(name)
    return typeof formatted === 'string' ? formatted : String(formatted ?? '')
  })
  const labelsByName = new Map<string, string>(legend.data.map((item: unknown, index: number) => [legendName(item), labels[index] ?? '']))
  const measuredTextWidth = labels.reduce((longest: number, label: string) => {
    const width = format.getTextRect(label, font).width
    return Number.isFinite(width) ? Math.max(longest, width) : longest
  }, 0)

  const padding = 5
  const itemGap = finite(legend.itemGap, 8)
  const itemWidth = 12
  const itemHeight = 12
  const iconTextGap = 5
  const pageButtonItemGap = finite(legend.pageButtonItemGap, 5)
  const pageButtonGap = finite(legend.pageButtonGap, 8)
  const pageIconSize = finite(legend.pageIconSize, 15)
  const pageTextStyle = legend.pageTextStyle && typeof legend.pageTextStyle === 'object' && !Array.isArray(legend.pageTextStyle)
    ? legend.pageTextStyle
    : textStyle
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
  // ECharts treats an explicit legend.width as the content/pager max size;
  // padding is applied around that box by the component view.  Keep it in the
  // patch but do not add it to the max size, otherwise the clip window gains a
  // partial next item on every page.
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
      // ZRender only exposes a plain text's measured glyph bounds to the
      // legend item group.  A transparent background makes its fixed width
      // participate in that native bound as well, giving every item one
      // equal slot even when its label is short.
      backgroundColor: textStyle.backgroundColor || 'transparent',
    },
    // This keeps the native legend hover affordance enabled.  The canonical
    // item name remains untouched so ECharts can show the full name when the
    // source legend supplies its native item tooltip configuration.
    tooltip: {
      show: true,
      formatter: (params: { name?: unknown }) => {
        const name = typeof params?.name === 'string' || typeof params?.name === 'number' ? String(params.name) : ''
        // Tooltip formatter output is interpreted as HTML by ECharts.  Keep
        // authored labels intact while preventing category text from becoming
        // markup when a source name contains angle brackets or ampersands.
        return format.encodeHTML(labelsByName.get(name) ?? name)
      },
    },
  }
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
  // ScrollableLegendView's page text is the native `{current}/{total}` label.
  // Its icon path occupies 60% of the configured icon box.  Measuring the
  // placeholder with the same ECharts text helper mirrors the view's
  // controller bounding rect without depending on the current page number.
  const pageFont = legendFont({ ...textStyleDefaults, ...pageTextStyle })
  const pageTextWidth = format.getTextRect('xx/xx', pageFont).width
  const pageIconWidth = pageIconSize * 0.6
  return Math.ceil(pageIconWidth * 2 + pageButtonItemGap * 2 + pageTextWidth)
}

const textStyleDefaults = { fontStyle: 'normal', fontWeight: 'normal', fontSize: 12, fontFamily: 'sans-serif' }

function finite(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : fallback
}
