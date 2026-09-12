import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ExternalLink, Plus, X } from 'lucide'
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
import './chat-composer'
import './chat-thread'
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

class ChatDrawer extends DatastarLit(LitElement) {
  @property({ type: Boolean, reflect: true }) open = false
  @property({ type: Boolean, reflect: true }) embedded = false
  @property({ attribute: false }) suggestions: AgentReferenceSignal[] = []
  @state() private references: AgentReferenceSignal[] = []
  @state() private referenceLimitMessage = ''
	@state() private editMessageId = ''
  private focusReturnTarget: HTMLElement | null = null
	private trackedConversationID: string | null = null
	private trackedAcceptedRunID: string | null = null

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
    .welcome { min-height: 0; overflow: auto; padding: var(--lv-space-lg); display: flex; flex-direction: column; justify-content: center; gap: var(--lv-space-md); }
    .welcome h2 { margin: 0; font: var(--lv-type-section-title); }
    .welcome p { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-body); }
    .prompts { display: grid; gap: var(--lv-space-sm); }
    .prompt { width: 100%; height: auto; min-height: var(--control-large-size); padding: var(--lv-space-md); text-align: left; justify-content: start; border: var(--lv-border-muted); background: var(--lv-bg-panel); color: var(--lv-fg-default); font: inherit; }
    .context-hint { color: var(--lv-fg-muted); }
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

  get agent(): ChatSignal {
    return this.signal<ChatSignal>('agent', emptyAgent)
  }

  get context(): AgentContextSignal | null {
    return this.signal<AgentContextSignal | null>('agentContext', null)
  }

	get visuals(): Record<string, VisualizationEnvelope> {
		return this.signal<Record<string, VisualizationEnvelope>>('agentVisuals', {})
  }

	get dashboardFilters(): AgentContextSignal['filters'] {
		return this.signal<AgentContextSignal['filters']>('filterState', this.context?.filters ?? {
			revision: 0,
			appliedControls: {},
			draftControls: {},
			dirtyBindings: [],
			defaultsRevision: '',
		})
	}

	get dashboardSelections(): DashboardInteractionSelection[] {
		return this.signal<DashboardInteractionSelection[]>('interactionSelections', [])
	}

  get pending(): boolean {
    return this.signal<boolean>('agentTurnPending', false) || Boolean(this.agent.status.running)
  }

	get referenceSearch(): AgentReferenceSearchSignal {
		return this.signal<AgentReferenceSearchSignal>('agentReferenceSearch', {
			query: '', requestId: 0, results: [],
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
    return html`
		<aside class="drawer" role="dialog" aria-modal="false" aria-label="Dashboard agent" aria-hidden=${String(!this.open)} ?inert=${!this.open} @keydown=${this.handleKeydown}>
        <header class="header">
          <div class="toolbar">
            <div class="title">${agentIcon()}<span>Dashboard agent</span></div>
            <div class="toolbar-actions">
              <button class="text-action" type="button" title=${this.pending ? 'Wait for the current answer to finish' : agentEnabled ? 'New chat' : 'Agent is not configured'} aria-label="New chat" ?disabled=${!agentEnabled || this.pending} @click=${this.newChat}>${lucideIcon(Plus)}<span>New chat</span></button>
              <a class="text-action" href=${conversationHref} title="Open full chat" aria-label="Open full chat" aria-disabled=${String(this.pending && !agent.activeConversationId)} @click=${(event: MouseEvent) => { if (this.pending && !agent.activeConversationId) event.preventDefault() }}>${lucideIcon(ExternalLink)}<span>Full chat</span></a>
					  <button class="close-action" type="button" title="Close" aria-label="Close agent" @click=${this.closeDrawer}>${lucideIcon(X)}</button>
            </div>
          </div>
          <section class="context" aria-label="Included dashboard context">
            <div class="context-line">
              <span class="page-context">${context?.pageTitle || 'Current page'}</span>
              ${controls || selections ? html`<span class="context-separator" aria-hidden="true">·</span><span class="filter-context">${controls} ${controls === 1 ? 'filter' : 'filters'} · ${selections} ${selections === 1 ? 'selection' : 'selections'}</span>` : html`<span class="context-hint">· Page included</span>`}
            </div>
            ${this.referenceLimitMessage ? html`
              <div class="reference-limit-status" data-reference-limit-status role="status" aria-live="polite">${this.referenceLimitMessage}</div>
            ` : null}
          </section>
        </header>
        ${showWelcome ? html`
          <section class="welcome" aria-label="Start a dashboard conversation">
            <h2>What would you like to understand?</h2>
            <p>Ask about ${context?.exploration ? 'this data' : context?.dashboardTitle || 'this dashboard'}. Your current page, filters, and selections are included.</p>
            <div class="prompts">
              ${['Summarize the key takeaways on this page.', 'Explain how the main metrics are calculated.', 'Which results stand out, and why?'].map(prompt => html`<button class="prompt" @click=${() => this.fillPrompt(prompt)}>${prompt}</button>`)}
            </div>
            <p>Choose a question to edit, or use @ to attach a specific chart.</p>
          </section>
        ` : null}
        <lv-chat-thread ?hidden=${showWelcome}
          surface="drawer"
          .transcript=${agent.transcript ?? []}
          .visuals=${this.visuals}
          .status=${this.pending ? { ...agent.status, running: true } : agent.status}
          conversation-id=${agent.activeConversationId ?? ''}
          @lv-chat-reuse=${this.reuseDraft}
        ></lv-chat-thread>
        <lv-chat-composer
          .value=${agent.composer.value ?? ''}
          .disabled=${this.pending || agent.composer.disabled || !agentEnabled}
          .pending=${this.pending}
          .running=${Boolean(agent.status.running)}
          .runId=${agent.status.runId ?? ''}
          .canContinue=${Boolean(agent.status.canContinue)}
          .placeholder=${agentEnabled ? context?.exploration ? 'Ask about this data…' : 'Ask about this dashboard…' : 'Agent is not configured'}
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
        ></lv-chat-composer>
      </aside>
    `
  }

  private fillPrompt(prompt: string) {
    const composer = this.shadowRoot?.querySelector('lv-chat-composer') as (HTMLElement & { setDraft(value: string): void }) | null
    composer?.setDraft(prompt)
  }

  private newChat() {
    if (this.pending) return
		this.clearEditMessage()
    this.fillPrompt('')
    this.references = []
    this.referenceLimitMessage = ''
    this.notifyReferences()
		emitDomainEvent(this, domainEvents.chatNew, undefined)
  }

	private closeDrawer() {
		this.open = false
		emitDomainEvent(this, domainEvents.chatDrawerClose, undefined)
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
		this.shadowRoot?.querySelector<HTMLElement & { setDraft(value: string): void }>('lv-chat-composer')?.setDraft(detail.text ?? '')
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
