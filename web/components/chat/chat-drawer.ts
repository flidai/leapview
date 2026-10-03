import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChartColumn, CircleHelp, ExternalLink, LayoutDashboard, Maximize2, Move, Plus, RefreshCw, TrendingUp, X, type IconNode } from 'lucide'
import type {
  AgentContextSignal,
	DashboardInteractionSelection,
	AgentReferenceSearchSignal,
	AgentReferenceSignal,
	ChatSignal,
	ChatTranscriptItemSignal,
} from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DatastarLit } from '../shared/datastar-lit'
import { domainEvents, emitDomainEvent } from '../shared/events'
import { lucideIcon } from '../shared/lucide-icons'
import { agentIcon } from './agent-icon'
import { clearDrawerReturn, fullChatHref, handoffBuilderConversation, readDrawerReturn, rememberChatReturn, type DrawerReturnState } from './chat-navigation'
import { previewChatBuilder } from './chat-builder-preview'
import './chat-visual-panel'
import './chat-composer'
import './chat-thread'
import type { ChatVisualPanel } from './chat-visual-panel'
import { saveChatVisual } from './saved-visuals'
import {
  type ChatReferencesChangeDetail,
  defaultAgentReferenceLimit,
  isOnPageReference,
	latestAcceptedRunId,
  mergeReferences,
  normalizeReferenceLimit,
  referenceIdentity,
} from './reference'

const emptyAgent: ChatSignal = {
  conversations: [],
  activeConversationId: '',
  transcript: [],
  status: { enabled: false, running: false },
  composer: { value: '', disabled: true, placeholder: 'Agent is not configured.' },
}

const emptyVisuals: Record<string, VisualizationEnvelope> = {}
const emptyDashboardFilters: NonNullable<AgentContextSignal['filters']> = {
	revision: 0,
	appliedControls: {},
	draftControls: {},
	dirtyBindings: [],
	defaultsRevision: '',
}
const emptyDashboardSelections: DashboardInteractionSelection[] = []
const emptyReferenceSearch: AgentReferenceSearchSignal = { query: '', requestId: 0, results: [] }

const builderPrompts: Array<{ label: string; prompt: string; icon: IconNode }> = [
  { label: 'Add a chart', prompt: 'Add a chart for the main metric by category.', icon: ChartColumn },
  { label: 'Change a visual', prompt: 'Change this chart to a bar chart.', icon: RefreshCw },
  { label: 'Move a chart', prompt: 'Move this chart to a different position.', icon: Move },
  { label: 'Resize a chart', prompt: 'Resize this chart to make it taller.', icon: Maximize2 },
]

const dashboardPrompts: Array<{ label: string; prompt: string; icon: IconNode }> = [
  { label: 'Summarize', prompt: 'Summarize the key takeaways on this page.', icon: LayoutDashboard },
  { label: 'Explain metrics', prompt: 'Explain how the main metrics are calculated.', icon: CircleHelp },
  { label: 'Find insights', prompt: 'Which results stand out, and why?', icon: TrendingUp },
]

class ChatDrawer extends DatastarLit(LitElement) {
  @property({ type: Boolean, reflect: true }) open = false
  @property({ type: Boolean, reflect: true }) embedded = false
  @property({ attribute: false }) suggestions: AgentReferenceSignal[] = []
  @state() private references: AgentReferenceSignal[] = []
  @state() private referenceLimitMessage = ''
	@state() private editMessageId = ''
  @state() private selectedVisualID = ''
  @state() private selectedExplorerHref = ''
  @state() private selectedVisualTitle = ''
  @state() private visualSaving = false
  @state() private visualSaved = false
  @state() private visualSaveError = ''
  @state() private visualPreviewPending = false
  @state() private visualPreviewHref = ''
  private returnState: DrawerReturnState | undefined
  private restoringReturn = false
  private requestedReturnConversationID = ''
  private retainedDraft = ''
  private retainedScroll: DrawerReturnState['scroll'] | undefined
  private focusReturnTarget: HTMLElement | null = null
	private trackedConversationID: string | null = null
	private trackedAcceptedRunID: string | null = null
	private updateSignalCache = new Map<string, unknown>()
	private cachingUpdateSignals = false

