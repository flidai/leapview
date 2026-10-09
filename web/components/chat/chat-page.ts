import { setChatPreviewLocation, clearChatDashboardLocation, rememberChatDashboardLocation } from './dashboard-preview-location'
import { generatedDashboardHref } from './generated-dashboard'
import { loadDatastarRuntime } from '../shared/datastar-runtime'
import { savedVisualComponentId, savedVisualSourceId } from './dashboard-membership'
import { submitVisualForm } from './visual-library-bridge'
import './agent-visual-library'
import type { DashboardVisualSource, VisualLibraryState } from './agent-visual-library'
import { LitElement, css, html } from 'lit'
import { visualizationRegistry } from '../dashboard/visualization/registry'
import { repeat } from 'lit/directives/repeat.js'
import { ifDefined } from 'lit/directives/if-defined.js'
import { chatPageStyles } from './chat-page.styles'
import { chatPagePreviewStyles } from './chat-page-preview.styles'
import { state } from 'lit/decorators.js'
import { Check, CircleHelp, LayoutDashboard, Maximize2, Minimize2, Minus, Plus, Save, TrendingUp, X, type IconNode } from 'lucide'
import type { ChatArtifactSignal, AgentContextSignal, AgentReferenceSearchSignal, AgentReferenceSignal, ChatConversationSummary, ChatPageSignal, ChatSignal, ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope, VisualizationWindowRequest } from '../../generated/visualization'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { lucideIcon } from '../shared/lucide-icons'
import { uuidv7 } from '../shared/command-identity'
import '../dashboard/visual-modal'
import './chat-thread'
import { agentIcon } from './agent-icon'
import { type ChatReferencesChangeDetail, defaultAgentReferenceLimit, latestAcceptedRunId, mergeReferences, normalizeReferenceLimit } from './reference'
import './chat-composer'
import './chat-list'
import type { ChatDashboardMessage, DashboardChatComponent } from './dashboard-preview-contract'
import { chatVisualsFromSignals } from './visual-signals'
import './chat-visual-panel'
import type { ChatVisualPanel } from './chat-visual-panel'
import { saveChatVisual } from './saved-visuals'
import './chat-dashboard-picker'
import type { ChatDashboardResult } from './chat-dashboard-api'

const emptyAgent: ChatSignal = {
  conversations: [],
  activeConversationId: '',
  transcript: [],
  status: { enabled: false, running: false },
  composer: { value: '', disabled: true, placeholder: 'Agent is not configured.' },
}

const promptStarters: Array<{ label: string; prompt: string; icon: IconNode }> = [
  { label: 'Build a dashboard', prompt: 'Build a complete dashboard from my selected data source, with key metrics, trends, comparisons, and useful filters. Arrange it clearly and open the preview.', icon: LayoutDashboard },
  { label: 'Spot a change', prompt: 'What changed most in the last 30 days?', icon: TrendingUp },
  { label: 'Explain a metric', prompt: 'Explain how revenue is calculated.', icon: CircleHelp },
  { label: 'Review a dashboard', prompt: 'Summarize the Executive Sales dashboard.', icon: LayoutDashboard },
]

class LeapViewChatPage extends DatastarLit(LitElement) {
  private redirectedConversationID = ''
  private dashboardGenerationRun = ''
  private completedDashboardGenerationRun = ''
  @state() private visualLibraryState: VisualLibraryState = { savedIds: [], savingId: '', error: '' }
  @state() private dashboardPreview = false
  @state() private selectedPreviewVisual = ''
  @state() private builderOpen = false
  @state() private savingDashboard = false
  @state() private dashboardSaveError = ''
  @state() private savedBuilderHref = ''
  @state() private restoredBuilderHref: string | undefined
  @state() private savedDashboardArtifacts: ChatArtifactSignal[] = []
  @state() private savedDashboardVisuals: Record<string, VisualizationEnvelope> = {}
  @state() private retainedDashboardArtifacts: ChatArtifactSignal[] = []
  private retainedDashboardVisuals: Record<string, VisualizationEnvelope> = {}
  private pendingVisualRemoval: { artifactId: string; revisionId: string; pageId: string } | null = null
  private projectedDashboardId = ''
  @state() private dashboardCopies: Record<string, { id: string; pageId: string }> = {}
  // Keep identity links through removal so Undo can restore membership.
  private dashboardCopyLinks: Record<string, DashboardChatComponent[]> = {}
  private dashboardComponents: DashboardChatComponent[] = []
  private pendingDashboardChange: { artifactId: string; componentId: string; remove: boolean } | null = null
  private pendingPreviewArtifacts: string[] = []
  private dashboardRevisionId = ''
  @state() private dashboardPageId = ''
  @state() private dashboardPageTitle = ''
  @state() private dashboardPages: Array<{id: string; title: string}> = []
  @state() private builderUpdating = false
  @state() private builderCanEdit = true
  @state() private pendingDashboardPageId = ''
  private builderNeedsRefresh = false
  private visualCacheKey = ''
  private visualCache: Record<string, VisualizationEnvelope> = {}
  private visitedVisuals: string[] = []
  private warmedRenderers = new Set<string>()
  private savedSignature = ''
  private pendingSaveSignature = ''
  private saveRequestID = ''
  private saveTimer = 0
  private wasAgentRunning = false
  private arrangeCreatedDashboard = false
  private sidebarScroll = 0
  private chatScroll: { top: number; autoScroll: boolean } | null = null

  private get chatThread(): (HTMLElement & {
    updateComplete: Promise<boolean>
    captureScroll(): { top: number; autoScroll: boolean }
    restoreScroll(position: { top: number; autoScroll: boolean }): void
  }) | null {
    return this.shadowRoot?.querySelector('lv-chat-thread') ?? null
  }

  private enterBuilder(): void {
		this.closeVisual(false)
    this.sidebarScroll = this.shadowRoot?.querySelector<HTMLElement>('.preview-scroll')?.scrollTop ?? 0
    this.chatScroll = this.chatThread?.captureScroll() ?? null
    this.dashboardPreview = true
    this.builderOpen = true
    if (this.savedBuilderHref && this.builderNeedsRefresh && this.builderFrame) {
      this.builderFrame.src = this.savedBuilderHref
      this.builderNeedsRefresh = false
    }
    setChatPreviewLocation('builder')
  }

  private async openGeneratedDashboard(href: string, arrangeLayout = false): Promise<void> {
    this.arrangeCreatedDashboard = false
    this.dashboardSaveError = ''
    this.builderNeedsRefresh = false
    this.pendingDashboardChange = null
    this.pendingDashboardPageId = ''
    this.savedBuilderHref = ''
    this.restoredBuilderHref = undefined
    this.savedDashboardArtifacts = []
    this.savedDashboardVisuals = {}
    this.retainedDashboardArtifacts = []
    this.retainedDashboardVisuals = {}
    this.pendingVisualRemoval = null
    this.projectedDashboardId = ''
    this.builderCanEdit = true
    this.dashboardCopies = {}
    this.dashboardCopyLinks = {}
    this.dashboardPageId = ''
    this.selectedPreviewVisual = ''
    this.savingDashboard = true
    clearChatDashboardLocation()
    this.enterBuilder()
    await this.updateComplete
    if (this.builderFrame) {
      this.arrangeCreatedDashboard = arrangeLayout
      this.builderFrame.src = href
    }
    window.clearTimeout(this.saveTimer)
    this.saveTimer = window.setTimeout(() => {
      if (!this.savingDashboard) return
      this.savingDashboard = false
      this.dashboardSaveError = 'The dashboard is taking longer to open. Reload the chat to retry.'
    }, 45000)
  }

  private async restoreChatLayout(): Promise<void> {
    await this.updateComplete
    await this.chatThread?.updateComplete
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    const scroll = this.shadowRoot?.querySelector<HTMLElement>('.preview-scroll')
    if (scroll) scroll.scrollTop = this.sidebarScroll
    if (this.chatScroll) this.chatThread?.restoreScroll(this.chatScroll)
    this.shadowRoot?.querySelector<HTMLElement>('.conversation-titlebar .chat-size-toggle')?.focus({ preventScroll: true })
  }

