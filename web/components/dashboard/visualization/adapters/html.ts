import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext, type RendererAdapter, type RendererContext, type RendererHandle } from '../host-controller'
import { conditionalIconGlyph, conditionalStyleColor, contrastTextColor, resolveConditionalFormat } from '../conditional-format'
import { resolveKPIWidgetLayout } from '../kpi-layout'
import type { WidgetSize } from '../layout'
import { resolveVisualizationMetadata } from '../metadata'
import { kpiSparklinePath, resolveKPIState } from './kpi'

export { kpiLayoutFeatures, resolveKPIWidgetLayout } from '../kpi-layout'

export const adapter: RendererAdapter = {
  mount(container, envelope, context) { return new HTMLHandle(container, envelope, context) },
}

class HTMLHandle implements RendererHandle {
  private envelope: VisualizationEnvelope
  private context: RendererContext
  private size: WidgetSize = { width: Number.POSITIVE_INFINITY, height: Number.POSITIVE_INFINITY }
  private selectedLayout = ''
  private fit = true

  constructor(private readonly container: HTMLElement, envelope: VisualizationEnvelope, context: RendererContext) {
    this.envelope = envelope
    this.context = context
    this.render()
  }

  update(envelope: VisualizationEnvelope, _change: number, context: RendererContext): void {
    this.envelope = envelope
    this.context = context
    this.render()
  }