  static styles = css`
    :host {
      display: block;
      box-sizing: border-box;
      width: 0;
      min-width: 0;
      height: 100svh;
      overflow: hidden;
      border-left: 0 solid var(--lv-line-muted);
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      --lv-chat-stack-width: 100%;
    }

    :host([open]) {
			width: 100%;
      border-left-width: 1px;
    }

    :host([embedded]) {
      height: 100%;
      border-left: 0;
    }

    .drawer {
      display: grid;
			width: 100%;
      height: 100%;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr) auto;
      background: var(--lv-bg-app);
    }

    .drawer.welcome-mode { grid-template-rows: auto minmax(0, 1fr); }

    .header {
      display: grid;
			gap: var(--lv-space-sm);
			padding: var(--lv-space-md) var(--lv-space-lg) var(--lv-space-sm);
    }

    .toolbar {
      display: flex;
      min-width: 0;
      align-items: center;
    }

    .title {
      display: flex;
      min-width: 0;
      flex: 1;
      align-items: center;
			gap: var(--lv-space-sm);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .toolbar-actions {
      display: flex;
      align-items: center;
      gap: var(--lv-space-2xs);
    }

    .title svg,
    button svg,
    a svg {
      width: 16px;
      height: 16px;
    }

    button,
    a {
      display: inline-grid;
			width: var(--control-medium-size);
			height: var(--control-medium-size);
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
      text-decoration: none;
    }

    button:hover,
    button:focus-visible,
    a:hover,
    a:focus-visible {
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    a[aria-disabled="true"] { opacity: 0.5; cursor: wait; }

    button:disabled {
      color: var(--lv-fg-muted);
      cursor: not-allowed;
      opacity: 0.5;
    }

    button:disabled:hover {
      background: transparent;
    }

    .text-action { font: var(--lv-type-caption); width: auto; display: inline-flex; gap: var(--lv-space-xs); padding-inline: var(--lv-space-sm); }
    .welcome { box-sizing: border-box; min-width: 0; min-height: 0; overflow: auto; padding: var(--lv-space-lg) var(--lv-space-sm); display: flex; flex-direction: column; align-items: center; justify-content: safe center; gap: var(--lv-space-md); }
    .welcome-heading { display: flex; align-items: center; justify-content: center; gap: var(--lv-space-sm); text-align: center; }
    .welcome-heading .agent-mark { display: grid; place-items: center; color: var(--lv-accent); }
    .welcome-heading .agent-mark svg { width: var(--base-size-20); height: var(--base-size-20); }
    .welcome h2 { margin: 0; font: var(--lv-type-section-title); }
    .welcome lv-chat-composer { width: 100%; flex: 0 0 auto; }
    .prompts { display: flex; flex-wrap: wrap; justify-content: center; gap: var(--lv-space-sm); padding-inline: var(--lv-space-sm); }
    .prompt { display: inline-flex; width: auto; height: auto; min-height: var(--lv-control-medium); align-items: center; gap: var(--lv-space-xs); padding: 0 var(--lv-space-md); border: var(--lv-border-muted); border-radius: var(--lv-radius-full); background: var(--lv-bg-panel); color: var(--lv-fg-default); font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-medium); white-space: nowrap; }
    .prompt:hover { border-color: var(--lv-line-accent-muted); }
    .prompt svg { width: var(--base-size-16); height: var(--base-size-16); color: var(--lv-accent); }
    .welcome-hint { margin: 0; padding-inline: var(--lv-space-md); color: var(--lv-fg-muted); text-align: center; font: var(--lv-type-caption); }
    .welcome-hint kbd { display: inline-grid; min-width: 20px; height: 20px; place-items: center; border: var(--lv-border-muted); border-radius: var(--lv-radius-tight); background: var(--lv-bg-control); color: var(--lv-fg-default); font: inherit; }
    button:focus-visible, a:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-line-accent); outline-offset: var(--lv-space-2xs); }

    .close-action {
      margin-left: var(--lv-space-xs);
    }

    :host([embedded]) .title,
    :host([embedded]) .close-action {
      display: none;
    }

    :host([embedded]) .toolbar {
      justify-content: flex-end;
    }

    :host([embedded]) .header {
      padding-block-start: var(--lv-space-sm);
    }

    :host([embedded]) .text-action {
      width: var(--control-medium-size);
      padding-inline: 0;
    }

    :host([embedded]) .text-action span {
      display: none;
    }

    .context {
      display: grid;
			gap: var(--lv-space-sm);
      border: 0;
			padding: 0;
      background: var(--lv-bg-app);
      font: var(--lv-type-caption);
    }

    .context-line {
      display: flex;
      min-width: 0;
      align-items: center;
			gap: var(--lv-space-xs);
    }

    .page-context {
      overflow: hidden;
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-medium);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .context-separator {
      color: var(--lv-fg-muted);
    }

    .filter-context {
      color: var(--lv-fg-muted);
      white-space: nowrap;
    }

    .reference-limit-status {
      color: var(--lv-fg-muted);
    }

    lv-chat-thread {
      display: block;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    lv-chat-thread[hidden] { display: none; }

    lv-chat-visual-panel {
      grid-row: 2 / 4;
      min-width: 0;
      min-height: 0;
      z-index: 1;
      background: var(--lv-bg-app);
    }

    lv-chat-composer {
      display: block;
      border-top: 0;
      background: transparent;
    }

    @media (max-width: 720px) {
      :host([open]) {
        position: fixed;
        inset: 0;
        z-index: var(--zIndex-modal, 200);
        width: 100vw;
        border-left: 0;
      }

      :host([open][embedded]) {
        position: static;
        width: 100%;
        height: 100%;
      }

      .drawer {
        width: 100vw;
      }

      :host([embedded]) .drawer {
        width: 100%;
      }
    }

    @media (prefers-reduced-motion: reduce) {
      :host { transition: none; }
    }
  `