  private readonly builderFrameName = `chat-builder-${crypto.randomUUID()}`

  private readonly mutationFrameName = `chat-dashboard-mutation-${crypto.randomUUID()}`

  private get mutationFrame(): HTMLIFrameElement | null {
    return this.shadowRoot?.querySelector<HTMLIFrameElement>('.mutation-frame') ?? null
  }

  private handleMutationLoad = (): void => {
    const frame = this.mutationFrame
    if (!this.savingDashboard || !frame?.contentDocument || frame.contentWindow?.location.href === 'about:blank') return
    if (frame.contentDocument.getElementById('chat-dashboard-receipt')) return
    this.dashboardSaveError = frame.contentDocument.body?.innerText.trim().slice(0, 500) || 'The dashboard could not be updated. Please try again.'
    this.savingDashboard = false
    this.pendingDashboardChange = null
    window.clearTimeout(this.saveTimer)
  }

  private get builderFrame(): HTMLIFrameElement | null {
    return this.shadowRoot?.querySelector<HTMLIFrameElement>('.builder-frame') ?? null
  }

  private get artifactSignature(): string {
    return JSON.stringify((this.agent.transcript ?? []).flatMap(item => item.status === 'complete' && item.artifact ? [item.artifact.id] : []))
  }

  private get dashboardSaved(): boolean {
    return Boolean(this.savedBuilderHref) && this.savedSignature === this.artifactSignature
  }

  private handleBuilderMessage = (event: MessageEvent<ChatDashboardMessage>): void => {
    if (event.origin !== window.location.origin) return
    const mutation = event.data?.type === 'lv-dashboard-mutation'
    if (event.source !== (mutation ? this.mutationFrame : this.builderFrame)?.contentWindow) return
    if (!mutation && this.builderNeedsRefresh) return
    if (event.data?.type === 'lv-builder-back-to-chat') {
      void this.closeDashboardPreview()
      return
    }
    if (event.data?.type === 'lv-builder-operation-error') {
      this.pendingDashboardPageId = ''
      this.dashboardSaveError = event.data.message
      this.savingDashboard = false
      this.pendingDashboardChange = null
      window.clearTimeout(this.saveTimer)
      return
    }
    if (event.data?.type !== 'lv-builder-saved' && event.data?.type !== 'lv-dashboard-mutation') return
    const href = new URL(event.data.href, window.location.href)
    if (href.origin !== window.location.origin || !href.pathname.startsWith('/dashboards/')) return
    const components = event.data.components
    this.dashboardComponents = components
    for (const component of components) if (component.artifactId) this.rememberDashboardCopy(component.artifactId, component)
    for (const [index, artifactId] of this.pendingPreviewArtifacts.entries()) {
      const component = components.find(component => component.id === `visual_${index + 1}`)
      if (component) this.rememberDashboardCopy(artifactId, component)
    }
    // An early loading projection may not contain the created components yet.
    if (this.pendingPreviewArtifacts.every((_, index) => components.some(component => component.id === `visual_${index + 1}`))) this.pendingPreviewArtifacts = []
    const pending = this.pendingDashboardChange
    const component = pending && components.find(component => component.id === pending.componentId)
    const completed = !pending || (pending.remove ? !component : Boolean(component))
    if (pending && completed) {
      if (component) this.rememberDashboardCopy(pending.artifactId, component)
      this.pendingDashboardChange = null
    }
    if (!mutation || !this.dashboardPageId) this.dashboardPageId = event.data.pageId
    this.reconcileDashboardCopies()
    if (completed) {
      window.clearTimeout(this.saveTimer)
      this.savingDashboard = false
      this.dashboardSaveError = ''
    }
    this.savedBuilderHref = href.pathname + href.search
    this.savedSignature = this.pendingSaveSignature || this.savedSignature
    this.dashboardRevisionId = event.data.revisionId
    if (mutation && this.dashboardPageId && this.savedBuilderHref) {
      href.searchParams.set('page', this.dashboardPageId)
      this.savedBuilderHref = href.pathname + href.search
    } else this.dashboardPageId = event.data.pageId
    this.persistDashboardLocation()
    if (event.data.type === 'lv-builder-saved') {
      this.dashboardPageTitle = event.data.pageTitle || event.data.pageId
      this.dashboardPages = event.data.pages ?? this.dashboardPages
      this.builderUpdating = event.data.updating === true
      this.builderCanEdit = event.data.canEdit !== false
      if (this.pendingDashboardPageId === event.data.pageId) this.pendingDashboardPageId = ''
      this.savedDashboardArtifacts = event.data.artifacts
      this.savedDashboardVisuals = event.data.visuals
      if (this.arrangeCreatedDashboard && event.data.canArrange && !event.data.updating) {
        this.arrangeCreatedDashboard = false
        this.builderFrame?.contentWindow?.postMessage({ type: 'lv-arrange-dashboard-visuals', reflow: true } satisfies ChatDashboardMessage, window.location.origin)
      }
    }
    const reference = event.data.reference
    if (this.projectedDashboardId && this.projectedDashboardId !== reference.reference.id) {
      this.retainedDashboardArtifacts = []
      this.retainedDashboardVisuals = {}
    }
    this.projectedDashboardId = reference.reference.id
    this.references = mergeReferences([reference], this.references.filter(item => item.reference.kind !== reference.reference.kind || item.reference.id !== reference.reference.id))
    if (event.data.type === 'lv-builder-saved') void this.syncDashboardContext(reference.reference.id, event.data.pageId, this.dashboardPageTitle, event.data.modelId ?? '')
    this.finishPendingVisualRemoval()
  }

  private async syncDashboardContext(dashboardId: string, pageId: string, pageTitle: string, modelId: string): Promise<void> {
    const runtime = await loadDatastarRuntime()
    // Ignore a projection superseded while the shared runtime was loading.
    if (pageId !== this.dashboardPageId || !this.references.some(reference => reference.reference.kind === 'dashboard' && reference.reference.id === dashboardId)) return
    runtime.mergePatch({agentContext: {surface: 'chat', dashboardId, pageId, pageTitle, modelId}})
  }

  private rememberDashboardCopy(artifactId: string, component: DashboardChatComponent): void {
    const links = this.dashboardCopyLinks[artifactId] ?? []
    if (!links.some(link => link.id === component.id && link.pageId === component.pageId)) this.dashboardCopyLinks[artifactId] = [...links, component]
  }

  private reconcileDashboardCopies(): void {
    for (const [artifactId, savedId] of Object.entries(this.visualLibraryState.libraryIds ?? {})) {
      for (const component of this.dashboardComponents) {
        if ((component.savedVisualId ?? savedVisualSourceId(component.id)) === savedId) this.rememberDashboardCopy(artifactId, component)
      }
    }
    const copies: Record<string, DashboardChatComponent> = {}
    for (const [artifactId, links] of Object.entries(this.dashboardCopyLinks)) {
      const component = this.dashboardComponents.find(component => component.pageId === this.dashboardPageId && links.some(link => link.id === component.id && link.pageId === component.pageId))
      if (component) copies[artifactId] = component
    }
    this.dashboardCopies = copies
  }

  private handleVisualLibraryState = (event: CustomEvent<VisualLibraryState>): void => {
    this.visualLibraryState = event.detail
    const prefix = `dashboard:${this.projectedDashboardId}:${this.dashboardPageId}:`
    for (const artifact of event.detail.dashboardArtifacts ?? []) {
      if (!artifact.id.startsWith(prefix)) continue
      if (!this.retainedDashboardArtifacts.some(item => item.id === artifact.id)) {
        this.retainedDashboardArtifacts = [...this.retainedDashboardArtifacts, this.savedDashboardArtifacts.find(item => item.id === artifact.id) ?? artifact]
      }
      this.rememberDashboardCopy(artifact.id, {id: artifact.id.slice(prefix.length), pageId: this.dashboardPageId})
    }
    this.reconcileDashboardCopies()
    this.finishPendingVisualRemoval()
  }