  private render(): void {
    const envelope = this.envelope
    const context = this.context
    this.container.replaceChildren()
    const article = document.createElement('article')
    article.className = 'lv-kpi-card'
    const resolution = resolveKPIWidgetLayout(envelope, this.size)
    const requirement = resolution.kind === 'fit'
      ? resolution
      : resolution.requirements.at(-1)!
    this.selectedLayout = requirement.layout
    this.fit = resolution.kind === 'fit'
    this.container.dataset.layoutVariant = requirement.layout
    this.container.dataset.layoutFit = this.fit ? 'fit' : 'too-small'
    article.dataset.layout = requirement.layout
    article.dataset.layoutFit = this.container.dataset.layoutFit
    const conditional = kpiConditionalPresentation(envelope, context)
    const metadata = resolveVisualizationMetadata(envelope)
    const state = resolveKPIState(envelope, context)
    article.setAttribute('aria-label', accessibleLabel([
      metadata.title,
      metadata.summary ?? metadata.description,
      state.accessibleSummary,
      conditional.iconLabel ? `Status: ${conditional.iconLabel}.` : '',
    ]))
    if (conditional.background) article.style.backgroundColor = conditional.background
    if (conditional.foreground) article.style.color = conditional.foreground
    if (envelope.spec.kind === 'kpi') {
      article.dataset.mode = envelope.spec.presentation.mode
      const tone = state.rangeTone ?? envelope.spec.presentation.tone
      if (tone) article.dataset.tone = tone
    }
    if (state.highlightActive) article.dataset.highlighted = 'true'
    const label = document.createElement('div')
    label.className = 'lv-visualization-label'
    label.textContent = metadata.title
    label.title = metadata.title
    const subtitle = metadata.subtitle ? document.createElement('small') : undefined
    if (subtitle) {
      subtitle.className = 'lv-visualization-note'
      subtitle.textContent = metadata.subtitle!
    }
    const value = document.createElement('strong')
    value.className = 'lv-visualization-kpi'
    if (conditional.valueColor) value.style.color = conditional.valueColor
    value.textContent = [conditional.icon, state.currentText].filter(Boolean).join(' ')
    value.title = state.currentText
    article.append(label)
    if (subtitle) article.append(subtitle)
    article.append(value)
    if (state.comparisonText !== undefined || state.deltaText !== undefined) {
      const comparison = document.createElement('div')
      comparison.className = 'lv-kpi-comparison'
      if (state.comparisonText !== undefined) {
        const comparisonValue = document.createElement('span')
        comparisonValue.textContent = `${state.comparisonLabel}: ${state.comparisonText}`
        comparison.append(comparisonValue)
      }
      if (state.deltaText !== undefined) {
        const delta = document.createElement('span')
        delta.className = 'lv-kpi-delta'
        if (state.changeStatus) delta.dataset.status = state.changeStatus
        const distinctChangeStatus = state.changeStatus && state.changeStatus.toLowerCase() !== state.deltaText.toLowerCase()
          ? state.changeStatus
          : undefined
        delta.textContent = [state.deltaCue, state.deltaText, distinctChangeStatus].filter(Boolean).join(' ')
        comparison.append(delta)
      }
      article.append(comparison)
    }
    if (envelope.spec.kind === 'kpi' && (envelope.spec.presentation.mode === 'bullet' || envelope.spec.presentation.mode === 'progress')) {
      const mode = envelope.spec.presentation.mode
      const progress = document.createElement('div')
      progress.className = `lv-kpi-progress lv-kpi-progress-${mode}`
      progress.setAttribute('role', mode === 'bullet' ? 'meter' : 'progressbar')
      progress.setAttribute('aria-label', state.goalLabel ?? 'Target')
      progress.setAttribute('aria-valuetext', `${state.currentText} of ${state.goalText ?? 'unavailable'}`)
      progress.setAttribute('aria-valuemin', String(mode === 'bullet' ? state.bulletMinimum ?? 0 : 0))
      const maximum = mode === 'bullet' ? state.bulletMaximum : state.goal
      if (maximum !== undefined) progress.setAttribute('aria-valuemax', String(maximum))
      if (state.current !== undefined) progress.setAttribute('aria-valuenow', String(state.current))
      if (mode === 'bullet') {
        for (const range of state.bulletRanges) {
          const band = document.createElement('span')
          band.className = 'lv-kpi-bullet-range'
          band.dataset.tone = range.tone
          band.style.insetInlineStart = `${range.start * 100}%`
          band.style.width = `${(range.end - range.start) * 100}%`
          band.setAttribute('aria-hidden', 'true')
          progress.append(band)
        }
      }
      const fill = document.createElement('span')
      fill.className = 'lv-kpi-progress-fill'
      const fillPosition = mode === 'bullet' ? state.bulletValuePosition : state.progress
      fill.style.width = `${(fillPosition ?? 0) * 100}%`
      progress.append(fill)
      if (mode === 'bullet' && state.bulletGoalPosition !== undefined) {
        const target = document.createElement('span')
        target.className = 'lv-kpi-bullet-target'
        target.style.insetInlineStart = `${state.bulletGoalPosition * 100}%`
        target.setAttribute('aria-hidden', 'true')
        progress.append(target)
      }
      article.append(progress)
    }
    if (state.goalText !== undefined) {
      const goal = document.createElement('small')
      goal.className = 'lv-kpi-goal'
      goal.textContent = `${state.goalLabel}: ${state.goalText}`
      article.append(goal)
    }
    if (state.rangeLabel) {
      const status = document.createElement('small')
      status.className = 'lv-kpi-status'
      status.dataset.tone = state.rangeTone
      status.textContent = `Status: ${state.rangeLabel}`
      article.append(status)
    } else if (envelope.spec.kind === 'kpi' && envelope.spec.presentation.ranges.length > 0 && state.current !== undefined) {
      const status = document.createElement('small')
      status.className = 'lv-kpi-status'
      status.dataset.tone = 'warning'
      status.textContent = 'Status: Out of range'
      article.append(status)
    }
    if (state.highlightAnnouncement) {
      const highlight = document.createElement('small')
      highlight.className = 'lv-visualization-note lv-kpi-highlight'
      highlight.setAttribute('aria-live', 'polite')
      highlight.textContent = state.highlightAnnouncement
      highlight.title = state.highlightAnnouncement
      article.append(highlight)
    }
    if (state.trend.length > 0) {
      const namespace = 'http://www.w3.org/2000/svg'
      const sparkline = document.createElementNS(namespace, 'svg')
      sparkline.classList.add('lv-kpi-sparkline')
      sparkline.setAttribute('viewBox', '0 0 100 28')
      sparkline.setAttribute('preserveAspectRatio', 'none')
      sparkline.setAttribute('aria-hidden', 'true')
      const path = document.createElementNS(namespace, 'path')
      path.setAttribute('d', kpiSparklinePath(state.trend))
      sparkline.append(path)
      article.append(sparkline)
    }
    if (envelope.spec.kind === 'kpi' && envelope.spec.presentation.note) {
      const note = document.createElement('small'); note.className = 'lv-visualization-note'; note.textContent = envelope.spec.presentation.note; article.append(note)
    }
    this.container.append(article)
  }