	override performUpdate(): void {
		// DatastarLit materializes a signal tree on every read. The drawer reads
		// the agent transcript in both updated() and render(), so keep one
		// snapshot for the complete Lit update and avoid cloning a long transcript
		// twice when the dashboard pane toggles.
		this.cachingUpdateSignals = true
		this.updateSignalCache.clear()
		try {
			super.performUpdate()
		} finally {
			this.cachingUpdateSignals = false
			this.updateSignalCache.clear()
		}
	}

	private cachedSignal<T>(path: string, fallback: T): T {
		if (!this.cachingUpdateSignals) return this.signal<T>(path, fallback)
		if (this.updateSignalCache.has(path)) return this.updateSignalCache.get(path) as T
		const value = this.signal<T>(path, fallback)
		this.updateSignalCache.set(path, value)
		return value
	}

  get agent(): ChatSignal {
		return this.cachedSignal<ChatSignal>('agent', emptyAgent)
  }

  get context(): AgentContextSignal | null {
		return this.cachedSignal<AgentContextSignal | null>('agentContext', null)
	}

	get visuals(): Record<string, VisualizationEnvelope> {
		return this.cachedSignal<Record<string, VisualizationEnvelope>>('agentVisuals', emptyVisuals)
	}

	get dashboardFilters(): AgentContextSignal['filters'] {
		return this.cachedSignal<AgentContextSignal['filters']>('filterState', this.context?.filters ?? emptyDashboardFilters)
	}

	get dashboardSelections(): DashboardInteractionSelection[] {
		return this.cachedSignal<DashboardInteractionSelection[]>('interactionSelections', emptyDashboardSelections)
	}

  get pending(): boolean {
    return this.signal<boolean>('agentTurnPending', false) || Boolean(this.agent.status.running)
  }