  private finishPendingVisualRemoval(): void {
    if (this.savingDashboard || this.builderUpdating || this.pendingDashboardPageId) return
    const state = this.visualLibraryState
    if (this.pendingVisualRemoval && !state.savingId) {
      const pending = this.pendingVisualRemoval
      this.pendingVisualRemoval = null
      if (!state.error && state.libraryIds?.[pending.artifactId]) {
        if (pending.revisionId === this.dashboardRevisionId && pending.pageId === this.dashboardPageId) this.removeDashboardVisual(pending.artifactId)
        else this.dashboardSaveError = 'The dashboard changed. Please try removing this visual again.'
      }
    }
  }

  private handleBuilderLoad = (): void => {
    const frame = this.builderFrame
    if (!this.savingDashboard || !frame?.contentDocument || frame.contentWindow?.location.href === 'about:blank') return
    if (frame.contentDocument.querySelector('lv-dashboard-builder')) return
    this.dashboardSaveError = frame.contentDocument.body?.innerText.trim().slice(0, 500) || 'Dashboard could not be saved. Please try again.'
    this.savingDashboard = false
    window.clearTimeout(this.saveTimer)
  }

  private refreshSavedVisuals = (): void => {
    this.builderFrame?.contentWindow?.postMessage({ type: 'lv-refresh-saved-visuals' }, window.location.origin)
  }

  private get liveBuilder(): Window | null {
    const frame = this.builderFrame
    return !this.builderNeedsRefresh && frame?.contentDocument?.querySelector('lv-dashboard-builder') ? frame.contentWindow : null
  }

  private selectDashboardPage(event: Event): void {
    const pageId = (event.target as HTMLSelectElement).value
    if (!this.liveBuilder || pageId === this.dashboardPageId || !this.dashboardPages.some(page => page.id === pageId)) return
    this.pendingDashboardPageId = pageId
    this.liveBuilder.postMessage({type: 'lv-select-dashboard-page', pageId} satisfies ChatDashboardMessage, window.location.origin)
  }

  private beginDashboardChange(keepBuilder = false): void {
    this.savingDashboard = true
    if (!keepBuilder) {
      this.builderNeedsRefresh = true
      if (this.builderFrame) this.builderFrame.src = 'about:blank'
    }
    this.dashboardSaveError = ''
    window.clearTimeout(this.saveTimer)
    this.saveTimer = window.setTimeout(() => {
      this.savingDashboard = false
      this.pendingDashboardChange = null
      this.dashboardSaveError = 'The dashboard update is taking longer than expected. Please try again.'
    }, 45000)
  }

  private addAgentVisual = async (event: CustomEvent<{ savedId: string; artifactId: string }>): Promise<void> => {
    event.preventDefault()
    if (!this.builderCanEdit || this.savingDashboard || this.builderUpdating || this.pendingDashboardPageId || this.dashboardCopies[event.detail.artifactId]) return
    const requestId = uuidv7()
    this.pendingDashboardChange = {
      artifactId: event.detail.artifactId,
      componentId: savedVisualComponentId(event.detail.savedId, requestId),
      remove: false,
    }
    const builder = this.liveBuilder
    this.beginDashboardChange(Boolean(builder))
    if (builder) {
      builder.postMessage({type: 'lv-add-saved-visual', id: event.detail.savedId, requestId, pageId: this.dashboardPageId}, window.location.origin)
      return
    }
    await this.updateComplete
    if (this.savedBuilderHref) {
      const path = new URL(this.savedBuilderHref, window.location.href).pathname.replace(/\/edit$/, '/draft/saved-visual')
      submitVisualForm(path, this.mutationFrame, {
        savedVisualId: event.detail.savedId, idempotencyKey: requestId, chatReceipt: '1',
        pageId: this.dashboardPageId, revisionId: this.dashboardRevisionId,
      })
    } else {
      this.pendingSaveSignature = ''
      submitVisualForm('/dashboards/new', this.mutationFrame, {
        chatReceipt: '1', savedVisualId: event.detail.savedId, title: conversationTitle(this.agent), idempotencyKey: requestId,
      })
    }
  }

  private toggleDashboardVisual(artifactId: string): void {
    if (!this.builderCanEdit || this.savingDashboard || this.builderUpdating || this.pendingDashboardPageId || this.visualLibraryState.savingId) return
    const copy = this.dashboardCopies[artifactId]
    if (!copy) {
      this.savePreviewVisual(artifactId, true)
      return
    }
    if (!this.agent.transcript?.some(item => item.artifact?.id === artifactId)) {
      // Save the canonical source before removing its last placement, so Add
      // remains an independent import even after later dashboard edits.
      this.pendingVisualRemoval = {artifactId, revisionId: this.dashboardRevisionId, pageId: this.dashboardPageId}
      this.dispatchEvent(new CustomEvent('lv-save-agent-visual', {bubbles: true, composed: true, detail: {artifactId, add: false, retain: true}}))
      return
    }
    this.removeDashboardVisual(artifactId)
  }

  private removeDashboardVisual(artifactId: string): void {
    const copy = this.dashboardCopies[artifactId]
    if (!copy || !this.builderCanEdit || this.savingDashboard || this.builderUpdating || this.pendingDashboardPageId) return
    if (!this.agent.transcript?.some(item => item.artifact?.id === artifactId)) {
      const artifact = this.previewArtifacts.find(item => item.id === artifactId)
      if (artifact && !this.retainedDashboardArtifacts.some(item => item.id === artifactId)) this.retainedDashboardArtifacts = [...this.retainedDashboardArtifacts, artifact]
      if (this.visuals[artifactId]) this.retainedDashboardVisuals[artifactId] = this.visuals[artifactId]
    }
    this.pendingDashboardChange = { artifactId, componentId: copy.id, remove: true }
    const builder = this.liveBuilder
    this.beginDashboardChange(Boolean(builder))
    if (builder) {
      builder.postMessage({type: 'lv-remove-dashboard-visual', pageId: copy.pageId, componentId: copy.id}, window.location.origin)
      return
    }
    const path = new URL(this.savedBuilderHref, window.location.href).pathname.replace(/\/edit$/, '/draft/chat-remove-visual')
    submitVisualForm(path, this.mutationFrame, {
      chatReceipt: '1', pageId: copy.pageId, componentId: copy.id, revisionId: this.dashboardRevisionId,
    })
  }

  private async saveDashboard(openBuilder = false): Promise<void> {
    this.dashboardSaveError = ''
    if (this.dashboardSaved || (openBuilder && this.savedBuilderHref)) {
      if (openBuilder) this.enterBuilder()
      return
    }
    if (this.savingDashboard) return
    // Preview opens an empty draft until the user explicitly adds a visual.
    const item = this.agent.transcript?.find(item => item.artifact && item.status === 'complete')
    let semanticModel: string
    try {
      semanticModel = JSON.parse(item?.argumentsJson || item?.inputJson || '').semanticModelId
      if (!semanticModel) throw new Error('Missing semantic model')
    } catch {
      this.dashboardSaveError = 'Ask the agent to create a visual before opening a dashboard.'
      return
    }
    this.saveRequestID ||= uuidv7()
    this.pendingSaveSignature = this.artifactSignature
    this.pendingPreviewArtifacts = []
    this.builderNeedsRefresh = false
    if (openBuilder) this.enterBuilder()
    this.savingDashboard = true
    await this.updateComplete
    submitVisualForm('/dashboards/new', this.builderFrame, {
      title: conversationTitle(this.agent),
      semanticModel, embed: 'chat',
      idempotencyKey: this.saveRequestID,
    })
    this.saveTimer = window.setTimeout(() => {
      if (!this.savingDashboard) return
      this.savingDashboard = false
      this.dashboardSaveError = 'The save is taking longer than expected. Retry to retrieve the same draft.'
    }, 45000)
  }


