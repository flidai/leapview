import type { VisualizationConditionalFormat, VisualizationEnvelope, VisualizationFieldRef } from '../../../generated/visualization'

export const CHART_CUE_FONT_FAMILY = 'LeapView Chart Cues'
export const CHART_CUE_GLYPHS = '●■◆▲▼↑↓⚠'
export const CHART_CUE_FONT_LOAD_TIMEOUT_MS = 5_000

type FontFaceSetLike = {
  check(font: string, text?: string): boolean
  load(font: string, text?: string): Promise<readonly unknown[]>
}

export type ChartCueFontReadiness = 'ready' | 'unavailable'

const pendingLoads = new WeakMap<object, Promise<ChartCueFontReadiness>>()

export function chartCueFontStack(fallback: string): string {
  const base = fallback.trim() || 'system-ui'
  return `'${CHART_CUE_FONT_FAMILY}', ${base}`
}

/**
 * Load the chart cue face before a canvas renderer measures a cue-bearing
 * label. A missing face, rejected load, or slow browser font pipeline is a
 * bounded failure rather than allowing a fallback-metric canvas draw.
 */
export function waitForChartCueFont(
  fonts: FontFaceSetLike | undefined = browserFontFaceSet(),
  timeoutMs = CHART_CUE_FONT_LOAD_TIMEOUT_MS,
): Promise<ChartCueFontReadiness> {
  if (!fonts) return Promise.resolve('unavailable')
  const existing = pendingLoads.get(fonts)
  if (existing) return existing
  let pending: Promise<ChartCueFontReadiness>
  pending = loadChartCueFont(fonts, timeoutMs).then((result) => {
    if (result !== 'ready' && pendingLoads.get(fonts) === pending) pendingLoads.delete(fonts)
    return result
  })
  pendingLoads.set(fonts, pending)
  return pending
}

export async function requireChartCueFont(
  fonts: FontFaceSetLike | undefined = browserFontFaceSet(),
  timeoutMs = CHART_CUE_FONT_LOAD_TIMEOUT_MS,
): Promise<void> {
  if (await waitForChartCueFont(fonts, timeoutMs) !== 'ready') {
    throw new Error('chart cue font failed to load')
  }
}

/** Only pie and donut labels can carry the conditional cue typography. */
export function proportionalCueFontNeeded(envelope: VisualizationEnvelope): boolean {
  const spec = envelope.spec
  if (spec.kind !== 'proportional' || (spec.mark !== 'pie' && spec.mark !== 'donut')) return false
  return proportionalConditionalCueFormat(envelope, spec.value) !== undefined
}

export function proportionalConditionalCueFormat(
  envelope: VisualizationEnvelope,
  value: VisualizationFieldRef,
): VisualizationConditionalFormat | undefined {
  const formats = envelope.spec.conditionalFormatting ?? []
  return formats.find((format) =>
    format.target === 'mark_fill'
    && format.field.dataset === value.dataset
    && format.field.field === value.field
    && conditionalRuleHasIcon(format))
    ?? formats.find((format) =>
      format.target === 'series_color'
      && format.field.dataset === value.dataset
      && format.field.field === value.field
      && conditionalRuleHasIcon(format))
}

async function loadChartCueFont(fonts: FontFaceSetLike, timeoutMs: number): Promise<ChartCueFontReadiness> {
  const load = Promise.resolve()
    .then(() => fonts.load(`400 12px '${CHART_CUE_FONT_FAMILY}'`, CHART_CUE_GLYPHS))
    .then((faces) => faces.length > 0 && fonts.check(`400 12px '${CHART_CUE_FONT_FAMILY}'`, CHART_CUE_GLYPHS) ? 'ready' as const : 'unavailable' as const)
    .catch(() => 'unavailable' as const)
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<ChartCueFontReadiness>((resolve) => {
    timer = setTimeout(() => resolve('unavailable'), timeoutMs)
  })
  try {
    return await Promise.race([load, timeout])
  } finally {
    if (timer !== undefined) clearTimeout(timer)
  }
}

function browserFontFaceSet(): FontFaceSetLike | undefined {
  if (typeof document === 'undefined' || !document.fonts) return undefined
  return document.fonts
}

function conditionalRuleHasIcon(format: VisualizationConditionalFormat): boolean {
  const rule = format.rule
  if (rule.nullStyle.icon) return true
  if (rule.kind === 'gradient') return Boolean(rule.low.icon || rule.high.icon)
  if (rule.defaultStyle.icon) return true
  if (rule.kind === 'rules') return rule.rules.some((candidate) => Boolean(candidate.style.icon))
  return Object.values(rule.values).some((style) => Boolean(style.icon))
}
