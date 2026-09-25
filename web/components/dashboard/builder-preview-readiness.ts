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

export function isBuilderVisualTypeSwitchPending(
  commandPending: boolean,
  action: string,
  pending: { pageID: string; visualID: string; toType: string } | null,
  pageID: string,
  visual: Pick<DashboardBuilderVisualSignal, 'id' | 'type'>,
  renderedType: string,
): boolean {
  if (!commandPending || renderedType === visual.type.toLowerCase()) return false
  if (action === 'restore_revision') return true
  return action === 'set_visual_type'
    && pending?.pageID === pageID
    && pending.visualID === visual.id
    && pending.toType === renderedType
}
