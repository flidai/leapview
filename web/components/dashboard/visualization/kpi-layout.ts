import type { VisualizationEnvelope } from '../../../generated/visualization'
import {
  resolveWidgetLayout,
  type WidgetLayoutFeature,
  type WidgetLayoutResolution,
  type WidgetSize,
} from './layout'
import { resolveVisualizationMetadata } from './metadata'

export function kpiLayoutFeatures(envelope: VisualizationEnvelope): WidgetLayoutFeature[] {
  if (envelope.spec.kind !== 'kpi') return []
  const metadata = resolveVisualizationMetadata(envelope)
  const presentation = envelope.spec.presentation
  return [
    ...(metadata.subtitle ? ['subtitle' as const] : []),
    ...(envelope.spec.comparison ? ['comparison' as const] : []),
    ...(presentation.mode === 'bullet' || presentation.mode === 'progress' ? ['progress' as const] : []),
    ...(envelope.spec.goal ? ['goal' as const] : []),
    ...(presentation.ranges.length > 0 ? ['status' as const] : []),
    ...(envelope.spec.trend ? ['trend' as const] : []),
    ...(presentation.note ? ['note' as const] : []),
  ]
}

export function resolveKPIWidgetLayout(envelope: VisualizationEnvelope, size: WidgetSize): WidgetLayoutResolution {
  return resolveWidgetLayout('kpi', size, kpiLayoutFeatures(envelope))
}