	get referenceSearch(): AgentReferenceSearchSignal {
		return this.cachedSignal<AgentReferenceSearchSignal>('agentReferenceSearch', emptyReferenceSearch)
	}

  connectedCallback(): void {
    this.returnState = readDrawerReturn()
    super.connectedCallback()
    window.addEventListener('pageshow', this.handlePageShow)
  }

  disconnectedCallback(): void {
    window.removeEventListener('pageshow', this.handlePageShow)
    super.disconnectedCallback()
  }

  private handlePageShow = (event: PageTransitionEvent): void => {
    this.returnState = readDrawerReturn()
    const conversationId = this.returnState?.conversationId
    if (event.persisted && conversationId && this.requestedReturnConversationID !== conversationId) {
      // A cached document has not rerun its shell initialization. Reload the
      // authorized transcript, including turns added while full chat was open.
      // Mark before dispatch so absent/unauthorized replies cannot retry.
      this.requestedReturnConversationID = conversationId
      emitDomainEvent(this, domainEvents.chatRestore, { conversationId })
    }
    this.requestUpdate()
  }

  private expandChat = (event: MouseEvent): void => {
    if (this.pending) { event.preventDefault(); return }
    ;(event.currentTarget as HTMLAnchorElement).href = this.expandedHref()
  }

  private expandedHref(): string {
    this.requestedReturnConversationID = ''
    const composer = this.shadowRoot?.querySelector<HTMLElement & { snapshotDraft(): string }>('lv-chat-composer')
    const thread = this.shadowRoot?.querySelector<HTMLElement & { snapshotScroll(): DrawerReturnState['scroll'] }>('lv-chat-thread')
    const token = rememberChatReturn({
      conversationId: this.agent.activeConversationId ?? '',
      draft: composer?.snapshotDraft() ?? this.retainedDraft,
      references: this.references,
      editMessageId: this.editMessageId,
      selectedVisualId: this.selectedVisualID,
      selectedExplorerHref: this.selectedExplorerHref,
      selectedVisualTitle: this.selectedVisualTitle,
      scroll: this.retainedScroll ?? thread?.snapshotScroll() ?? { top: 0, follow: true },
    })
    return fullChatHref(this.agent.activeConversationId ?? '', token ? `?return=${token}` : '')
  }

  private previewVisual = async (): Promise<void> => {
    if (this.pending || this.visualPreviewPending || !this.selectedVisualID || !this.agent.activeConversationId) return
    const conversationId = this.agent.activeConversationId
    const artifactId = this.selectedVisualID
    const title = this.selectedVisualTitle || this.visuals[artifactId]?.spec.title || 'Visual result'
    const state: DrawerReturnState = {
      conversationId, draft: this.retainedDraft, references: [...this.references],
      editMessageId: this.editMessageId, selectedVisualId: '', selectedExplorerHref: '', selectedVisualTitle: '',
      scroll: this.retainedScroll ?? { top: 0, follow: true },
    }
    this.visualPreviewPending = true
    this.visualSaveError = ''
    try {
      // Retain the original sidebar snapshot for browser Back while handing
      // the same conversation to the new builder's authorized restore path.
      this.expandedHref()
      const result = await previewChatBuilder({ conversationId, artifactId, title })
      if (!this.isConnected || this.agent.activeConversationId !== conversationId) return
      this.visualPreviewHref = result.href
      location.assign(handoffBuilderConversation(result.href, state))
    } catch (error) {
      if (!this.isConnected || this.agent.activeConversationId !== conversationId) return
      this.visualSaveError = error instanceof Error ? error.message : 'Could not open the dashboard builder. Please try again.'
    } finally {
      this.visualPreviewPending = false
    }
  }

