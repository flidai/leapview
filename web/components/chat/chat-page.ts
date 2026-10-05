import { loadDatastarRuntime } from '../shared/datastar-runtime'
import { savedVisualComponentId, savedVisualSourceId } from './dashboard-membership'
import { submitVisualForm } from './visual-library-bridge'
import './agent-visual-library'
import type { VisualLibraryState } from './agent-visual-library'
import { LitElement, css, html } from 'lit'
import { visualizationRegistry } from '../dashboard/visualization/registry'
import { repeat } from 'lit/directives/repeat.js'
import { ifDefined } from 'lit/directives/if-defined.js'
import { state } from 'lit/decorators.js'
import { Check, CircleHelp, Grid2X2, LayoutDashboard, Maximize2, Minimize2, Minus, Plus, Save, TrendingUp, X, type IconNode } from 'lucide'
import type { ChatArtifactSignal, AgentContextSignal, AgentReferenceSearchSignal, AgentReferenceSignal, ChatConversationSummary, ChatPageSignal, ChatSignal, ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { lucideIcon } from '../shared/lucide-icons'
import { uuidv7 } from '../shared/command'
import '../dashboard/visual-modal'
import './chat-thread'
import { agentIcon } from './agent-icon'
import { type ChatReferencesChangeDetail, defaultAgentReferenceLimit, latestAcceptedRunId, mergeReferences, normalizeReferenceLimit } from './reference'
import './chat-composer'
import './chat-list'
import type { ChatDashboardMessage, DashboardChatComponent } from './dashboard-workspace'
import { chatVisualsFromSignals } from './visual-signals'

const emptyAgent: ChatSignal = {
  conversations: [],
  activeConversationId: '',
  transcript: [],
  status: { enabled: false, running: false },
  composer: { value: '', disabled: true, placeholder: 'Agent is not configured.' },
}

const promptStarters: Array<{ label: string; prompt: string; icon: IconNode }> = [
  { label: 'Spot a change', prompt: 'What changed most in the last 30 days?', icon: TrendingUp },
  { label: 'Explain a metric', prompt: 'Explain how revenue is calculated.', icon: CircleHelp },
  { label: 'Review a dashboard', prompt: 'Summarize the Executive Sales dashboard.', icon: LayoutDashboard },
]

class LeapViewChatPage extends DatastarLit(LitElement) {
  private redirectedConversationID = ''
  @state() private visualLibraryState: VisualLibraryState = { savedIds: [], savingId: '', error: '' }
  @state() private dashboardPreview = false
  @state() private selectedPreviewVisual = ''
  @state() private builderOpen = false
  @state() private canArrangeDashboard = false
  @state() private fixingDashboardVisuals = false
  @state() private fixVisualsMessage = ''
  @state() private savingDashboard = false
  @state() private dashboardSaveError = ''
  @state() private savedBuilderHref = ''
  @state() private restoredBuilderHref: string | undefined
  @state() private savedDashboardArtifacts: ChatArtifactSignal[] = []
  @state() private savedDashboardVisuals: Record<string, VisualizationEnvelope> = {}
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
    this.sidebarScroll = this.shadowRoot?.querySelector<HTMLElement>('.preview-scroll')?.scrollTop ?? 0
    this.chatScroll = this.chatThread?.captureScroll() ?? null
    this.dashboardPreview = true
    this.builderOpen = true
    if (this.savedBuilderHref && this.builderNeedsRefresh && this.builderFrame) {
      this.builderFrame.src = this.savedBuilderHref
      this.builderNeedsRefresh = false
    }
    const url = new URL(window.location.href)
    url.searchParams.set('preview', 'builder')
    window.history.pushState(window.history.state, '', url)
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
      if (this.pendingDashboardPageId === event.data.pageId) this.pendingDashboardPageId = ''
      this.canArrangeDashboard = event.data.canArrange === true
      if (event.data.fixingVisuals !== undefined) this.fixingDashboardVisuals = event.data.fixingVisuals
      if (event.data.fixMessage !== undefined) this.fixVisualsMessage = event.data.fixMessage
      this.savedDashboardArtifacts = event.data.artifacts
      this.savedDashboardVisuals = event.data.visuals
    }
    const reference = event.data.reference
    this.references = mergeReferences([reference], this.references.filter(item => item.reference.kind !== reference.reference.kind || item.reference.id !== reference.reference.id))
    if (event.data.type === 'lv-builder-saved') void this.syncDashboardContext(reference.reference.id, event.data.pageId, this.dashboardPageTitle, event.data.modelId ?? '')
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
    this.reconcileDashboardCopies()
  }

  private arrangeDashboard = (): void => {
    if (!this.builderOpen || !this.canArrangeDashboard) return
    this.canArrangeDashboard = false
    this.fixingDashboardVisuals = true
    this.fixVisualsMessage = ''
    this.builderFrame?.contentWindow?.postMessage({ type: 'lv-arrange-dashboard-visuals' } satisfies ChatDashboardMessage, window.location.origin)
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
    if (this.savingDashboard || this.builderUpdating || this.pendingDashboardPageId || this.dashboardCopies[event.detail.artifactId]) return
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
    if (this.savingDashboard || this.builderUpdating || this.pendingDashboardPageId) return
    const copy = this.dashboardCopies[artifactId]
    if (!copy) {
      this.savePreviewVisual(artifactId, true)
      return
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
    this.syncPreviewLocation()
    window.addEventListener('popstate', this.syncPreviewLocation)
    window.addEventListener('message', this.handleBuilderMessage)
    document.addEventListener('datastar-signal-patch', this.invalidateVisualCache)
  }

  disconnectedCallback(): void {
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
      const workspace = window.history.state?.chatDashboard
      if (workspace?.href === href) {
        this.dashboardCopyLinks = workspace.links ?? {}
        this.savedSignature = workspace.signature ?? ''
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
    const url = new URL(window.location.href)
    url.searchParams.set('dashboard', href)
    // Store only identity links in browser history. The iframe always loads the
    // latest authorized draft from the server, never a cached dashboard snapshot.
    window.history.replaceState({ ...window.history.state, chatDashboard: {
      href, links: this.dashboardCopyLinks, signature: this.savedSignature,
    } }, '', url)
  }

  private async openDashboardPreview(event?: CustomEvent<{ artifactId?: string }>): Promise<void> {
    const fromBuilder = this.builderOpen
    const position = fromBuilder ? this.chatScroll : this.chatThread?.captureScroll()
    this.builderOpen = false
    this.selectedPreviewVisual = event?.detail?.artifactId ?? this.selectedPreviewArtifact?.id ?? ''
    this.visitedVisuals = [...this.visitedVisuals.filter(id => id !== this.selectedPreviewVisual), this.selectedPreviewVisual].slice(-3)
    if (!this.dashboardPreview || fromBuilder) {
      const url = new URL(window.location.href)
      url.searchParams.set('preview', 'dashboard')
      if (fromBuilder) window.history.replaceState(window.history.state, '', url)
      else window.history.pushState(window.history.state, '', url)
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
    const url = new URL(window.location.href)
    url.searchParams.set('preview', 'dashboard')
    window.history.replaceState(window.history.state, '', url)
    await this.restoreChatLayout()
  }

  private async closeVisualSidebar(): Promise<void> {
    const position = this.chatThread?.captureScroll()
    this.dashboardPreview = false
    const url = new URL(window.location.href)
    url.searchParams.delete('preview')
    window.history.replaceState(window.history.state, '', url)
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
    for (const artifact of this.savedDashboardArtifacts) artifacts.set(artifact.id, artifact)
    return [...artifacts.values()]
  }

  private get pageVisualLinks(): ChatArtifactSignal[] {
    const conversationIDs = new Set((this.agent.transcript ?? []).flatMap(item => item.artifact ? [item.artifact.id] : []))
    return this.savedDashboardArtifacts.filter(artifact => {
      const component = this.dashboardCopies[artifact.id]
      return !component || !Object.entries(this.dashboardCopies).some(([id, copy]) => conversationIDs.has(id) && copy.id === component.id && copy.pageId === component.pageId)
    })
  }

  @state() private references: AgentReferenceSignal[] = []
	@state() private editMessageId = ''
	@state() private optimisticTurn: ChatTranscriptItemSignal | null = null
	private optimisticConversationID = ''
	private optimisticEditMessageID = ''
	private optimisticBaselineMessageIDs = new Set<string>()
	private trackedConversationID: string | null = null
	private trackedAcceptedRunID: string | null = null

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      background: var(--lv-bg-app);
    }

    .route {
      display: block;
      min-height: 100svh;
      background: var(--lv-bg-app);
    }

    .main {
      display: grid;
      min-width: 0;
      height: 100svh;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
      background: var(--lv-bg-app);
    }

    .workspace {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    .workspace { grid-template-areas: 'chat'; }
    .body { grid-area: chat; }
    .workspace.preview-open {
      grid-template-columns: minmax(300px, .9fr) minmax(0, 1.1fr);
      grid-template-areas: 'chat visuals';
    }
    .preview-open .thread-stack { --lv-chat-stack-width: 100%; }
    .preview-panel {
      grid-area: visuals; display: grid; grid-template-rows: auto minmax(0, 1fr);
      min-width: 0; min-height: 0; overflow: hidden; border-left: var(--lv-border-default);
      background: var(--lv-bg-panel);
    }
    .preview-scroll { overflow: auto; min-height: 0; padding: 16px; overscroll-behavior: contain; }
    .preview-panel:has(.dashboard-destination) { grid-template-rows: auto auto minmax(0, 1fr); }
    .preview-panel[hidden], .builder-stage[hidden], .builder-frame[hidden] { display: none; }
    .builder-stage { grid-area: builder; min-width: 0; min-height: 0; position: relative; }
    .builder-frame { display: block; width: 100%; height: 100%; border: 0; }
    .preview-actions { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; flex-shrink: 0; }
    .save-error { padding: 12px; color: var(--lv-fg-danger); font: var(--lv-type-body-compact); }
    .preview-action:disabled { opacity: .6; cursor: default; }
    .workspace.builder-open { grid-template-columns: minmax(0, 1fr) clamp(280px, 24vw, 320px); grid-template-areas: 'builder chat'; }
    .workspace.builder-open .body {
      --lv-type-body: 400 14px/1.5 var(--fontStack-system);
      --lv-chat-stack-gap: 24px;
    }
    .builder-open .body { border-left: var(--lv-border-default); }
    .preview-heading { flex-wrap: wrap; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 16px; border-bottom: var(--lv-border-default); }
    .preview-heading h2 { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin: 0; font: var(--lv-type-section-title); }
    .preview-empty { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .preview-grid { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
    .preview-card[hidden] { display: none; }
    .preview-card { min-width: 0; border-radius: var(--lv-radius-default); outline: none; scroll-margin: 6px; }
    .preview-card:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 3px; }
    .preview-card lv-visual-artifact { height: clamp(300px, 44svh, 480px); }
    .preview-card.kpi lv-visual-artifact { height: 180px; }
    .preview-card.wide lv-visual-artifact { height: clamp(340px, 52svh, 560px); }
    .preview-action {
      display: inline-flex; align-items: center; justify-content: center; gap: 8px;
      padding: 7px 12px; min-height: 34px; border: var(--lv-border-default);
      border-radius: var(--lv-radius-default); background: var(--lv-bg-panel);
      color: var(--lv-fg-default); font: var(--lv-type-body-compact); cursor: pointer;
    }
    .preview-action:hover { background: var(--lv-bg-control-hover); }
    .preview-action:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 2px; }
    .preview-action svg { width: 16px; height: 16px; }
    .close-visuals { padding: 7px; }
    .titlebar-start { display: flex; min-width: 0; align-items: center; gap: 16px; }
    .titlebar-start h1 { min-width: 0; }

    @media (max-width: 900px) {
      .workspace.preview-open { grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(220px, 42%) minmax(0, 1fr); grid-template-areas: 'chat' 'visuals'; }
      .workspace.builder-open { grid-template-rows: minmax(0, 1fr) minmax(220px, 38%); grid-template-areas: 'builder' 'chat'; }
      .preview-panel, .builder-open .body { border-left: 0; border-top: var(--lv-border-default); }
      .preview-scroll { padding: 12px; }
      .titlebar-start { flex-wrap: wrap; gap: 8px; }
      .preview-heading { padding: 10px 12px; }
    }

    .main.list-main {
      height: auto;
      min-height: 100svh;
      grid-template-rows: minmax(0, 1fr);
      overflow: visible;
    }

    .main.new-main {
      grid-template-rows: minmax(0, 1fr);
    }

    .main.builder-main { grid-template-rows: minmax(0, 1fr); }

    .loading-state {
      display: grid;
      place-items: center;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .conversation-titlebar {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      gap: 16px;
      padding: 14px var(--base-size-16) var(--base-size-8);
    }

    h1 {
      margin: 0;
    }

    h1 {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
    }

    .body {
      display: grid;
      grid-template-rows: minmax(0, 1fr);
      min-width: 0;
      min-height: 0;
      overflow: auto;
      background: var(--lv-bg-app);
    }

    .body.with-chat-header { grid-template-rows: auto minmax(0, 1fr); }
    .fix-result { grid-column: 1 / -1; margin: 0; font-size: 12px; line-height: 1.4; color: var(--lv-fg-muted); }
    .chat-pane-header {
      display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center;
      gap: 6px; min-height: 50px; padding: 0 12px; border-bottom: var(--lv-border-default);
    }
    .chat-pane-header[hidden] { display: none; }
    .chat-pane-heading { display: flex; min-width: 0; align-items: center; gap: 6px; white-space: nowrap; font: var(--lv-type-body-compact); }
    .chat-page-name { overflow: hidden; text-overflow: ellipsis; }
    .chat-pane-heading svg { flex-shrink: 0; }
    .dashboard-destination { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-bottom: var(--lv-border-default); font: var(--lv-type-body-compact); }
    .dashboard-destination select { min-width: 0; max-width: 240px; padding: 5px 8px; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); }
    .arrange-dashboard { display: inline-flex; align-items: center; gap: 5px; padding: 6px 8px; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); font: var(--lv-type-body-compact); font-size: 12px; white-space: nowrap; cursor: pointer; }
    .arrange-dashboard svg { width: 14px; height: 14px; }
    .arrange-dashboard:disabled { opacity: .6; cursor: default; }
    .chat-pane-heading svg { width: 16px; height: 16px; }
    .titlebar-actions { display: flex; align-items: center; gap: 8px; }
    .chat-size-toggle {
      display: inline-flex; align-items: center; justify-content: center;
      width: 32px; height: 32px; flex-shrink: 0; padding: 6px;
      border: 0; border-radius: var(--lv-radius-default); background: transparent;
      color: var(--lv-fg-muted); cursor: pointer;
    }
    .chat-size-toggle svg { width: 16px; height: 16px; }
    .chat-size-toggle:hover { background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
    .chat-size-toggle:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 2px; }
    .chat-size-toggle:disabled { opacity: .6; cursor: default; }

    .list-main .workspace { overflow: visible; }

    .list-main .body {
      min-height: auto;
      overflow: visible;
    }

    .thread-stack {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr) auto;
      overflow: hidden;
      background: var(--lv-bg-app);
    }

    .new-chat-stage {
      box-sizing: border-box;
      display: flex;
      min-width: 0;
      min-height: 100%;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: var(--lv-space-md);
      overflow-y: auto;
      padding: calc(var(--lv-space-lg) * 3) 0 var(--lv-space-lg);
      background: var(--lv-bg-app);
    }

    .new-chat-stage > * {
      animation: new-chat-enter var(--lv-transition-medium) both;
    }

    .new-chat-stage lv-chat-composer {
      width: 100%;
      animation-delay: 70ms;
    }

    .new-chat-intro {
      box-sizing: border-box;
      width: min(100%, var(--lv-chat-stack-width));
      padding-inline: var(--lv-space-lg);
    }

    .new-chat-heading {
      display: flex;
      align-items: center;
      justify-content: center;
      gap: var(--lv-space-sm);
      text-align: center;
    }

    .agent-mark {
      display: grid;
      width: var(--base-size-24);
      height: var(--base-size-24);
      flex: 0 0 var(--base-size-24);
      place-items: center;
      color: var(--lv-accent);
    }

    .agent-mark svg {
      width: var(--base-size-20);
      height: var(--base-size-20);
    }

    .new-chat-title {
      max-width: 100%;
      font: var(--lv-type-page-title);
    }

    .prompt-starters {
      box-sizing: border-box;
      display: flex;
      width: min(100%, var(--lv-chat-stack-width));
      flex-wrap: wrap;
      justify-content: center;
      gap: var(--lv-space-sm);
      padding-inline: var(--lv-space-lg);
    }

    .prompt-starter {
      display: inline-flex;
      min-height: var(--lv-control-medium);
      align-items: center;
      gap: var(--lv-space-xs);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      padding: 0 var(--lv-space-md);
      cursor: pointer;
      transition:
        background var(--lv-transition-fast),
        border-color var(--lv-transition-fast);
    }

    .prompt-starter:hover:not(:disabled) {
      border-color: var(--lv-line-accent-muted);
      background: var(--lv-bg-control-hover);
    }

    .prompt-starter:focus-visible {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent);
      outline-offset: var(--lv-space-2xs);
    }

    .prompt-starter:disabled {
      color: var(--lv-fg-muted);
      cursor: not-allowed;
      opacity: 0.65;
    }

    .prompt-starter-icon {
      display: grid;
      width: var(--base-size-16);
      height: var(--base-size-16);
      place-items: center;
      color: var(--lv-accent);
    }

    .prompt-starter-icon svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .prompt-starter-label {
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
    }

    .new-chat-context-hint {
      margin: 0;
      padding-inline: var(--lv-space-lg);
      color: var(--lv-fg-muted);
      text-align: center;
      font: var(--lv-type-caption);
    }

    .new-chat-context-hint kbd {
      display: inline-grid;
      min-width: 20px;
      height: 20px;
      place-items: center;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-tight);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      font: inherit;
    }

    @keyframes new-chat-enter {
      from {
        opacity: 0;
        transform: translateY(var(--lv-space-sm));
      }

      to {
        opacity: 1;
        transform: translateY(0);
      }
    }

    @media (prefers-reduced-motion: reduce) {
      .new-chat-stage > * {
        animation: none;
      }
    }

    lv-chat-thread {
      display: block;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    lv-chat-composer {
      display: block;
      background: var(--lv-bg-app);
    }

    @media (max-width: 768px) {
      .route {
        grid-template-columns: 1fr;
      }

      .main.new-main {
        height: 100svh;
      }

      .new-chat-stage {
        justify-content: flex-start;
        padding-top: calc(var(--lv-space-lg) * 2);
      }

      .prompt-starters { gap: var(--lv-space-xs); }
    }
  `

  updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('builderOpen')) {
      this.dispatchEvent(new CustomEvent('lv-chat-workspace-change', {
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
    this.navigateFromDraft()
    const running = Boolean(this.agent.status.running)
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
    return { ...this.visualCache, ...this.savedDashboardVisuals }
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
    return this.pending || Boolean(agent.status?.running) || Boolean(agent.composer?.disabled)
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
    return html`
      <div class="route" @lv-add-agent-visual=${this.addAgentVisual} @lv-saved-visuals-changed=${this.refreshSavedVisuals} @lv-chat-submit=${this.showOptimisticTurn} @lv-chat-dashboard-preview=${this.openDashboardPreview}>
        <iframe class="mutation-frame" name=${this.mutationFrameName} title="Dashboard update" hidden @load=${this.handleMutationLoad}></iframe>
        <section class=${['main', isList ? 'list-main' : '', isNew ? 'new-main' : '', this.builderOpen ? 'builder-main' : ''].filter(Boolean).join(' ')} aria-label="LeapView chats">
          ${isList || isNew || this.builderOpen ? null : this.renderConversationTitlebar(title)}
          <div class=${`workspace${this.dashboardPreview && !isList && !isNew ? ` preview-open${this.builderOpen ? ' builder-open' : ''}` : ''}`}>
            ${this.renderDashboardPreview(title, !isList && !isNew && this.dashboardPreview && !this.builderOpen)}
            <section class="builder-stage" aria-label="Dashboard builder workspace" ?hidden=${!this.dashboardPreview || !this.builderOpen}>
              ${this.dashboardSaveError ? html`<p class="save-error" role="alert">${this.dashboardSaveError}</p>` : null}
              <iframe class="builder-frame" name=${this.builderFrameName} title="Dashboard builder" src=${ifDefined(this.restoredBuilderHref)} @load=${this.handleBuilderLoad} ?hidden=${Boolean(this.dashboardSaveError)}></iframe>
            </section>
            <div class=${`body${this.builderOpen ? ' with-chat-header' : ''}`}>
              <div class="chat-pane-header" ?hidden=${!this.builderOpen}>
                <span class="chat-pane-heading" title=${this.dashboardPageTitle ? `Chat · ${this.dashboardPageTitle}` : 'Chat'}>${agentIcon()}<span>Chat</span>${this.dashboardPageTitle ? html`<span class="chat-page-name"> · ${this.dashboardPageTitle}</span>` : null}</span>
                <div class="titlebar-actions">
                  <button class="arrange-dashboard" type="button" aria-label="Fix view visuals" aria-busy=${this.fixingDashboardVisuals} title="Complete missing chart fields while keeping your positions and sizes" ?disabled=${!this.canArrangeDashboard || this.fixingDashboardVisuals} @click=${this.arrangeDashboard}>${lucideIcon(Grid2X2)} ${this.fixingDashboardVisuals ? 'Fixing…' : 'Fix view visuals'}</button>
                  <button class="chat-size-toggle" type="button" aria-label="Expand chat" title="Expand chat" @click=${this.closeDashboardPreview}>${lucideIcon(Maximize2)}</button>
                </div>
                ${this.fixVisualsMessage ? html`<p class="fix-result" role="status">${this.fixVisualsMessage}</p>` : null}
              </div>
              ${isList ? this.renderListView(agent) : isNew ? this.renderNewView(composer, status) : this.renderConversationView(agent, status, composer)}
            </div>
          </div>
          <lv-visual-modal></lv-visual-modal>
        </section>
      </div>
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
          ${this.previewArtifacts.length ? html`<button class="chat-size-toggle" type="button" aria-label="Shrink chat" title="Shrink chat" ?disabled=${this.savingDashboard} @click=${() => this.saveDashboard(true)}>${lucideIcon(Minimize2)}</button>` : null}
        </div>
      </div>
    `
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
      <section class="preview-panel" aria-label="Dashboard preview" ?hidden=${!visible}>
        <div class="preview-heading">
          <h2>Visual</h2>
          <div class="preview-actions">
            ${canSave ? html`<button class="preview-action" type="button" ?disabled=${!selected || saving} aria-pressed=${saved} title=${saved ? 'Unsave visual' : 'Save visual'} @click=${() => selected && this.savePreviewVisual(selected.id, false)}>${lucideIcon(saved ? Check : Save)} ${selected && this.visualLibraryState.savingId === selected.id ? 'Updating…' : saved ? 'Saved' : 'Unsaved'}</button>` : null}
            <button class="preview-action" type="button" ?disabled=${!selected || saving || this.savingDashboard || this.builderUpdating || Boolean(this.pendingDashboardPageId) || Boolean(this.savedBuilderHref && !this.dashboardPageId)} aria-pressed=${added} @click=${() => selected && this.toggleDashboardVisual(selected.id)}>${lucideIcon(added ? Minus : Plus)} ${this.savingDashboard ? 'Updating…' : added ? 'Remove from dashboard' : 'Add to dashboard'}</button>
            <button class="preview-action preview-builder-action" type="button" aria-label="View in Dashboard Preview" title="View in Dashboard Preview" ?disabled=${this.savingDashboard || !artifacts.length} @click=${() => this.saveDashboard(true)}>${lucideIcon(LayoutDashboard)} Preview</button>
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
            return html`
              <div class=${`preview-card${kind === 'kpi' ? ' kpi' : ''}${['table', 'matrix', 'pivot'].includes(kind) ? ' wide' : ''}${this.selectedPreviewVisual === artifact.id ? ' selected' : ''}`}
                ?hidden=${artifact.id !== selected?.id} data-preview-visual=${artifact.id} tabindex="-1" aria-label=${payload?.spec.title || artifact.summary || 'Visual'}>
                <lv-visual-artifact eager type=${artifact.type} artifact-id=${artifact.id} .payload=${payload}></lv-visual-artifact>
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
        <lv-agent-visual-library .agent=${agent} @lv-visual-library-state=${this.handleVisualLibraryState}></lv-agent-visual-library>
        <lv-chat-thread .savedVisualIds=${this.visualLibraryState.savedIds} .savingVisualId=${this.visualLibraryState.savingId}
          .transcript=${this.displayTranscript(agent.transcript ?? [])}
          .pageArtifacts=${this.pageVisualLinks}
          .pageTitle=${this.dashboardPageTitle}
          .visuals=${this.visuals ?? {}}
          .status=${status}
          .dashboardPreviewAvailable=${true}
          .selectedVisualId=${this.dashboardPreview ? this.selectedPreviewArtifact?.id ?? '' : ''}
          surface=${this.builderOpen ? 'drawer' : 'page'}
          conversation-id=${agent.activeConversationId ?? ''}
          @lv-chat-reuse=${this.reuseDraft}
        >${status.error ?? ''}</lv-chat-thread>
        ${status.enabled ? this.renderComposer(composer, status) : null}
      </div>
    `
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

	private showOptimisticTurn = (event: CustomEvent<{ input?: string; references?: AgentReferenceSignal[]; editMessageId?: string }>): void => {
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
