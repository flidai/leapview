import { LitElement, css, html } from 'lit'
import { property } from 'lit/decorators.js'
import type { ChatArtifactSignal, ChatSignal, SavedVisualLibrarySignal } from '../../generated/signals'
import type { DashboardChatComponent } from './dashboard-preview-contract'
import { submitVisualForm, type SavedVisualLibraryMessage } from './visual-library-bridge'

export type VisualLibraryState = { savedIds: string[]; totalSaved?: number; libraryIds?: Record<string, string>; dashboardArtifacts?: ChatArtifactSignal[]; savingId: string; error: string }
export type DashboardVisualSource = { dashboardId: string; revisionId: string; pageId: string; components: DashboardChatComponent[]; artifacts: ChatArtifactSignal[] }

// Native forms keep saving inside the authenticated, CSRF-protected page flow.
// Both chat surfaces share this controller and the account's server-side library.
class AgentVisualLibrary extends LitElement {
  @property({ attribute: false }) agent?: ChatSignal
  @property({ attribute: false }) dashboardSource?: DashboardVisualSource
  private readonly frameName = `visual-library-${crypto.randomUUID()}`
  private library?: SavedVisualLibrarySignal
  private pending: { artifactId: string; add: boolean; remove?: boolean } | null = null
  private eventHost?: EventTarget
  private timer = 0
  private agentKey = ''
  private libraryRequested = false

  static styles = css`:host { display: none; }`

  connectedCallback(): void {
    super.connectedCallback()
    const root = this.getRootNode()
    this.eventHost = root instanceof ShadowRoot ? root.host : this
    this.eventHost.addEventListener('lv-save-agent-visual', this.save as EventListener)
    window.addEventListener('message', this.message)
  }

  disconnectedCallback(): void {
    this.eventHost?.removeEventListener('lv-save-agent-visual', this.save as EventListener)
    window.removeEventListener('message', this.message)
    window.clearTimeout(this.timer)
    super.disconnectedCallback()
  }

  render() {
    // Discover the account library even in conversations with no artifacts.
    // Retain the frame afterward so pending saves keep their target.
    this.libraryRequested ||= Boolean(this.agent?.activeConversationId || this.agent?.transcript?.some(item => item.artifact) || this.dashboardSource?.dashboardId)
    if (!this.libraryRequested) return null
    return html`<iframe name=${this.frameName} title="Saved visual library" src="/visuals/saved" @load=${this.loaded}></iframe>`
  }

  updated(): void {
    const key = JSON.stringify([this.agent?.activeConversationId, this.artifacts.map(item => item.id)])
    if (key === this.agentKey) return
    this.agentKey = key
    this.emitState()
  }

  private sourceKey(id: string): string {
    return `${this.agent?.activeConversationId ?? 'chat'}/${id}`
  }

  private get artifacts(): ChatArtifactSignal[] {
    return [...(this.agent?.transcript ?? []).flatMap(item => item.artifact ? [item.artifact] : []), ...(this.dashboardSource?.artifacts ?? []), ...this.dashboardArtifacts]
  }

  private get dashboardArtifacts(): ChatArtifactSignal[] {
    const source = this.dashboardSource
    const conversationId = this.agent?.activeConversationId
    if (!conversationId || !source?.dashboardId || !source.pageId) return []
    const prefix = `${conversationId}/dashboard:${source.dashboardId}:${source.pageId}:`
    return (this.library?.visuals ?? []).filter(visual => visual.sourceKey.startsWith(prefix) && visual.sourceKey.length > prefix.length)
      .map(visual => ({id: visual.sourceKey.slice(conversationId.length + 1), type: 'visual', summary: visual.title}))
  }

  private emitState(error = ''): void {
    const savedIds = [...new Set(this.artifacts.filter(item => this.library?.visuals.some(visual => visual.sourceKey === this.sourceKey(item.id))).map(item => item.id))]
    const libraryIds = Object.fromEntries(savedIds.map(id => [id, this.library!.visuals.find(visual => visual.sourceKey === this.sourceKey(id))!.id]))
    this.dispatchEvent(new CustomEvent<VisualLibraryState>('lv-visual-library-state', {
      bubbles: true, composed: true, detail: { savedIds, totalSaved: this.library?.visuals.length ?? 0, libraryIds, dashboardArtifacts: this.dashboardArtifacts, savingId: this.pending?.artifactId ?? '', error },
    }))
  }