  private restoreReturnState(): void {
    const saved = this.returnState
    if (!saved || this.restoringReturn || (this.agent.activeConversationId ?? '') !== saved.conversationId) return
    // The shell first authorizes and hydrates the remembered conversation.
    if (saved.conversationId && !this.agent.transcript?.length) return
    this.restoringReturn = true
    this.returnState = undefined
    this.references = Array.isArray(saved.references) ? saved.references : []
    this.editMessageId = saved.editMessageId && this.canEditMessage(saved.editMessageId) ? saved.editMessageId : ''
    if (saved.selectedVisualId && this.visuals[saved.selectedVisualId]) {
      this.selectedVisualID = saved.selectedVisualId
      this.selectedExplorerHref = saved.selectedExplorerHref
      this.selectedVisualTitle = saved.selectedVisualTitle
    }
    this.retainedDraft = saved.draft ?? ''
    if (this.selectedVisualID) this.retainedScroll = saved.scroll
    this.notifyReferences()
    void this.updateComplete.then(async () => {
      const composer = this.shadowRoot?.querySelector<LitElement & { setDraft(value: string, focus?: boolean): void }>('lv-chat-composer')
      await composer?.updateComplete
      composer?.setDraft(saved.draft ?? '', false)
      const thread = this.shadowRoot?.querySelector<LitElement & { restoreScroll(state: DrawerReturnState['scroll']): void }>('lv-chat-thread')
      await thread?.updateComplete
      thread?.restoreScroll(saved.scroll ?? { top: 0, follow: true })
      clearDrawerReturn()
      this.restoringReturn = false
    })
  }

  public openDrawer(): void {
    if (this.open) this.focusComposer()
    this.open = true
  }

  public openWithReference(reference: AgentReferenceSignal): void {
    const alreadyAttached = this.references.some((current) => referenceIdentity(current) === referenceIdentity(reference))
    if (!alreadyAttached && this.references.length >= this.normalizedReferenceLimit()) {
      const limit = this.normalizedReferenceLimit()
      this.referenceLimitMessage = `Up to ${limit} ${limit === 1 ? 'item' : 'items'} can be attached`
      this.openDrawer()
      return
    }
    if (!alreadyAttached) {
      this.references = [...this.references, reference]
      this.referenceLimitMessage = ''
      this.notifyReferences()
    }
    this.openDrawer()
  }

  protected updated(changed: Map<string, unknown>): void {
		this.syncEditState()
    if (this.embedded && this.agent.status.enabled && this.returnState?.conversationId
      && this.agent.activeConversationId !== this.returnState.conversationId
      && this.requestedReturnConversationID !== this.returnState.conversationId) {
      this.requestedReturnConversationID = this.returnState.conversationId
      emitDomainEvent(this, domainEvents.chatRestore, { conversationId: this.returnState.conversationId })
    }
    this.restoreReturnState()
		if (this.selectedVisualID && !this.visuals[this.selectedVisualID]) this.closeVisual(false)
    if (!changed.has('open')) return
    if (!this.open) {
      this.focusReturnTarget?.focus()
      this.focusReturnTarget = null
      return
    }
    this.focusReturnTarget = deepActiveElement(document)
    this.focusComposer()
  }

  private focusComposer(): void {
    void this.updateComplete.then(async () => {
      if (!this.open) return
      const composer = this.shadowRoot?.querySelector('lv-chat-composer') as (LitElement & { remeasure(): void }) | null
      await composer?.updateComplete
      if (!this.open) return
      composer?.remeasure()
      const textarea = composer?.shadowRoot?.querySelector<HTMLTextAreaElement>('textarea:not(:disabled)')
      const fallback = this.shadowRoot?.querySelector<HTMLButtonElement>('.close-action')
      ;(textarea ?? fallback)?.focus()
    })
  }

