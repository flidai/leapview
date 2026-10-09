import type { AgentReferenceSignal, AgentContextSignal, ChatArtifactSignal, DashboardBuilderEnvelope } from '../../generated/signals'
import type { VisualizationEnvelope, VisualizationWindowRequest } from '../../generated/visualization'

export type SavedVisualImportMessage = {
  type: 'lv-builder-imported'
  envelope: DashboardBuilderEnvelope
  agentContext: AgentContextSignal
}

export type DashboardChatComponent = { id: string; pageId: string; artifactId?: string; savedVisualId?: string }

// Same-origin bridge between the existing builder and its owning chat.
// The parent validates both origin and the exact iframe window before use.
export type ChatDashboardMessage =
  | { type: 'lv-dashboard-mutation'; href: string; revisionId: string; pageId: string; reference: AgentReferenceSignal; components: DashboardChatComponent[] }
  | { type: 'lv-builder-back-to-chat' }
  | { type: 'lv-refresh-builder' }
  | { type: 'lv-builder-visual-window'; pageId: string; request: VisualizationWindowRequest }
  | { type: 'lv-select-dashboard-page'; pageId: string }
  | { type: 'lv-arrange-dashboard-visuals'; reflow?: boolean }
  | { type: 'lv-builder-operation-error'; message: string }
  | {
    type: 'lv-builder-saved'
    revisionId: string
    canArrange?: boolean
    updating?: boolean
    fixingVisuals?: boolean
    fixMessage?: string
    pageId: string
    pages?: Array<{id: string; title: string}>
    pageTitle?: string
    modelId?: string
    href: string
    reference: AgentReferenceSignal
    components: DashboardChatComponent[]
    artifacts: ChatArtifactSignal[]
    visuals: Record<string, VisualizationEnvelope>
  }
