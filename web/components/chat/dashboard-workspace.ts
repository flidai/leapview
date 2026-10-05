import type { AgentReferenceSignal, AgentContextSignal, ChatArtifactSignal, DashboardBuilderEnvelope } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'

export type SavedVisualImportMessage = {
  type: 'lv-builder-imported'
  envelope: DashboardBuilderEnvelope
  agentContext: AgentContextSignal
}

// Same-origin bridge between the existing builder and its owning chat.
// The parent validates both origin and the exact iframe window before use.
export type ChatDashboardMessage =
  | { type: 'lv-dashboard-mutation'; href: string; revisionId: string; pageId: string; reference: AgentReferenceSignal; components: {id: string; pageId: string}[] }
  | { type: 'lv-builder-back-to-chat' }
  | { type: 'lv-refresh-builder' }
  | { type: 'lv-arrange-dashboard-visuals' }
  | { type: 'lv-builder-operation-error'; message: string }
  | {
    type: 'lv-builder-saved'
    revisionId: string
    canArrange?: boolean
    fixingVisuals?: boolean
    fixMessage?: string
    pageId: string
    href: string
    reference: AgentReferenceSignal
    components: { id: string; pageId: string }[]
    artifacts: ChatArtifactSignal[]
    visuals: Record<string, VisualizationEnvelope>
  }
