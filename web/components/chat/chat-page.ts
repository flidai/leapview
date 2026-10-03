import { LitElement, html } from 'lit'
import { chatPageStyles } from './chat-page.styles'
import { state } from 'lit/decorators.js'
import { CircleHelp, LayoutDashboard, TrendingUp, X, type IconNode } from 'lucide'
import type { AgentContextSignal, AgentReferenceSearchSignal, AgentReferenceSignal, ChatConversationSummary, ChatDashboardDraftSignal, ChatPageSignal, ChatSignal, ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { lucideIcon } from '../shared/lucide-icons'
import '../dashboard/visual-modal'
import './chat-thread'
import { agentIcon } from './agent-icon'
import { type ChatReferencesChangeDetail, defaultAgentReferenceLimit, latestAcceptedRunId, mergeReferences, normalizeReferenceLimit } from './reference'
import './chat-composer'
import './chat-list'
import './chat-visual-panel'
import type { ChatVisualPanel } from './chat-visual-panel'
import { saveChatVisual } from './saved-visuals'
import './chat-dashboard-picker'
import type { ChatDashboardResult } from './chat-dashboard-api'
import './chat-dashboard-draft'
import { chatReturnHref, fullChatHref, readFullChatReturn, takeFullChatReturn, returnFromFullChat, updateChatReturnConversation } from './chat-navigation'

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
  @state() private selectedVisualID = ''
  @state() private selectedExplorerHref = ''
  @state() private selectedVisualTitle = ''
  @state() private visualSaving = false
  @state() private visualSaved = false
  @state() private visualSaveError = ''
  @state() private dashboardPickerOpen = false
  @state() private dashboardDestination?: ChatDashboardResult
  @state() private dashboardPreviewOpen = false
  @state() private dashboardWorkspace = false
  @state() private selectedDraftVisualID = ''
  @state() private previewArtifactID = ''
  private requestedPreviewID = ''
  private previewQueryRevision = ''
  private previewConversationID = ''
  private previewEditing = false
  private restoredExpandedDraft = false
  private trackedDashboardDraftRevision = ''
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

  static styles = chatPageStyles

  connectedCallback(): void {
    super.connectedCallback()
    if (typeof window === 'undefined') return
    this.requestedPreviewID = new URLSearchParams(window.location.search).get('preview')?.slice(0, 256) ?? ''
    this.compactMedia = window.matchMedia('(max-width: 768px)')
    this.compactViewport = this.compactMedia.matches
    this.compactMedia.addEventListener('change', this.onCompactViewportChange)
  }

  disconnectedCallback(): void {
    this.compactMedia?.removeEventListener('change', this.onCompactViewportChange)
    this.compactMedia = undefined
    super.disconnectedCallback()
  }

  private onCompactViewportChange = (event: MediaQueryListEvent): void => {
    this.compactViewport = event.matches
  }

  protected willUpdate(): void {
    if (!this.hasBootstrapSignals) return
    const dashboardDraftRevision = this.dashboardDraft?.revision?.trim() ?? ''
    if (dashboardDraftRevision && dashboardDraftRevision !== this.trackedDashboardDraftRevision) {
      if (!this.requestedPreviewID) {
        if (this.selectedVisualID) this.closeVisual(false)
        this.previewArtifactID = ''
        this.clearPreviewURL()
      }
      this.dashboardPreviewOpen = true
      if (!this.dashboardDraft?.visuals.some((visual) => visual.id === this.selectedDraftVisualID)) {
        this.selectedDraftVisualID = this.dashboardDraft?.visuals[0]?.id ?? ''
      }
    } else if (!dashboardDraftRevision && this.trackedDashboardDraftRevision) {
      this.dashboardPreviewOpen = false
      this.selectedDraftVisualID = ''
    }
    this.trackedDashboardDraftRevision = dashboardDraftRevision
    this.syncVisualPreview()
    if (this.selectedVisualID && !this.visuals[this.selectedVisualID]) this.closeVisual(false)
  }

  updated(): void {
    if (!this.hasBootstrapSignals) return
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
    this.restoreExpandedDraft()
    this.navigateFromDraft()
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
      this.dashboardWorkspace = false
      this.previewArtifactID = ''
      this.requestedPreviewID = ''
      this.dashboardPreviewOpen = false
      this.selectedDraftVisualID = ''
      this.trackedDashboardDraftRevision = ''
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

  private restoreExpandedDraft(): void {
    if (this.restoredExpandedDraft) return
    const saved = readFullChatReturn()
    if (!saved || (this.agent.activeConversationId ?? '') !== saved.conversationId) return
    if (saved.conversationId && !this.agent.transcript?.length) return
    this.restoredExpandedDraft = true
    const initial = takeFullChatReturn()
    if (!initial) return
    this.references = initial.references
    this.editMessageId = initial.editMessageId && this.canEditMessage(initial.editMessageId) ? initial.editMessageId : ''
    void this.updateComplete.then(async () => {
      const composer = this.shadowRoot?.querySelector<HTMLElement & { updateComplete: Promise<unknown>; setDraft(value: string, focus?: boolean): void }>('lv-chat-composer')
      await composer?.updateComplete
      composer?.setDraft(initial.draft, false)
    })
  }

  private navigateFromDraft(): void {
    const conversationID = this.agent.activeConversationId?.trim()
    if (this.page?.view !== 'new' || !conversationID || conversationID === this.redirectedConversationID) return
    this.redirectedConversationID = conversationID
    updateChatReturnConversation(conversationID)
    window.location.replace(fullChatHref(conversationID))
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

  get visuals(): Record<string, VisualizationEnvelope> {
    return this.signal<Record<string, VisualizationEnvelope>>('visuals', {})
  }

  get dashboardDraft(): ChatDashboardDraftSignal | undefined {
    return this.agent.dashboardDraft
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
    const selectedVisual = this.selectedVisualID ? this.visuals[this.selectedVisualID] : undefined
    const dashboardDraft = this.previewDraft
    const draftContainsSelectedVisual = Boolean(this.dashboardDraft?.visuals.some((visual) => visual.artifactId === this.selectedVisualID))
    const showVisualPanel = !this.dashboardPreviewOpen && !this.previewEditing && Boolean(selectedVisual && !isList && !isNew && (!this.dashboardDraft || !draftContainsSelectedVisual))
    const showDashboardDraft = Boolean(dashboardDraft && !showVisualPanel) && !isList && !isNew && this.dashboardPreviewOpen
    return html`
      <div class=${showDashboardDraft ? this.dashboardWorkspace ? 'route dashboard-open dashboard-workspace' : 'route dashboard-open' : showVisualPanel ? 'route visual-open' : 'route'} @lv-chat-submit=${this.preparePreviewTurn}>
        <section class=${['main', isList ? 'list-main' : '', isNew ? chatReturnHref() ? 'new-main with-return' : 'new-main' : ''].filter(Boolean).join(' ')} aria-label="LeapView chats" ?inert=${Boolean((showDashboardDraft || showVisualPanel) && this.compactViewport)}>
          ${isList || isNew && !chatReturnHref() ? null : this.renderConversationTitlebar(isNew ? 'New chat' : title, Boolean(dashboardDraft && !this.dashboardPreviewOpen))}
          <div class="body">
            ${isList ? this.renderListView(agent) : isNew ? this.renderNewView(composer, status) : this.renderConversationView(agent, status, composer)}
          </div>
          <lv-visual-modal></lv-visual-modal>
        </section>
        ${showDashboardDraft && dashboardDraft ? html`
          <lv-chat-dashboard-draft
            .draft=${dashboardDraft}
            .visuals=${this.visuals}
            .selectedVisualId=${this.selectedDraftVisualID}
            .busy=${this.pending}
            .sourceArtifactId=${this.previewArtifactID}
            .workspace=${this.dashboardWorkspace}
            @lv-chat-dashboard-preview=${this.enterDashboardWorkspace}
            @lv-chat-dashboard-save-visual=${() => { this.dashboardPickerOpen = true }}
            conversation-id=${agent.activeConversationId ?? ''}
            .modal=${this.compactViewport}
            @lv-chat-dashboard-close=${this.closeDashboardPreview}
            @lv-chat-dashboard-edit=${this.editDashboardVisual}
          ></lv-chat-dashboard-draft>
        ` : null}
        ${showVisualPanel && selectedVisual ? html`
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
            @lv-chat-visual-preview=${this.previewSelectedVisual}
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
          @lv-chat-dashboard-close=${this.closeDashboardPicker}
          @lv-chat-dashboard-add-another=${this.addAnotherVisual}
        ></lv-chat-dashboard-picker>
      ` : null}
    `
  }

  private renderConversationTitlebar(title: string, showDraftToggle = false) {
    return html`
      <div class="conversation-titlebar">
        <h1>${this.dashboardWorkspace ? 'Agent' : title}</h1>
        ${chatReturnHref() ? html`<a class="return-chat" href=${chatReturnHref()!} aria-label="Return to page" title="Return to page" @click=${(event: MouseEvent) => { event.preventDefault(); returnFromFullChat() }}>${lucideIcon(X, { size: 16 })}</a>` : null}
        ${showDraftToggle && this.previewDraft ? html`
          <button class="mobile-dashboard-toggle" type="button" @click=${this.openDashboardPreview}>
            <span>Dashboard draft</span><span class="mobile-dashboard-count">${this.previewDraft!.visuals.length}</span>
          </button>
        ` : null}
      </div>
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
        <lv-chat-thread
          .transcript=${this.displayTranscript(agent.transcript ?? [])}
          .visuals=${this.visuals ?? {}}
          .status=${status}
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
    this.previewEditing = false
    this.dashboardWorkspace = false
    this.previewArtifactID = ''
    this.dashboardPreviewOpen = false
    this.clearPreviewURL()
    const draftVisual = this.dashboardDraft?.visuals.find((visual) => visual.artifactId === artifactId)
    if (draftVisual) {
      this.selectedDraftVisualID = draftVisual.id
      this.dashboardPreviewOpen = true
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
    this.previewArtifactID = ''
    this.previewEditing = false
    this.clearPreviewURL()
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
    void this.updateComplete.then(() => {
      const draft = this.shadowRoot?.querySelector<HTMLElement & { focusSave(): void }>('lv-chat-dashboard-draft')
      if (draft) draft.focusSave()
      else this.shadowRoot?.querySelector<ChatVisualPanel>('lv-chat-visual-panel')?.focusPreview()
    })
  }

  private addAnotherVisual = (event: CustomEvent<ChatDashboardResult>): void => {
    this.dashboardDestination = event.detail
    this.dashboardWorkspace = false
    this.dashboardPreviewOpen = false
    this.previewArtifactID = ''
    this.requestedPreviewID = ''
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

  private get previewDraft(): ChatDashboardDraftSignal | undefined {
    const payload = this.previewArtifactID ? this.visuals[this.previewArtifactID] : undefined
    if (!payload) return this.dashboardDraft
    const title = payload.spec.title?.trim() || this.selectedVisualTitle || 'Visual preview'
    return { revision: `visual:${this.previewArtifactID}`, title, visuals: [{ id: this.previewArtifactID, artifactId: this.previewArtifactID, title }] }
  }

  private previewSelectedVisual = (): void => {
    if (this.pending || !this.selectedVisualID || !this.isPreviewableArtifact(this.selectedVisualID)) return
    this.previewArtifactID = this.selectedVisualID
    this.dashboardWorkspace = true
    this.previewEditing = false
    this.previewConversationID = this.agent.activeConversationId
    this.previewQueryRevision = this.latestQueryArtifactID()
    this.selectedDraftVisualID = this.selectedVisualID
    this.dashboardPreviewOpen = true
    this.persistPreviewURL()
  }

  private isPreviewableArtifact(id: string): boolean {
    return Boolean(this.visuals[id] && (this.agent.transcript ?? []).some(item => item.kind === 'tool' && item.name === 'query_visual' && item.status === 'complete' && item.artifact?.id === id))
  }

  private latestQueryArtifactID(): string {
    return (this.agent.transcript ?? []).filter(item => item.kind === 'tool' && item.name === 'query_visual' && item.status === 'complete' && this.visuals[item.artifact?.id ?? '']).at(-1)?.artifact?.id ?? ''
  }

  private syncVisualPreview(): void {
    const conversationId = this.agent.activeConversationId
    if (this.previewConversationID && this.previewConversationID !== conversationId) {
      this.previewArtifactID = ''
      this.requestedPreviewID = ''
      this.clearPreviewURL()
    }
    if (this.requestedPreviewID && this.isPreviewableArtifact(this.requestedPreviewID)) {
      this.previewArtifactID = this.requestedPreviewID
      this.dashboardWorkspace = true
      this.selectedVisualID = this.requestedPreviewID
      this.requestedPreviewID = ''
      this.previewConversationID = conversationId
      this.previewQueryRevision = this.latestQueryArtifactID()
      this.dashboardPreviewOpen = true
    }
    if (!this.previewArtifactID) return
    if (!this.isPreviewableArtifact(this.previewArtifactID)) {
      this.previewArtifactID = ''
      this.dashboardPreviewOpen = Boolean(this.dashboardDraft)
      this.clearPreviewURL()
      return
    }
    const latest = this.latestQueryArtifactID()
    if ((this.dashboardPreviewOpen || this.previewEditing) && latest && latest !== this.previewQueryRevision) {
      this.previewArtifactID = latest
      this.selectedVisualID = latest
      this.selectedDraftVisualID = latest
      this.persistPreviewURL()
    }
    this.previewQueryRevision = latest
  }

  private persistPreviewURL(): void {
    const url = new URL(window.location.href)
    url.searchParams.set('preview', this.previewArtifactID)
    window.history.replaceState(window.history.state, '', url)
  }

  private clearPreviewURL(): void {
    const url = new URL(window.location.href)
    if (!url.searchParams.has('preview')) return
    url.searchParams.delete('preview')
    window.history.replaceState(window.history.state, '', url)
  }

  private preparePreviewTurn = (event: CustomEvent<{ input?: string; references?: AgentReferenceSignal[]; editMessageId?: string; previewArtifactId?: string }>): void => {
    event.detail.previewArtifactId = this.dashboardPreviewOpen || this.previewEditing ? this.previewArtifactID : ''
    this.showOptimisticTurn(event)
  }

  private openDashboardPreview = (): void => {
    this.dashboardPreviewOpen = true
    if (this.previewArtifactID) this.persistPreviewURL()
  }

  private enterDashboardWorkspace = (): void => {
    this.dashboardWorkspace = true
    this.dashboardPreviewOpen = true
  }

  private closeDashboardPreview = (): void => {
    if (this.dashboardWorkspace && !this.previewArtifactID && !this.compactViewport) {
      this.dashboardWorkspace = false
      void this.updateComplete.then(() => this.shadowRoot?.querySelector<HTMLElement & { focusPreview(): void }>('lv-chat-dashboard-draft')?.focusPreview())
      return
    }
    this.dashboardWorkspace = false
    this.previewEditing = false
    this.dashboardPreviewOpen = false
    this.dashboardPickerOpen = false
    this.clearPreviewURL()
    void this.updateComplete.then(() => {
      const panel = this.shadowRoot?.querySelector<ChatVisualPanel>('lv-chat-visual-panel')
      if (panel) panel.focusPreview()
      else this.shadowRoot?.querySelector<HTMLElement & { focusInput(): void }>('lv-chat-composer')?.focusInput()
    })
  }

  private editDashboardVisual = (event: CustomEvent<{ action: 'edit' | 'remove'; visualId: string; title: string; prompt: string }>): void => {
    const detail = event.detail
    if (!detail?.visualId || !detail.prompt) return
    this.selectedDraftVisualID = detail.visualId
    this.previewEditing = Boolean(this.previewArtifactID)
    if (this.compactViewport) this.dashboardPreviewOpen = false
    void this.updateComplete.then(() => {
      this.shadowRoot?.querySelector<HTMLElement & { setDraft(value: string): void }>('lv-chat-composer')?.setDraft(detail.prompt)
    })
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