  render() {
    const agent = this.agent
		const context = this.context
		const currentFilters = this.dashboardFilters
		const controls = Object.values(currentFilters.appliedControls ?? {})
			.filter((control) => control.expression.kind !== 'unfiltered').length
		const selections = this.dashboardSelections.length
		const searchResults = this.referenceSearch.results ?? []
		const pinnedSuggestions = mergeReferences(
			searchResults.filter((reference) => isOnPageReference(reference, context)),
			this.suggestions,
		)
		const catalogSuggestions = searchResults.filter((reference) => !isOnPageReference(reference, context))
    const conversationHref = agent.activeConversationId
      ? `/chats/${encodeURIComponent(agent.activeConversationId)}`
      : '/chats/new'
    const agentEnabled = Boolean(agent.status?.enabled)
    const showWelcome = agentEnabled && !this.pending && !agent.status.error && !(agent.transcript?.length)
    const composer = html`<lv-chat-composer
      .value=${agent.composer.value ?? ''}
      .disabled=${this.pending || agent.composer.disabled || !agentEnabled}
      .pending=${this.pending}
      .running=${Boolean(agent.status.running)}
      .runId=${agent.status.runId ?? ''}
      .canContinue=${Boolean(agent.status.canContinue)}
      .placeholder=${agentEnabled ? this.embedded ? 'Ask to change this dashboard…' : context?.exploration ? 'Ask about this data…' : 'Ask about this dashboard…' : 'Agent is not configured'}
      .references=${this.references}
      .referenceLimit=${context?.referenceLimit ?? defaultAgentReferenceLimit}
      .pinnedSuggestions=${pinnedSuggestions}
      .suggestions=${catalogSuggestions}
      .suggestionQuery=${this.referenceSearch.query}
      .suggestionRequestId=${this.referenceSearch.requestId}
      .acceptedRunId=${latestAcceptedRunId(agent.transcript ?? [])}
      .editMessageId=${this.editMessageId}
      .editing=${Boolean(this.editMessageId)}
      @lv-chat-references-change=${this.referencesChanged}
      @lv-chat-edit-cancel=${this.cancelEdit}
    ></lv-chat-composer>`
    return html`
		<aside class=${showWelcome ? 'drawer welcome-mode' : 'drawer'} role="dialog" aria-modal="false" aria-label="Dashboard agent" aria-hidden=${String(!this.open)} ?inert=${!this.open} @keydown=${this.handleKeydown}>
        <header class="header">
          <div class="toolbar">
            <div class="title">${agentIcon()}<span>Dashboard agent</span></div>
            <div class="toolbar-actions">
              <button class="text-action" type="button" title=${this.pending ? 'Wait for the current answer to finish' : agentEnabled ? 'New chat' : 'Agent is not configured'} aria-label="New chat" ?disabled=${!agentEnabled || this.pending} @click=${this.newChat}>${lucideIcon(Plus)}<span>New chat</span></button>
              <a class="text-action" href=${this.pending ? nothing : conversationHref} title=${this.pending ? 'Wait for the current answer to finish' : 'Open full chat'} aria-label="Open full chat" aria-disabled=${String(this.pending)} @click=${this.expandChat}>${lucideIcon(ExternalLink)}<span>Full chat</span></a>
					  <button class="close-action" type="button" title="Close" aria-label="Close agent" @click=${this.closeDrawer}>${lucideIcon(X)}</button>
            </div>
          </div>
          <section class="context" aria-label="Included dashboard context">
            <div class="context-line">
              <span class="page-context">${context?.pageTitle || 'Current page'}</span>
              ${controls || selections ? html`<span class="context-separator" aria-hidden="true">·</span><span class="filter-context">${controls} ${controls === 1 ? 'filter' : 'filters'} · ${selections} ${selections === 1 ? 'selection' : 'selections'}</span>` : null}
            </div>
            ${this.referenceLimitMessage ? html`
              <div class="reference-limit-status" data-reference-limit-status role="status" aria-live="polite">${this.referenceLimitMessage}</div>
            ` : null}
          </section>
        </header>
        ${showWelcome ? html`
          <section class="welcome" aria-label="Start a dashboard conversation">
            <div class="welcome-heading"><span class="agent-mark" aria-hidden="true">${agentIcon()}</span><h2>${this.embedded ? 'What should I change?' : 'What would you like to understand?'}</h2></div>
            ${composer}
            <div class="prompts">
              ${(this.embedded ? builderPrompts : dashboardPrompts).map(({ label, prompt, icon }) => html`<button class="prompt" type="button" title=${prompt} aria-label=${`${label}: ${prompt}`} @click=${() => this.fillPrompt(prompt)}>${lucideIcon(icon, { size: 16, strokeWidth: 2 })}<span>${label}</span></button>`)}
            </div>
            <p class="welcome-hint">Type <kbd>@</kbd> to attach ${this.embedded ? 'a chart on this page.' : 'a dashboard, metric, model, page, or visual.'}</p>
          </section>
        ` : null}
        <lv-chat-thread ?hidden=${showWelcome || Boolean(this.selectedVisualID)}
          surface="drawer"
          .transcript=${agent.transcript ?? []}
          .visuals=${this.visuals}
          .status=${this.pending ? { ...agent.status, running: true } : agent.status}
          conversation-id=${agent.activeConversationId ?? ''}
          @lv-chat-reuse=${this.reuseDraft}
          @lv-chat-visual-open=${this.openVisual}
        ></lv-chat-thread>
        ${showWelcome || this.selectedVisualID ? null : composer}
        ${this.selectedVisualID && this.visuals[this.selectedVisualID] ? html`
          <lv-chat-visual-panel
            artifact-id=${this.selectedVisualID}
            title=${this.selectedVisualTitle || this.visuals[this.selectedVisualID].spec.title || 'Visual result'}
            .payload=${this.visuals[this.selectedVisualID]}
            .explorerHref=${this.selectedExplorerHref}
            .dashboardAvailable=${!this.pending && Boolean(agent.activeConversationId) && (agent.transcript ?? []).some(item => item.kind === 'tool' && item.name === 'query_visual' && item.status === 'complete' && item.artifact?.id === this.selectedVisualID)}
            .previewPending=${this.visualPreviewPending}
            .previewHref=${this.visualPreviewHref}
            @lv-chat-visual-preview=${this.previewVisual}
            .saving=${this.visualSaving}
            .saved=${this.visualSaved}
            .saveError=${this.visualSaveError}
            @lv-chat-visual-close=${() => this.closeVisual()}
            @lv-chat-visual-save=${this.saveVisual}
          ></lv-chat-visual-panel>
        ` : null}
      </aside>
    `
  }

