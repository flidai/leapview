import type { DashboardBuilderVisualSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'

// Picker limits describe the default shape for a newly created visual. A
// compiler-produced preview can represent richer authored queries (series,
// point encodings, or map layers) that do not fit those default field wells.
export function hasCompiledBuilderPreview(
  visual: Pick<DashboardBuilderVisualSignal, 'type' | 'previewError'>,
  renderedType: string,
  active: boolean,
  preview: VisualizationEnvelope | undefined,
): boolean {
  if (!active || visual.previewError || renderedType !== visual.type.toLowerCase() || !preview) return false
  const kind = renderedType === 'map' ? 'geographic'
    : renderedType === 'scatter' ? 'point'
      : ['line', 'area', 'bar', 'column', 'combo'].includes(renderedType) ? 'cartesian' : undefined
  return kind !== undefined && preview.spec.kind === kind
}