  private save = (event: CustomEvent<{ artifactId: string; add: boolean; retain?: boolean }>): void => {
    if (this.pending) return
    const { artifactId, add } = event.detail
    const item = [...(this.agent?.transcript ?? [])].reverse().find(item => item.artifact?.id === artifactId && item.status === 'complete')
    const artifact = item?.artifact ?? this.dashboardSource?.artifacts.find(item => item.id === artifactId)
    if (!artifact) return
    const saved = this.library?.visuals.find(visual => visual.sourceKey === this.sourceKey(artifactId))
    if (saved && add) {
      this.addToDashboard(saved.id, artifactId)
      return
    }
    try {
      if (saved && !event.detail.retain) {
        this.pending = { artifactId, add: false, remove: true }
        this.emitState()
        submitVisualForm('/visuals/saved/remove', this.shadowRoot?.querySelector('iframe') ?? null, { savedVisualId: saved.id })
      } else {
        let fields: Record<string, string>
        if (item?.artifact) {
          const definition = JSON.parse(item.argumentsJson || item.inputJson || '')
          if (!definition.visual || !definition.semanticModelId) throw new Error('This visual has no editable definition. Ask the agent to recreate it.')
          fields = { definition: JSON.stringify(definition), title: definition.visual.title || artifact.summary || 'Saved visual' }
        } else {
          const source = this.dashboardSource
          const component = source?.components.find(component => component.artifactId === artifactId && component.pageId === source.pageId)
          if (!source || !component) throw new Error('Open this visual on its dashboard page before saving it.')
          fields = { dashboardId: source.dashboardId, revisionId: source.revisionId, pageId: component.pageId, componentId: component.id, title: artifact.summary || 'Saved visual' }
        }
        this.pending = { artifactId, add }
        this.emitState()
        submitVisualForm('/visuals/saved', this.shadowRoot?.querySelector('iframe') ?? null, {
          ...fields, sourceKey: this.sourceKey(artifactId),
        })
      }
      window.clearTimeout(this.timer)
      this.timer = window.setTimeout(() => {
        this.pending = null
        this.emitState('Updating the library took too long. Please try again.')
      }, 20000)
    } catch (error) {
      this.pending = null
      this.emitState(error instanceof Error ? error.message : 'Could not save this visual.')
    }
  }

  private loaded = (): void => {
    const frame = this.shadowRoot?.querySelector('iframe')
    if (!this.pending || !frame?.contentDocument || frame.contentDocument.querySelector('lv-saved-visual-library')) return
    this.pending = null
    window.clearTimeout(this.timer)
    this.emitState('Could not save this visual. Please try again.')
  }

  private addToDashboard(savedId: string, artifactId: string): void {
    const consumed = !this.dispatchEvent(new CustomEvent('lv-add-agent-visual', {
      bubbles: true, composed: true, cancelable: true, detail: { savedId, artifactId },
    }))
    if (!consumed) submitVisualForm('/dashboards/new', '_self', {
      savedVisualId: savedId, title: this.library?.visuals.find(visual => visual.id === savedId)?.title ?? 'Chat dashboard',
    })
  }

  private message = (event: MessageEvent<SavedVisualLibraryMessage>): void => {
    if (event.origin !== window.location.origin || event.source !== this.shadowRoot?.querySelector('iframe')?.contentWindow || event.data?.type !== 'lv-saved-visual-library') return
    this.library = event.data.library
    const pending = this.pending
    if (this.library.error) {
      this.pending = null
      window.clearTimeout(this.timer)
      this.emitState(this.library.error)
      return
    }
    const removed = pending?.remove && !this.library.visuals.some(visual => visual.sourceKey === this.sourceKey(pending.artifactId))
    if (pending && (removed || (this.library.sourceKey === this.sourceKey(pending.artifactId) && this.library.savedId))) {
      this.pending = null
      window.clearTimeout(this.timer)
      this.emitState()
      this.dispatchEvent(new CustomEvent('lv-saved-visuals-changed', { bubbles: true, composed: true }))
      if (pending.add) this.addToDashboard(this.library.savedId, pending.artifactId)
    } else {
      this.emitState()
    }
  }
}
customElements.define('lv-agent-visual-library', AgentVisualLibrary)