  resize(width: number, height: number): void {
    const next = { width, height }
    const resolution = resolveKPIWidgetLayout(this.envelope, next)
    const requirement = resolution.kind === 'fit' ? resolution : resolution.requirements.at(-1)!
    const fit = resolution.kind === 'fit'
    this.size = next
    if (requirement.layout === this.selectedLayout && fit === this.fit) return
    this.render()
  }

  async snapshot(): Promise<Blob> { return new Blob([this.container.textContent ?? ''], { type: 'text/plain' }) }
  dispose(): void {
    delete this.container.dataset.layoutVariant
    delete this.container.dataset.layoutFit
    this.container.replaceChildren()
  }
}

export function kpiConditionalPresentation(envelope: VisualizationEnvelope, context: RendererContext): {
  background?: string
  foreground?: string
  valueColor?: string
  icon?: string
  iconLabel?: string
} {
  const spec = envelope.spec
  if (spec.kind !== 'kpi' || envelope.dataState.kind !== 'inline') return {}
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === spec.value.dataset)
  const row = dataset?.rows[0]
  if (!dataset || !row) return {}
  const resolveTarget = (target: 'visual_background' | 'kpi_value') => {
    const format = spec.conditionalFormatting?.find((candidate) => candidate.target === target)
    return format ? resolveConditionalFormat(format, dataset.columns, row) : undefined
  }
  const backgroundResult = resolveTarget('visual_background')
  const valueResult = resolveTarget('kpi_value')
  const background = backgroundResult ? conditionalStyleColor(backgroundResult.style, (intent) => intentColor(intent, context)) : undefined
  const authoredValueColor = valueResult ? conditionalStyleColor(valueResult.style, (intent) => intentColor(intent, context)) : undefined
  const foreground = background
    ? contrastTextColor(background, [context.colors.foreground, context.colors.surface, '#000000', '#ffffff'])
    : undefined
  const valueColor = background && authoredValueColor
    ? contrastTextColor(background, [authoredValueColor, foreground!, context.colors.surface, '#000000', '#ffffff'])
    : authoredValueColor
  const icon = valueResult?.style.icon ?? backgroundResult?.style.icon
  return {
    ...(background ? { background } : {}),
    ...(foreground ? { foreground } : {}),
    ...(valueColor ? { valueColor } : {}),
    ...(icon ? { icon: conditionalIconGlyph(icon), iconLabel: iconAccessibleLabel(icon) } : {}),
  }
}

export function kpiText(envelope: VisualizationEnvelope, context: RendererContext = defaultRendererContext): string {
  return resolveKPIState(envelope, context).currentText
}

export function accessibleLabel(parts: Array<string | undefined>): string {
  const sentences = parts
    .map((part) => part?.trim().replace(/[\s.]+$/u, ''))
    .filter((part): part is string => Boolean(part))
  return sentences.length === 0 ? '' : `${sentences.join('. ')}.`
}

function intentColor(intent: string, context: RendererContext): string {
  switch (intent) {
    case 'accent': return context.colors.accent
    case 'neutral': return context.colors.muted
    case 'ink': return context.colors.foreground
    case 'success': return context.colors.success
    case 'warning': return context.colors.attention
    case 'danger': return context.colors.danger
  }
  if (intent.startsWith('data_')) return context.colors.data[Number(intent.slice(5)) - 1] ?? context.colors.accent
  return context.colors.foreground
}

function iconAccessibleLabel(icon: string): string {
  switch (icon) {
    case 'arrow_up': return 'increasing'
    case 'arrow_down': return 'decreasing'
    case 'triangle_up': return 'higher'
    case 'triangle_down': return 'lower'
    case 'warning': return 'warning'
    default: return icon.replaceAll('_', ' ')
  }
}