  connectedCallback(): void {
    super.connectedCallback()
    if (typeof window === 'undefined') return
    this.compactMedia = window.matchMedia('(max-width: 768px)')
    this.compactViewport = this.compactMedia.matches
    this.compactMedia.addEventListener('change', this.onCompactViewportChange)
    this.syncPreviewLocation()
    window.addEventListener('popstate', this.syncPreviewLocation)
    window.addEventListener('message', this.handleBuilderMessage)
    document.addEventListener('datastar-signal-patch', this.invalidateVisualCache)
  }

  disconnectedCallback(): void {
    this.compactMedia?.removeEventListener('change', this.onCompactViewportChange)
    this.compactMedia = undefined
    window.removeEventListener('popstate', this.syncPreviewLocation)
    window.removeEventListener('message', this.handleBuilderMessage)
    document.removeEventListener('datastar-signal-patch', this.invalidateVisualCache)
    window.clearTimeout(this.saveTimer)
    super.disconnectedCallback()
  }

  private syncPreviewLocation = (): void => {
    const location = new URL(window.location.href)
    const preview = location.searchParams.get('preview')
    const href = this.validBuilderHref(location.searchParams.get('dashboard'))
    if (href && href !== this.savedBuilderHref) {
      this.savedBuilderHref = href
      this.restoredBuilderHref = href
      const retained = window.history.state?.chatDashboard
      if (retained?.href === href) {
        this.dashboardCopyLinks = retained.links ?? {}
        this.savedSignature = retained.signature ?? ''
      }
    }
    const wasBuilder = this.builderOpen
    this.dashboardPreview = preview === 'dashboard' || preview === 'builder'
    this.builderOpen = preview === 'builder' && Boolean(this.savedBuilderHref)
    if (wasBuilder && !this.builderOpen) void this.restoreChatLayout()
  }

  private validBuilderHref(value: string | null): string {
    if (!value) return ''
    try {
      const href = new URL(value, window.location.href)
      if (href.origin !== window.location.origin || !/^\/dashboards\/[^/]+\/edit$/.test(href.pathname)) return ''
      const result = new URL(href.pathname, window.location.origin)
      result.searchParams.set('embed', 'chat')
      if (href.searchParams.has('draft')) result.searchParams.set('draft', href.searchParams.get('draft')!)
      if (href.searchParams.has('page')) result.searchParams.set('page', href.searchParams.get('page')!)
      return result.pathname + result.search
    } catch { return '' }
  }

  private persistDashboardLocation(): void {
    const href = this.validBuilderHref(this.savedBuilderHref)
    if (!href) return
    rememberChatDashboardLocation(href, this.dashboardCopyLinks, this.savedSignature)
  }

  private async openDashboardPreview(event?: CustomEvent<{ artifactId?: string }>): Promise<void> {
		this.closeVisual(false)
    const fromBuilder = this.builderOpen
    const position = fromBuilder ? this.chatScroll : this.chatThread?.captureScroll()
    this.builderOpen = false
    this.selectedPreviewVisual = event?.detail?.artifactId ?? this.selectedPreviewArtifact?.id ?? ''
    this.visitedVisuals = [...this.visitedVisuals.filter(id => id !== this.selectedPreviewVisual), this.selectedPreviewVisual].slice(-3)
    if (!this.dashboardPreview || fromBuilder) {
      setChatPreviewLocation('dashboard', fromBuilder)
      this.dashboardPreview = true
    }
    await this.updateComplete
    await this.chatThread?.updateComplete
    if (position) this.chatThread?.restoreScroll(position)
    const cards = this.shadowRoot?.querySelectorAll<HTMLElement>('[data-preview-visual]')
    const selectedID = this.selectedPreviewArtifact?.id
    const selected = Array.from(cards ?? []).find(card => card.dataset.previewVisual === selectedID)
    if (selected) {
      selected.scrollIntoView({ block: 'nearest', behavior: 'auto' })
      selected.focus({ preventScroll: true })
    }
  }

  private async closeDashboardPreview(): Promise<void> {
    this.builderOpen = false
    this.dashboardPreview = true
    setChatPreviewLocation('dashboard', true)
    await this.restoreChatLayout()
  }

  private async closeVisualSidebar(): Promise<void> {
    const position = this.chatThread?.captureScroll()
    this.dashboardPreview = false
    setChatPreviewLocation(null, true)
    await this.updateComplete
    await this.chatThread?.updateComplete
    if (position) this.chatThread?.restoreScroll(position)
    this.shadowRoot?.querySelector<HTMLElement>('.open-preview')?.focus()
  }

  private get selectedPreviewArtifact(): ChatArtifactSignal | undefined {
    const artifacts = this.previewArtifacts
    return artifacts.find(artifact => artifact.id === this.selectedPreviewVisual) ?? artifacts[0]
  }

  private savePreviewVisual(artifactId: string, add: boolean): void {
    this.dispatchEvent(new CustomEvent('lv-save-agent-visual', {
      bubbles: true, composed: true, detail: { artifactId, add },
    }))
  }

  private get previewArtifacts(): ChatArtifactSignal[] {
    const artifacts = new Map<string, ChatArtifactSignal>()
    for (const item of this.agent.transcript ?? []) {
      if (item.artifact && item.status === 'complete') artifacts.set(item.artifact.id, item.artifact)
    }
    for (const artifact of this.pageVisualLinks) artifacts.set(artifact.id, artifact)
    return [...artifacts.values()]
  }

  private get pageVisualLinks(): ChatArtifactSignal[] {
    const conversationIDs = new Set((this.agent.transcript ?? []).flatMap(item => item.artifact ? [item.artifact.id] : []))
    const retained = this.retainedDashboardArtifacts.filter(artifact => this.dashboardCopyLinks[artifact.id]?.some(copy => copy.pageId === this.dashboardPageId))
    const represented = new Set([...conversationIDs, ...retained.map(item => item.id)])
    const current = this.savedDashboardArtifacts.filter(artifact => {
      const component = this.dashboardCopies[artifact.id]
      return !component || !Object.entries(this.dashboardCopies).some(([id, copy]) => id !== artifact.id && represented.has(id) && copy.id === component.id && copy.pageId === component.pageId)
    })
    return [...new Map([...retained, ...current].map(artifact => [artifact.id, artifact])).values()]
  }

  private get dashboardVisualSource(): DashboardVisualSource {
    const artifacts = this.pageVisualLinks
    const components = artifacts.flatMap(artifact => {
      const copy = this.dashboardCopies[artifact.id]
      return copy ? [{...copy, artifactId: artifact.id}] : []
    })
    return {dashboardId: this.projectedDashboardId, revisionId: this.dashboardRevisionId, pageId: this.dashboardPageId, components, artifacts}
  }

  @state() private selectedVisualID = ''
  @state() private selectedExplorerHref = ''
  @state() private selectedVisualTitle = ''
  @state() private visualSaving = false
  @state() private visualSaved = false
  @state() private visualSaveError = ''
  @state() private dashboardPickerOpen = false
  @state() private dashboardDestination?: ChatDashboardResult
  @state() private compactViewport = false
  private compactMedia?: MediaQueryList
  @state() private references: AgentReferenceSignal[] = []
	@state() private editMessageId = ''
	@state() private optimisticTurn: ChatTranscriptItemSignal | null = null
	private optimisticConversationID = ''
	private optimisticEditMessageID = ''
	private optimisticBaselineMessageIDs = new Set<string>()
	private trackedConversationID: string | null = null
	private trackedAcceptedRunID: string | null = null

  static styles = [chatPageStyles, chatPagePreviewStyles]

  private onCompactViewportChange = (event: MediaQueryListEvent): void => {
    this.compactViewport = event.matches
  }

  updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('builderOpen')) {
      this.dispatchEvent(new CustomEvent('lv-chat-layout-change', {
        bubbles: true, composed: true, detail: { builderOpen: this.builderOpen },
      }))
    }
    if (!this.hasBootstrapSignals) return
    // Start loading only renderers required by this conversation before a click.
    for (const envelope of Object.values(this.visuals)) {
      if (this.warmedRenderers.has(envelope.rendererID)) continue
      this.warmedRenderers.add(envelope.rendererID)
      try {
        void visualizationRegistry.load(visualizationRegistry.resolve(envelope)).catch(() => this.warmedRenderers.delete(envelope.rendererID))
      } catch { this.warmedRenderers.delete(envelope.rendererID) }
    }
    checkSignalContract('chat page', this.page, {
      title: 'required',
    })
    checkSignalContract('chat agent', this.agent, {
      transcript: 'required',
      status: 'required',
      composer: 'required',
    })
		this.syncEditState()
		this.syncOptimisticTurn()
    if (this.selectedVisualID && !this.visuals[this.selectedVisualID]) this.closeVisual(false)
    this.navigateFromDraft()
    const running = Boolean(this.agent.status.running)
    const runId = this.agent.status.runId || latestAcceptedRunId(this.agent.transcript ?? [])
    if (running && runId) this.dashboardGenerationRun = runId
    if (!running && this.dashboardGenerationRun && this.dashboardGenerationRun !== this.completedDashboardGenerationRun) {
      const completedRun = this.dashboardGenerationRun
      const href = generatedDashboardHref(this.agent.transcript ?? [], completedRun, this.agent.activeConversationId ?? '')
      if (href) this.completedDashboardGenerationRun = completedRun
      if (href) {
        const created = (this.agent.transcript ?? []).some(item => item.runId === completedRun && item.name === 'create_dashboard_draft' && item.status === 'complete' && !item.error)
        void this.openGeneratedDashboard(href, created)
      }
    }
    // Reopened dashboard replies have no query_visual receipts. Reuse the
    // authorized builder stream to load their cards and existing membership,
    // while keeping the conversation visible and without another AI request.
    if (!running && !this.dashboardPreview && !this.savedBuilderHref && !this.restoredBuilderHref) {
      const authored = [...this.agent.transcript ?? []].reverse().find(item => item.kind === 'tool' && ['create_dashboard_draft', 'fork_dashboard', 'edit_dashboard_source'].includes(item.name ?? '') && item.status === 'complete' && !item.error)
      const href = generatedDashboardHref(this.agent.transcript ?? [], authored?.runId ?? '', this.agent.activeConversationId ?? '')
      if (href) this.restoredBuilderHref = href
    }
    if (this.wasAgentRunning && !running && this.savedBuilderHref && !this.savingDashboard) {
      const frame = this.builderFrame
      if (frame?.contentWindow && frame.contentWindow.location.href !== 'about:blank') frame.contentWindow.postMessage({ type: 'lv-refresh-builder' } satisfies ChatDashboardMessage, window.location.origin)
      else this.builderNeedsRefresh = true
    }
    this.wasAgentRunning = running
  }

	private syncOptimisticTurn(): void {
		if (!this.optimisticTurn) return
		const conversationID = this.agent.activeConversationId?.trim() ?? ''
		const accepted = (this.agent.transcript ?? []).some((item) =>
			item.kind === 'user'
			&& Boolean(item.id?.trim())
			&& !this.optimisticBaselineMessageIDs.has(item.id.trim()),
		)
		if (conversationID !== this.optimisticConversationID || accepted || Boolean(this.agent.status?.error)) {
			this.clearOptimisticTurn()
		}
	}

	private syncEditState(): void {
		const conversationID = this.agent.activeConversationId?.trim() ?? ''
		const acceptedRunID = latestAcceptedRunId(this.agent.transcript ?? [])
		if (this.editMessageId && this.trackedConversationID !== null && this.trackedConversationID !== conversationID) {
			this.references = []
			this.shadowRoot?.querySelector<HTMLElement & { setDraft(value: string): void }>('lv-chat-composer')?.setDraft('')
		}
		if (this.trackedConversationID !== null && this.trackedConversationID !== conversationID) {
      this.closeVisual(false)
      this.dashboardDestination = undefined
    }
		if (
			(this.trackedConversationID !== null && this.trackedConversationID !== conversationID)
			|| (this.trackedAcceptedRunID !== null && acceptedRunID && this.trackedAcceptedRunID !== acceptedRunID)
		) {
			this.clearEditMessage()
		}
		this.trackedConversationID = conversationID
		this.trackedAcceptedRunID = acceptedRunID
	}

  private navigateFromDraft(): void {
    const conversationID = this.agent.activeConversationId?.trim()
    if (this.page?.view !== 'new' || !conversationID || conversationID === this.redirectedConversationID) return
    this.redirectedConversationID = conversationID
    window.location.assign(`/chats/${encodeURIComponent(conversationID)}`)
  }

  get page(): ChatPageSignal | null {
    return this.signal<ChatPageSignal | null>('page', null)
  }

  private get hasBootstrapSignals(): boolean {
    const agent = this.signal<ChatSignal | null>('agent', null)
    return this.page?.kind === 'chat' && Boolean(agent?.status && agent?.composer)
  }

  get agent(): ChatSignal {
    return this.signal<ChatSignal>('agent', emptyAgent)
  }

  private invalidateVisualCache = (event: Event): void => {
    const patch = (event as CustomEvent<Record<string, unknown>>).detail
    if (patch && Object.hasOwn(patch, 'visuals')) {
      this.visualCacheKey = ''
      this.requestUpdate()
    }
  }

  get visuals(): Record<string, VisualizationEnvelope> {
    // UI-only changes do not need to clone every dataset or reapply a chart.
    const raw = (this.signals.visuals ?? {}) as Record<string, VisualizationEnvelope>
    const key = JSON.stringify([(this.signals.agent as ChatSignal | undefined)?.activeConversationId, Object.entries(raw).map(([id, visual]) => [
      id, visual.schemaVersion, visual.rendererID, visual.specRevision, visual.dataRevision,
      visual.dataState.kind, visual.dataState.generation, visual.dataState.kind === 'inline' ? null : visual.dataState, visual.status, visual.selection, visual.highlights, visual.diagnostics,
    ])])
    if (key !== this.visualCacheKey) {
      this.visualCacheKey = key
      this.visualCache = chatVisualsFromSignals(this.signal<Record<string, VisualizationEnvelope>>('visuals', {}))
    }
    const visuals = { ...this.visualCache, ...this.retainedDashboardVisuals, ...this.savedDashboardVisuals }
    for (const artifact of this.retainedDashboardArtifacts) {
      const copy = this.dashboardCopies[artifact.id]
      const currentId = copy && this.dashboardComponents.find(component => component.id === copy.id && component.pageId === copy.pageId)?.artifactId
      if (currentId && this.savedDashboardVisuals[currentId]) visuals[artifact.id] = this.savedDashboardVisuals[currentId]
    }
    return visuals
  }

  get pending(): boolean {
    return this.signal<boolean>('agentTurnPending', false) || Boolean(this.agent.status?.running) || Boolean(this.optimisticTurn)
  }

	get referenceSearch(): AgentReferenceSearchSignal {
		return this.signal<AgentReferenceSearchSignal>('agentReferenceSearch', {
			query: '', requestId: 0, results: [],
		})
	}

  get composerDisabled(): boolean {
    const agent = this.agent
    return this.pending || Boolean(agent.status?.running) || Boolean(agent.composer?.disabled) || Boolean(this.pendingDashboardPageId)
  }

  get context(): AgentContextSignal | null {
    return this.signal<AgentContextSignal | null>('agentContext', null)
  }

  render() {
    if (!this.hasBootstrapSignals) {
      return html`<div class="route"><section class="main new-main" aria-label="LeapView chats"><div class="loading-state" role="status">Loading chat…</div></section></div>`
    }
    const page = this.page
    const agent = this.agent ?? emptyAgent
    const status = agent.status ?? emptyAgent.status
    const composer = agent.composer ?? emptyAgent.composer
    const view = page?.view ?? 'conversation'
    const isList = view === 'list'
    const isNew = view === 'new'
    const title = conversationTitle(agent)
    const selectedVisual = this.selectedVisualID ? this.visuals[this.selectedVisualID] : undefined
    return html`
      <div class=${selectedVisual && !isList && !isNew ? 'route visual-open' : 'route'} @lv-add-agent-visual=${this.addAgentVisual} @lv-saved-visuals-changed=${this.refreshSavedVisuals} @lv-chat-submit=${this.showOptimisticTurn} @lv-chat-dashboard-preview=${this.openDashboardPreview}>
        <iframe class="mutation-frame" name=${this.mutationFrameName} title="Dashboard update" hidden @load=${this.handleMutationLoad}></iframe>
        <section class=${['main', isList ? 'list-main' : '', isNew ? 'new-main' : '', this.builderOpen ? 'builder-main' : ''].filter(Boolean).join(' ')} aria-label="LeapView chats" ?inert=${Boolean(selectedVisual && this.compactViewport)}>
          ${isList || isNew || this.builderOpen ? null : this.renderConversationTitlebar(title)}
          <div class=${`chat-layout${this.dashboardPreview && !isList && !isNew ? ` preview-open${this.builderOpen ? ' builder-open' : ''}` : ''}`}>
            ${this.renderDashboardPreview(title, !isList && !isNew && this.dashboardPreview && !this.builderOpen)}
            <section class="builder-stage" aria-label="Dashboard builder" ?hidden=${!this.dashboardPreview || !this.builderOpen}>
              ${this.dashboardSaveError ? html`<p class="save-error" role="alert">${this.dashboardSaveError}</p>` : null}
              <iframe class="builder-frame" name=${this.builderFrameName} title="Dashboard builder" src=${ifDefined(this.restoredBuilderHref)} @load=${this.handleBuilderLoad} ?hidden=${Boolean(this.dashboardSaveError)}></iframe>
            </section>
            <div class=${`body${this.builderOpen ? ' with-chat-header' : ''}`}>
              <div class="chat-pane-header" ?hidden=${!this.builderOpen}>
                <span class="chat-pane-heading" title=${this.dashboardPageTitle ? `Chat · ${this.dashboardPageTitle}` : 'Chat'}>${agentIcon()}<span>Chat</span>${this.dashboardPageTitle ? html`<span class="chat-page-name"> · ${this.dashboardPageTitle}</span>` : null}</span>
                <div class="titlebar-actions">
                  <button class="chat-size-toggle" type="button" aria-label="Expand chat" title="Expand chat" @click=${this.closeDashboardPreview}>${lucideIcon(Maximize2)}</button>
                </div>
              </div>
              ${isList ? this.renderListView(agent) : isNew ? this.renderNewView(composer, status) : this.renderConversationView(agent, status, composer)}
            </div>
          </div>
          <lv-visual-modal></lv-visual-modal>
        </section>
        ${selectedVisual && !isList && !isNew ? html`
          <lv-chat-visual-panel
            artifact-id=${this.selectedVisualID}
            title=${this.selectedVisualTitle || selectedVisual.spec.title || 'Visual result'}
            .payload=${selectedVisual}
            .explorerHref=${this.selectedExplorerHref}
            .saving=${this.visualSaving}
            .saved=${this.visualSaved}
            .saveError=${this.visualSaveError}
            .modal=${this.compactViewport}
            .dashboardAvailable=${Boolean(agent.activeConversationId) && (agent.transcript ?? []).some(item => item.kind === 'tool' && item.name === 'query_visual' && item.status === 'complete' && item.artifact?.id === this.selectedVisualID)}
            @lv-chat-visual-add-dashboard=${() => { this.dashboardPickerOpen = true }}
            @lv-chat-visual-close=${() => this.closeVisual(true)}
            @lv-chat-visual-save=${this.saveVisual}
          ></lv-chat-visual-panel>
        ` : null}
      </div>
      ${this.dashboardPickerOpen && selectedVisual ? html`
        <lv-chat-dashboard-picker
          .conversationId=${agent.activeConversationId}
          .artifactId=${this.selectedVisualID}
          .visualTitle=${this.selectedVisualTitle || selectedVisual.spec.title || 'Visual result'}
          .preferredDashboardId=${this.dashboardDestination?.dashboardId ?? ''}
          .preferredPageId=${this.dashboardDestination?.pageId ?? ''}
          @lv-chat-dashboard-added=${this.dashboardVisualAdded}
          @lv-chat-dashboard-close=${this.closeDashboardPicker}
          @lv-chat-dashboard-add-another=${this.addAnotherVisual}
        ></lv-chat-dashboard-picker>
      ` : null}
    `
  }

  private renderConversationTitlebar(title: string) {
    return html`
      <div class="conversation-titlebar">
        <div class="titlebar-start">
          <h1>${title}</h1>
        </div>
        <div class="titlebar-actions">
          ${!this.dashboardPreview && this.previewArtifacts.length ? html`<button class="preview-action open-preview" type="button" @click=${() => this.openDashboardPreview()}>${lucideIcon(LayoutDashboard)} Visuals (${this.previewArtifacts.length})</button>` : null}
          ${this.savedBuilderHref || this.previewArtifacts.length ? html`<button class="preview-action" type="button" ?disabled=${this.savingDashboard || this.builderUpdating} @click=${() => this.saveDashboard(true)}>${lucideIcon(LayoutDashboard)} Preview dashboard</button>` : null}
          ${this.previewArtifacts.length ? html`<button class="chat-size-toggle" type="button" aria-label="Shrink chat" title="Shrink chat" @click=${() => this.openDashboardPreview()}>${lucideIcon(Minimize2)}</button>` : null}
        </div>
      </div>
    `
  }

  private requestDashboardVisualWindow = (event: CustomEvent<VisualizationWindowRequest>): void => {
    const payload = this.selectedPreviewVisual ? this.visuals[this.selectedPreviewVisual] : undefined
    if (payload?.dataState.kind !== 'windowed' || payload.visualID !== event.detail.visualID || !this.dashboardPageId) return
    event.stopPropagation()
    this.liveBuilder?.postMessage({ type: 'lv-builder-visual-window', pageId: this.dashboardPageId, request: event.detail } satisfies ChatDashboardMessage, window.location.origin)
  }

  private renderDashboardPreview(title: string, visible: boolean) {
    const selected = this.selectedPreviewArtifact
    // Keep at most three visited renderers mounted; only the selected one is visible.
    const visited = new Set([...this.visitedVisuals, ...(selected ? [selected.id] : [])])
    const artifacts = this.previewArtifacts.filter(artifact => visited.has(artifact.id))
    const visuals = this.visuals
    const saved = Boolean(selected && this.visualLibraryState.savedIds.includes(selected.id))
    const saving = Boolean(this.visualLibraryState.savingId)
    const added = Boolean(selected && this.dashboardCopies[selected.id])
    const canSave = Boolean(selected && this.agent.transcript?.some(item => item.artifact?.id === selected.id))
    return html`
      <section class="preview-panel" aria-label="Dashboard preview" ?hidden=${!visible} @lv-visualization-window-request=${this.requestDashboardVisualWindow}>
        <div class="preview-heading">
          <h2>Visual</h2>
          <div class="preview-actions">
            ${canSave ? html`<button class="preview-action" type="button" ?disabled=${!selected || saving} aria-pressed=${saved} title=${saved ? 'Unsave visual' : 'Save visual'} @click=${() => selected && this.savePreviewVisual(selected.id, false)}>${lucideIcon(saved ? Check : Save)} ${selected && this.visualLibraryState.savingId === selected.id ? 'Updating…' : saved ? 'Saved' : 'Unsaved'}</button>` : null}
            <button class="preview-action" type="button" ?disabled=${!selected || !this.builderCanEdit || saving || this.savingDashboard || this.builderUpdating || Boolean(this.pendingDashboardPageId) || Boolean(this.savedBuilderHref && !this.dashboardPageId)} aria-pressed=${added} @click=${() => selected && this.toggleDashboardVisual(selected.id)}>${lucideIcon(added ? Minus : Plus)} ${this.savingDashboard ? 'Updating…' : added ? 'Remove from dashboard' : 'Add to dashboard'}</button>
            <button class="preview-action close-visuals" type="button" aria-label="Close visuals sidebar" @click=${this.closeVisualSidebar}>${lucideIcon(X)}</button>
          </div>
        </div>
        ${this.savedBuilderHref ? html`<label class="dashboard-destination">Dashboard page
          <select aria-label="Dashboard page" .value=${this.pendingDashboardPageId || this.dashboardPageId} ?disabled=${this.savingDashboard || this.builderUpdating || Boolean(this.pendingDashboardPageId) || !this.dashboardPages.length} @change=${this.selectDashboardPage}>
            ${this.dashboardPages.map(page => html`<option value=${page.id} .selected=${page.id === (this.pendingDashboardPageId || this.dashboardPageId)}>${page.title}</option>`)}
          </select>
          <span>${added ? 'Added to this page' : 'Add to this page'}</span>
        </label>` : null}
        <div class="preview-scroll">
        ${this.visualLibraryState.error ? html`<p class="save-error" role="alert">${this.visualLibraryState.error}</p>` : null}
        ${this.dashboardSaveError ? html`<p class="save-error" role="alert">${this.dashboardSaveError}</p>` : null}
        <div class="preview-grid" aria-label=${title}>
          ${this.dashboardPreview ? repeat(artifacts, artifact => artifact.id, artifact => {
            const payload = visuals[artifact.id]
            const kind = payload?.spec.kind ?? artifact.type
            const removedTable = payload?.dataState.kind === 'windowed' && this.retainedDashboardVisuals[artifact.id] && !this.dashboardCopies[artifact.id]
            const removedVisual = !payload && this.visualLibraryState.libraryIds?.[artifact.id] && !this.dashboardCopies[artifact.id]
            return html`
              <div class=${`preview-card${kind === 'kpi' ? ' kpi' : ''}${['table', 'matrix', 'pivot'].includes(kind) ? ' wide' : ''}${this.selectedPreviewVisual === artifact.id ? ' selected' : ''}`}
                ?hidden=${artifact.id !== selected?.id} data-preview-visual=${artifact.id} tabindex="-1" aria-label=${payload?.spec.title || artifact.summary || 'Visual'}>
                ${removedTable ? html`<p class="preview-empty" role="status">Add this table back to the dashboard to view, sort, or load more rows.</p>` : removedVisual ? html`<p class="preview-empty" role="status">Add this visual back to the dashboard to view it.</p>` : payload ? html`<lv-visual-artifact eager type=${artifact.type} artifact-id=${artifact.id} .payload=${payload}></lv-visual-artifact>` : html`<p class="preview-empty" role="status">Loading visual…</p>`}
              </div>`
          }) : null}
        </div>
        ${!artifacts.length ? html`<p class="preview-empty">Ask the agent to create a visual. It will appear here as soon as it is ready.</p>` : null}
        </div>
      </section>
    `
  }

  private renderListView(agent: ChatSignal) {
    return html`
      <lv-chat-list
        .conversations=${agent.conversations ?? []}
        .agentEnabled=${Boolean(agent.status?.enabled)}
        active-conversation-id=${agent.activeConversationId ?? ''}
      ></lv-chat-list>
    `
  }

  private renderNewView(composer: ChatSignal['composer'], status: ChatSignal['status']) {
    const disabled = this.composerDisabled || status.running || composer.disabled
    return html`
      <div class="new-chat-stage">
        <section class="new-chat-intro" aria-labelledby="new-chat-title">
          <div class="new-chat-heading">
            <span class="agent-mark" aria-hidden="true">${agentIcon()}</span>
            <h1 id="new-chat-title" class="new-chat-title">Ask about your data</h1>
          </div>
        </section>
        ${this.renderComposer(composer, status, true)}
        <div class="prompt-starters" aria-label="Example questions">
          ${promptStarters.map((starter) => html`
            <button class="prompt-starter" type="button" title=${starter.prompt} aria-label=${`${starter.label}: ${starter.prompt}`} ?disabled=${disabled} @click=${() => this.selectPromptStarter(starter.prompt)}>
              <span class="prompt-starter-icon" aria-hidden="true">${lucideIcon(starter.icon, { size: 16, strokeWidth: 2 })}</span>
              <span class="prompt-starter-label">${starter.label}</span>
            </button>
          `)}
        </div>
        <p class="new-chat-context-hint">Type <kbd>@</kbd> to attach a dashboard, metric, model, page, or visual.</p>
      </div>
    `
  }

  private renderConversationView(agent: ChatSignal, status: ChatSignal['status'], composer: ChatSignal['composer']) {
    return html`
      <div class="thread-stack">
        <lv-agent-visual-library .agent=${agent} .dashboardSource=${this.dashboardVisualSource} @lv-visual-library-state=${this.handleVisualLibraryState}></lv-agent-visual-library>
        <lv-chat-thread .savedVisualIds=${this.visualLibraryState.savedIds} .savingVisualId=${this.visualLibraryState.savingId}
          .transcript=${this.displayTranscript(agent.transcript ?? [])}
          .pageArtifacts=${this.pageVisualLinks}
          .pageTitle=${this.dashboardPageTitle}
          .dashboardId=${this.projectedDashboardId}
          .visuals=${this.visuals ?? {}}
          .status=${status}
          .dashboardPreviewAvailable=${this.dashboardPreview || Boolean(this.savedBuilderHref)}
          .selectedVisualId=${this.dashboardPreview ? this.selectedPreviewArtifact?.id ?? '' : ''}
          surface=${this.builderOpen ? 'drawer' : 'page'}
          conversation-id=${agent.activeConversationId ?? ''}
          @lv-chat-reuse=${this.reuseDraft}
          @lv-chat-visual-open=${this.openVisual}
        >${status.error ?? ''}</lv-chat-thread>
        <div>
          ${this.dashboardDestination ? html`<div class="dashboard-destination"><span>Add visuals to <a href=${this.dashboardDestination.href}>${this.dashboardDestination.title}</a></span><button type="button" @click=${() => { this.dashboardDestination = undefined }}>Done</button></div>` : null}
          ${status.enabled ? this.renderComposer(composer, status) : null}
        </div>
      </div>
    `
  }

  private openVisual(event: CustomEvent<{ artifactId: string; explorerHref: string; title: string }>): void {
    const artifactId = event.detail?.artifactId ?? ''
    if (!artifactId || !this.visuals[artifactId]) return
    if (this.dashboardPreview) {
      void this.openDashboardPreview(new CustomEvent('lv-chat-dashboard-preview', { detail: { artifactId } }))
      return
    }
    this.dashboardPickerOpen = false
    this.selectedVisualID = artifactId
    this.selectedExplorerHref = event.detail.explorerHref ?? ''
    this.selectedVisualTitle = event.detail.title ?? ''
    this.visualSaved = false
    this.visualSaveError = ''
    void this.updateComplete.then(() => this.shadowRoot?.querySelector<ChatVisualPanel>('lv-chat-visual-panel')?.focusClose())
  }

  private closeVisual(restoreFocus: boolean): void {
    const artifactId = this.selectedVisualID
    this.dashboardPickerOpen = false
    if (!artifactId) return
    this.selectedVisualID = ''
    this.selectedExplorerHref = ''
    this.selectedVisualTitle = ''
    this.visualSaveError = ''
    if (restoreFocus) void this.updateComplete.then(() => {
      const cards = this.shadowRoot?.querySelector('lv-chat-thread')?.shadowRoot?.querySelectorAll<HTMLButtonElement>('.artifact-card')
      Array.from(cards ?? []).find(card => card.dataset.visualId === artifactId)?.focus()
    })
  }

  private closeDashboardPicker = (): void => {
    this.dashboardPickerOpen = false
    if (this.dashboardCopies[this.selectedVisualID]) {
      const artifactId = this.selectedVisualID
      this.closeVisual(false)
      void this.openDashboardPreview(new CustomEvent('lv-chat-dashboard-preview', { detail: { artifactId } }))
      return
    }
    void this.updateComplete.then(() => this.shadowRoot?.querySelector<ChatVisualPanel>('lv-chat-visual-panel')?.focusAddToDashboard())
  }

  private dashboardVisualAdded = (event: CustomEvent<ChatDashboardResult>): void => {
    const { componentId, pageId, href } = event.detail
    const builderHref = this.validBuilderHref(href)
    if (!componentId || !builderHref || !this.selectedVisualID) return
    this.savedBuilderHref = builderHref
    this.restoredBuilderHref = builderHref
    this.dashboardPageId = pageId
    const component: DashboardChatComponent = { id: componentId, pageId }
    this.dashboardComponents = [...this.dashboardComponents.filter(item => item.id !== componentId), component]
    this.rememberDashboardCopy(this.selectedVisualID, component)
    this.reconcileDashboardCopies()
    this.persistDashboardLocation()
  }

  private addAnotherVisual = (event: CustomEvent<ChatDashboardResult>): void => {
    this.dashboardDestination = event.detail
    this.closeVisual(false)
    void this.updateComplete.then(() => this.shadowRoot?.querySelector<HTMLElement & { focusInput(): void }>('lv-chat-composer')?.focusInput())
  }

  private saveVisual = async (event: CustomEvent<{ title: string; explorerHref: string; artifactId: string }>): Promise<void> => {
    if (this.visualSaving || !this.selectedVisualID || event.detail?.artifactId !== this.selectedVisualID) return
    const { title, explorerHref } = event.detail
    if (!explorerHref) return
    this.visualSaving = true
    this.visualSaveError = ''
    try {
      await saveChatVisual(title, explorerHref)
      if (event.detail.artifactId === this.selectedVisualID) this.visualSaved = true
    } catch {
      if (event.detail.artifactId === this.selectedVisualID) this.visualSaveError = 'Could not save this visual. Please try again.'
    } finally {
      this.visualSaving = false
    }
  }

  private renderComposer(composer: ChatSignal['composer'], status: ChatSignal['status'], hideContextAction = false) {
    return html`
      <lv-chat-composer
        ?hide-context-action=${hideContextAction}
        .value=${composer.value ?? ''}
        .disabled=${this.composerDisabled || status.running || composer.disabled}
        .pending=${this.pending || status.running}
        .running=${Boolean(status.running)}
        .runId=${status.runId ?? ''}
        .canContinue=${Boolean(status.canContinue)}
        .placeholder=${composer.placeholder ?? emptyAgent.composer.placeholder}
        .references=${this.references}
        .referenceLimit=${this.context?.referenceLimit ?? defaultAgentReferenceLimit}
        .suggestions=${this.referenceSearch.results ?? []}
        .suggestionQuery=${this.referenceSearch.query}
        .suggestionRequestId=${this.referenceSearch.requestId}
		.acceptedRunId=${latestAcceptedRunId(this.agent.transcript ?? [])}
		.editMessageId=${this.editMessageId}
		.editing=${Boolean(this.editMessageId)}
        @lv-chat-references-change=${this.referencesChanged}
		@lv-chat-edit-cancel=${this.cancelEdit}
      ></lv-chat-composer>
    `
  }

  private selectPromptStarter(prompt: string): void {
    this.shadowRoot?.querySelector<HTMLElement & { setDraft(value: string): void }>('lv-chat-composer')?.setDraft(prompt)
  }

	private showOptimisticTurn = (event: CustomEvent<{ input?: string; references?: AgentReferenceSignal[]; editMessageId?: string; surface?: string }>): void => {
    // Set intent on this exact submit event before the shell dispatches it.
    // Expanding chat must immediately stop automatic dashboard authoring.
    event.detail.surface = this.builderOpen ? 'builder' : 'chat'
		const conversationID = this.agent.activeConversationId?.trim() ?? ''
		const input = event.detail?.input?.trim() ?? ''
		if (!conversationID || !input) return
		const transcript = this.agent.transcript ?? []
		this.optimisticConversationID = conversationID
		this.optimisticEditMessageID = event.detail?.editMessageId?.trim() ?? ''
		this.optimisticBaselineMessageIDs = new Set(transcript.map(item => item.id?.trim()).filter((id): id is string => Boolean(id)))
		this.optimisticTurn = {
			id: `optimistic-${crypto.randomUUID()}`,
			kind: 'user',
			text: input,
			conversationId: conversationID,
			references: [...(event.detail?.references ?? [])],
			...(this.optimisticEditMessageID ? { edited: true } : {}),
		}
	}

	private displayTranscript(transcript: ChatTranscriptItemSignal[]): ChatTranscriptItemSignal[] {
		if (!this.optimisticTurn || this.optimisticConversationID !== (this.agent.activeConversationId?.trim() ?? '')) return transcript
		if (this.optimisticEditMessageID) {
			const target = transcript.findIndex(item => item.kind === 'user' && item.id?.trim() === this.optimisticEditMessageID)
			if (target >= 0) return [...transcript.slice(0, target), this.optimisticTurn]
		}
		return [...transcript, this.optimisticTurn]
	}

	private clearOptimisticTurn(): void {
		this.optimisticTurn = null
		this.optimisticConversationID = ''
		this.optimisticEditMessageID = ''
		this.optimisticBaselineMessageIDs = new Set()
	}

	private referencesChanged(event: CustomEvent<ChatReferencesChangeDetail>) {
		this.references = [...(event.detail.references ?? [])]
	}

	private reuseDraft(event: CustomEvent<ChatReuseDetail>): void {
		if (this.pending) return
		const detail = event.detail ?? { text: '', references: [] }
		const hasEditTarget = Object.prototype.hasOwnProperty.call(detail, 'editMessageId')
		if (hasEditTarget) {
			const editMessageId = typeof detail.editMessageId === 'string' ? detail.editMessageId.trim() : ''
			if (!editMessageId || !this.canEditMessage(editMessageId)) return
			this.editMessageId = editMessageId
		} else {
			this.clearEditMessage()
		}
		const references = mergeReferences(detail.references ?? [])
			.slice(0, normalizeReferenceLimit(this.context?.referenceLimit ?? defaultAgentReferenceLimit))
		this.references = references
		this.shadowRoot?.querySelector<HTMLElement & { setDraft(value: string): void }>('lv-chat-composer')?.setDraft(detail.text ?? '')
	}

	private canEditMessage(editMessageId: string): boolean {
		const conversationID = this.agent.activeConversationId?.trim()
		if (!conversationID) return false
		return (this.agent.transcript ?? []).some((item) =>
			item.kind === 'user'
			&& typeof item.id === 'string'
			&& item.id.trim() === editMessageId
			&& (!item.conversationId?.trim() || item.conversationId.trim() === conversationID),
		)
	}

	private clearEditMessage(): void {
		if (this.editMessageId) this.editMessageId = ''
	}

	private cancelEdit = (): void => {
		this.clearEditMessage()
	}
}

type ChatReuseDetail = {
	text: string
	references?: ChatTranscriptItemSignal['references']
	editMessageId?: string
}

function conversationTitle(agent: ChatSignal): string {
  const activeID = agent.activeConversationId?.trim()
  if (!activeID) return 'New chat'
  const active = (agent.conversations ?? []).find((conversation: ChatConversationSummary) => conversation.id === activeID)
  const title = active?.title?.trim()
  return title || 'New chat'
}

if (!customElements.get('lv-chat-page')) customElements.define('lv-chat-page', LeapViewChatPage)
