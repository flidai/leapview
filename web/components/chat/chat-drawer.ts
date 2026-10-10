import { chatDrawerStyles } from './chat-drawer.styles'
import './agent-visual-library'
import type { VisualLibraryState } from './agent-visual-library'
import { LitElement, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChartColumn, CircleHelp, ExternalLink, LayoutDashboard, Maximize2, Minimize2, Move, Plus, RefreshCw, TrendingUp, X, type IconNode } from 'lucide'
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
import { chatVisualsFromSignals } from './visual-signals'
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
  @property({ type: Boolean, reflect: true }) expanded = false
  @property({ attribute: false }) suggestions: AgentReferenceSignal[] = []
  @property({ attribute: false }) dashboardSavedVisualIds: string[] = []
  @state() private visualLibraryState: VisualLibraryState = { savedIds: [], savingId: '', error: '' }
  @state() private references: AgentReferenceSignal[] = []
  @state() private referenceLimitMessage = ''
	@state() private editMessageId = ''
  @state() private selectedVisualID = ''
  @state() private selectedExplorerHref = ''
  @state() private selectedVisualTitle = ''
  @state() private visualSaving = false
  @state() private visualSaved = false
  @state() private visualSaveError = ''
  private focusReturnTarget: HTMLElement | null = null
	private trackedConversationID: string | null = null
	private trackedAcceptedRunID: string | null = null
	private updateSignalCache = new Map<string, unknown>()
	private cachingUpdateSignals = false

  static styles = chatDrawerStyles

  override connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('popstate', this.syncExpandedLocation)
    this.syncExpandedLocation()
  }

  override disconnectedCallback(): void {
    window.removeEventListener('popstate', this.syncExpandedLocation)
    super.disconnectedCallback()
  }

  private syncExpandedLocation = (): void => {
    this.expanded = new URL(window.location.href).searchParams.get('chat') === 'expanded'
    if (this.expanded) this.open = true
  }

  private toggleExpanded = async (): Promise<void> => {
    const thread = this.shadowRoot?.querySelector<HTMLElement & { captureScroll(): { top: number; autoScroll: boolean }; restoreScroll(position: { top: number; autoScroll: boolean }): void }>('lv-chat-thread')
    const position = thread?.captureScroll()
    this.expanded = !this.expanded
    const url = new URL(window.location.href)
    if (this.expanded) {
      url.searchParams.set('chat', 'expanded')
      window.history.pushState(null, '', url)
    } else {
      url.searchParams.delete('chat')
      window.history.replaceState(null, '', url)
    }
    await this.updateComplete
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    if (position) thread?.restoreScroll(position)
    this.focusComposer()
  }

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
		return chatVisualsFromSignals(this.cachedSignal<Record<string, VisualizationEnvelope>>('agentVisuals', emptyVisuals))
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
    const dataContext = context?.surface === 'data'
    const agentTitle = dataContext ? 'Explorer agent' : 'Dashboard agent'
    const agentEnabled = Boolean(agent.status?.enabled)
    const dataContextReady = !dataContext || Boolean(context?.exploration?.modelId?.trim())
    const showWelcome = agentEnabled && !this.pending && !agent.status.error && !(agent.transcript?.length)
    const composer = html`<lv-chat-composer
      .value=${agent.composer.value ?? ''}
      .disabled=${this.pending || agent.composer.disabled || !agentEnabled || !dataContextReady}
      .pending=${this.pending}
      .running=${Boolean(agent.status.running)}
      .runId=${agent.status.runId ?? ''}
      .canContinue=${Boolean(agent.status.canContinue)}
      .placeholder=${agentEnabled ? !dataContextReady ? 'Select a model or switch to Analyze' : this.embedded ? 'Ask to change this dashboard…' : context?.exploration ? 'Ask about this data…' : 'Ask about this dashboard…' : 'Agent is not configured'}
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
		<aside class=${showWelcome ? 'drawer welcome-mode' : 'drawer'} role="dialog" aria-modal="false" aria-label=${agentTitle} aria-hidden=${String(!this.open)} ?inert=${!this.open} @keydown=${this.handleKeydown}>
        <header class="header">
          <div class="toolbar">
            <div class="title">${agentIcon()}<span>${agentTitle}</span></div>
            <div class="toolbar-actions">
              <button class="text-action" type="button" title=${this.pending ? 'Wait for the current answer to finish' : agentEnabled ? 'New chat' : 'Agent is not configured'} aria-label="New chat" ?disabled=${!agentEnabled || this.pending} @click=${this.newChat}>${lucideIcon(Plus)}<span>New chat</span></button>
              <button type="button" title=${this.expanded ? 'Shrink chat' : 'Expand chat'} aria-label=${this.expanded ? 'Shrink chat' : 'Expand chat'} aria-pressed=${this.expanded} @click=${this.toggleExpanded}>${lucideIcon(this.expanded ? Minimize2 : Maximize2)}</button>
					  <button class="close-action" type="button" title="Close" aria-label="Close agent" @click=${this.closeDrawer}>${lucideIcon(X)}</button>
            </div>
          </div>
          <section class="context" aria-label=${dataContext ? 'Included data context' : 'Included dashboard context'}>
            <div class="context-line">
              <span class="page-context">${context?.pageTitle || (dataContext ? 'Current exploration' : 'Current page')}</span>
              ${controls || selections ? html`<span class="context-separator" aria-hidden="true">·</span><span class="filter-context">${controls} ${controls === 1 ? 'filter' : 'filters'} · ${selections} ${selections === 1 ? 'selection' : 'selections'}</span>` : null}
            </div>
            ${agentEnabled && !dataContextReady ? html`<p class="reference-limit-status" role="status">Select a model or switch to Analyze to ask about data.</p>` : null}
            ${this.referenceLimitMessage ? html`
              <div class="reference-limit-status" data-reference-limit-status role="status" aria-live="polite">${this.referenceLimitMessage}</div>
            ` : null}
          </section>
        </header>
        ${showWelcome ? html`
          <section class="welcome" aria-label=${dataContext ? 'Start a data conversation' : 'Start a dashboard conversation'}>
            <div class="welcome-heading"><span class="agent-mark" aria-hidden="true">${agentIcon()}</span><h2>${this.embedded ? 'What should I change?' : 'What would you like to understand?'}</h2></div>
            ${composer}
            <div class="prompts">
              ${(this.embedded ? builderPrompts : dashboardPrompts).map(({ label, prompt, icon }) => html`<button class="prompt" type="button" title=${prompt} aria-label=${`${label}: ${prompt}`} ?disabled=${!dataContextReady} @click=${() => this.fillPrompt(prompt)}>${lucideIcon(icon, { size: 16, strokeWidth: 2 })}<span>${label}</span></button>`)}
            </div>
            <p class="welcome-hint">Type <kbd>@</kbd> to attach ${this.embedded ? 'a chart on this page.' : 'a dashboard, metric, model, page, or visual.'}</p>
          </section>
        ` : null}
        <lv-agent-visual-library .agent=${agent} @lv-visual-library-state=${(event: CustomEvent<VisualLibraryState>) => { this.visualLibraryState = event.detail }}></lv-agent-visual-library>
        ${this.visualLibraryState.error ? html`<p role="alert">${this.visualLibraryState.error}</p>` : null}
        <lv-chat-thread .dashboardVisualIds=${Object.entries(this.visualLibraryState.libraryIds ?? {}).filter(([, id]) => this.dashboardSavedVisualIds.includes(id)).map(([artifactId]) => artifactId)} .savedVisualIds=${this.visualLibraryState.savedIds} .savingVisualId=${this.visualLibraryState.savingId} ?hidden=${showWelcome || Boolean(this.selectedVisualID)}
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
    this.references = []
    this.referenceLimitMessage = ''
    this.notifyReferences()
		emitDomainEvent(this, domainEvents.chatNew, undefined)
  }

	private closeDrawer() {
		if (this.expanded) {
			void this.toggleExpanded()
			return
		}
		this.open = false
		emitDomainEvent(this, domainEvents.chatDrawerClose, undefined)
	}

  private openVisual = (event: CustomEvent<{ artifactId: string; explorerHref: string; title: string }>): void => {
    const artifactId = event.detail?.artifactId ?? ''
    if (!artifactId || !this.visuals[artifactId]) return
    this.selectedVisualID = artifactId
    this.selectedExplorerHref = event.detail.explorerHref ?? ''
    this.selectedVisualTitle = event.detail.title ?? ''
    this.visualSaved = false
    this.visualSaveError = ''
    void this.updateComplete.then(() => this.shadowRoot?.querySelector<ChatVisualPanel>('lv-chat-visual-panel')?.focusClose())
  }

  private closeVisual(restoreFocus = true): void {
    const artifactId = this.selectedVisualID
    if (!artifactId) return
    this.clearSelectedVisual()
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