  private fillPrompt(prompt: string) {
    const composer = this.shadowRoot?.querySelector('lv-chat-composer') as (HTMLElement & { setDraft(value: string): void }) | null
    composer?.setDraft(prompt)
  }

  private newChat() {
    if (this.pending) return
		this.clearSelectedVisual()
		this.clearEditMessage()
    this.fillPrompt('')
    this.retainedDraft = ''
    this.retainedScroll = undefined
    this.references = []
    this.referenceLimitMessage = ''
    this.notifyReferences()
		emitDomainEvent(this, domainEvents.chatNew, undefined)
  }

	private closeDrawer() {
		this.open = false
		emitDomainEvent(this, domainEvents.chatDrawerClose, undefined)
	}

  private openVisual = (event: CustomEvent<{ artifactId: string; explorerHref: string; title: string }>): void => {
    const artifactId = event.detail?.artifactId ?? ''
    if (!artifactId || !this.visuals[artifactId]) return
    this.retainedDraft = this.shadowRoot?.querySelector<HTMLElement & { snapshotDraft(): string }>('lv-chat-composer')?.snapshotDraft() ?? ''
    this.retainedScroll = this.shadowRoot?.querySelector<HTMLElement & { snapshotScroll(): DrawerReturnState['scroll'] }>('lv-chat-thread')?.snapshotScroll()
    this.selectedVisualID = artifactId
    this.selectedExplorerHref = event.detail.explorerHref ?? ''
    this.selectedVisualTitle = event.detail.title ?? ''
    this.visualSaved = false
    this.visualSaveError = ''
    this.visualPreviewHref = ''
    void this.updateComplete.then(() => this.shadowRoot?.querySelector<ChatVisualPanel>('lv-chat-visual-panel')?.focusClose())
  }

