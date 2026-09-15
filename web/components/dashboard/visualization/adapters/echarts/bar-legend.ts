import type { VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import { inlineDataset, type EChartsTranslation } from './common'

// A per-row fill callback does not set ECharts' series-level legend color.
// Use the rendered bar colors, including a split swatch for mixed outcomes.
export function applyBarLegendColors(
  decoration: EChartsTranslation,
  envelope: VisualizationEnvelope,
  series: readonly EChartsTranslation[],
  values: readonly VisualizationFieldRef[],
): EChartsTranslation {
  if (!decoration.legend) return decoration
  const colors = new Map<string, unknown>()
  series.forEach((item, index) => {
    const fill = item.itemStyle?.color
    const ref = values[index]
    if (item.type !== 'bar' || typeof fill !== 'function' || !ref) return
    const rows = inlineDataset(envelope, ref.dataset)?.rows ?? []
    const palette = [...new Set(rows.map((value) => fill({ value })).filter((color): color is string => typeof color === 'string'))]
    if (palette.length === 0) return
    colors.set(item.name, palette.length === 1 ? palette[0] : {
      type: 'linear', x: 0, y: 0, x2: 1, y2: 0,
      colorStops: palette.flatMap((color, slot) => [
        { offset: slot / palette.length, color },
        { offset: (slot + 1) / palette.length, color },
      ]),
    })
  })
  if (colors.size > 0) {
    const entries = decoration.legend.data ?? series.map((item) => ({ name: item.name }))
    decoration.legend.data = entries.map((entry: string | EChartsTranslation) => {
      const item = typeof entry === 'string' ? { name: entry } : entry
      return colors.has(item.name) ? { ...item, itemStyle: { ...item.itemStyle, color: colors.get(item.name) } } : item
    })
  }
  return decoration
}
