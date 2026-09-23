import type { VisualizationConditionalFormat, VisualizationEnvelope, VisualizationFieldRef } from '../../../generated/visualization'

export const CHART_CUE_FONT_FAMILY = 'LeapView Chart Cues'
export const CHART_CUE_GLYPHS = '●■◆▲▼↑↓⚠'
export const CHART_CUE_FONT_LOAD_TIMEOUT_MS = 5_000
const CHART_LABEL_FONT_SAMPLE = 'Chart Value 0123'
const CHART_LABEL_FONT_WEIGHTS = [400, 500, 600] as const

type FontFaceSetLike = {
  check(font: string, text?: string): boolean
  load(font: string, text?: string): Promise<readonly unknown[]>
}

export type ChartCueFontReadiness = 'ready' | 'unavailable'

const pendingCueLoads = new WeakMap<object, Promise<ChartCueFontReadiness>>()
const pendingBaseLoads = new WeakMap<object, Map<string, Promise<ChartCueFontReadiness>>>()

export function chartCueFontStack(fallback: string): string {
  const base = fallback.trim() || 'system-ui'
  return `'${CHART_CUE_FONT_FAMILY}', ${base}`
}

/** Load the cue and label faces before ECharts measures a cue-bearing canvas label. */
export async function requireChartCueAndBaseFonts(
  fontFamily: string,
  fonts: FontFaceSetLike | undefined = browserFontFaceSet(),
  timeoutMs = CHART_CUE_FONT_LOAD_TIMEOUT_MS,
): Promise<void> {
  const [cue, base] = await Promise.all([
    waitForChartCueFont(fonts, timeoutMs),
    waitForChartBaseFont(fonts, fontFamily, timeoutMs),
  ])
  if (cue !== 'ready') throw new Error('chart cue font failed to load')
  if (base !== 'ready') throw new Error('chart base font failed to load')
}

export function waitForChartCueFont(
  fonts: FontFaceSetLike | undefined = browserFontFaceSet(),
  timeoutMs = CHART_CUE_FONT_LOAD_TIMEOUT_MS,
): Promise<ChartCueFontReadiness> {
  if (!fonts) return Promise.resolve('unavailable')
  const existing = pendingCueLoads.get(fonts)
  if (existing) return existing
  let pending: Promise<ChartCueFontReadiness>
  pending = boundedFontLoad(
    () => fonts.load(`400 12px '${CHART_CUE_FONT_FAMILY}'`, CHART_CUE_GLYPHS),
    () => fonts.check(`400 12px '${CHART_CUE_FONT_FAMILY}'`, CHART_CUE_GLYPHS),
    timeoutMs,
    (faces) => faces.length > 0,
  ).then((result) => {
    if (result !== 'ready' && pendingCueLoads.get(fonts) === pending) pendingCueLoads.delete(fonts)
    return result
  })
  pendingCueLoads.set(fonts, pending)
  return pending
}

export function waitForChartBaseFont(
  fonts: FontFaceSetLike | undefined,
  fontFamily: string,
  timeoutMs = CHART_CUE_FONT_LOAD_TIMEOUT_MS,
): Promise<ChartCueFontReadiness> {
  if (!fonts) return Promise.resolve('unavailable')
  const family = fontFamily.trim() || 'system-ui'
  const loads = pendingBaseLoads.get(fonts) ?? new Map<string, Promise<ChartCueFontReadiness>>()
  pendingBaseLoads.set(fonts, loads)
  const existing = loads.get(family)
  if (existing) return existing
  let pending: Promise<ChartCueFontReadiness>
  pending = boundedFontLoad(
    () => Promise.all(CHART_LABEL_FONT_WEIGHTS.map((weight) => fonts.load(`${weight} 12px ${family}`, CHART_LABEL_FONT_SAMPLE))),
    () => CHART_LABEL_FONT_WEIGHTS.every((weight) => fonts.check(`${weight} 12px ${family}`, CHART_LABEL_FONT_SAMPLE)),
    timeoutMs,
  ).then((result) => {
    if (result !== 'ready' && loads.get(family) === pending) loads.delete(family)
    return result
  })
  loads.set(family, pending)
  return pending
}

export function proportionalCueFontNeeded(envelope: VisualizationEnvelope): boolean {
  const spec = envelope.spec
  return spec.kind === 'proportional' && proportionalConditionalCueFormat(envelope, spec.value) !== undefined
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

async function boundedFontLoad<T>(
  load: () => Promise<T>,
  check: () => boolean,
  timeoutMs: number,
  validateLoad: (result: T) => boolean = () => true,
): Promise<ChartCueFontReadiness> {
  const loading = Promise.resolve()
    .then(load)
    .then((result) => validateLoad(result) && check() ? 'ready' as const : 'unavailable' as const)
    .catch(() => 'unavailable' as const)
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<ChartCueFontReadiness>((resolve) => {
    timer = setTimeout(() => resolve('unavailable'), timeoutMs)
  })
  try {
    return await Promise.race([loading, timeout])
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