  private closeVisual(restoreFocus = true): void {
    const artifactId = this.selectedVisualID
    if (!artifactId) return
    this.clearSelectedVisual()
    void this.updateComplete.then(async () => {
      const composer = this.shadowRoot?.querySelector<LitElement & { setDraft(value: string, focus?: boolean): void }>('lv-chat-composer')
      await composer?.updateComplete
      composer?.setDraft(this.retainedDraft, false)
      const thread = this.shadowRoot?.querySelector<LitElement & { restoreScroll(state: DrawerReturnState['scroll']): void }>('lv-chat-thread')
      await thread?.updateComplete
      if (this.retainedScroll) thread?.restoreScroll(this.retainedScroll)
      this.retainedScroll = undefined
    })
    if (!restoreFocus) return
    void this.updateComplete.then(() => {
      const cards = this.shadowRoot?.querySelector('lv-chat-thread')?.shadowRoot?.querySelectorAll<HTMLButtonElement>('.artifact-card')
      Array.from(cards ?? []).find(card => card.dataset.visualId === artifactId)?.focus()
    })
  }

  private clearSelectedVisual(): void {
    this.selectedVisualID = ''
    this.selectedExplorerHref = ''
    this.selectedVisualTitle = ''
    this.visualSaved = false
    this.visualSaveError = ''
    this.visualPreviewHref = ''
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

  private handleKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'Escape' || !this.open || event.defaultPrevented) return
    event.preventDefault()
    event.stopPropagation()
    this.closeDrawer()
  }

	private referencesChanged(event: CustomEvent<ChatReferencesChangeDetail>) {
    this.references = event.detail.references ?? []
    this.referenceLimitMessage = ''
    this.notifyReferences()
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
		this.references = mergeReferences(detail.references ?? []).slice(0, this.normalizedReferenceLimit())
		this.referenceLimitMessage = ''
		this.notifyReferences()
		this.shadowRoot?.querySelector<HTMLElement & { setDraft(value: string, focus?: boolean): void }>('lv-chat-composer')?.setDraft(detail.text ?? '')
	}

	private syncEditState(): void {
		const conversationID = this.agent.activeConversationId?.trim() ?? ''
		const acceptedRunID = latestAcceptedRunId(this.agent.transcript ?? [])
		const conversationChanged = this.trackedConversationID !== null && this.trackedConversationID !== conversationID
		if (conversationChanged) {
      this.retainedDraft = ''
      this.retainedScroll = undefined
			this.clearSelectedVisual()
			// Composer state belongs to the active conversation. A route signal can
			// switch conversations without recreating the drawer, so clear an
			// unsent draft and attached references before they leak into the next
			// conversation.
			if (this.references.length > 0) {
				this.references = []
				this.notifyReferences()
			}
			this.shadowRoot?.querySelector<HTMLElement & { setDraft(value: string, focus?: boolean): void }>('lv-chat-composer')?.setDraft('', false)
		}
		if (
			conversationChanged
			|| (this.trackedAcceptedRunID !== null && acceptedRunID && this.trackedAcceptedRunID !== acceptedRunID)
		) {
			this.clearEditMessage()
		}
		this.trackedConversationID = conversationID
		this.trackedAcceptedRunID = acceptedRunID
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

  private normalizedReferenceLimit(): number {
		return normalizeReferenceLimit(this.context?.referenceLimit ?? defaultAgentReferenceLimit)
  }

  private notifyReferences() {
    emitDomainEvent<ChatReferencesChangeDetail>(this, domainEvents.agentReferencesChange, { references: this.references })
  }
}

type ChatReuseDetail = {
  text: string
  references?: ChatTranscriptItemSignal['references']
  editMessageId?: string
}

function deepActiveElement(root: Document | ShadowRoot): HTMLElement | null {
  let active = root.activeElement
  while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement
  return active instanceof HTMLElement ? active : null
}

if (!customElements.get('lv-chat-drawer')) customElements.define('lv-chat-drawer', ChatDrawer)
