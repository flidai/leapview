import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ExternalLink, Plus, X } from 'lucide'
import type {
  AgentContextSignal,
	DashboardInteractionSelection,
	AgentReferenceSearchSignal,
	AgentReferenceSignal,
	ChatSignal,
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
  @property({ attribute: false }) suggestions: AgentReferenceSignal[] = []
  @state() private references: AgentReferenceSignal[] = []
  @state() private referenceLimitMessage = ''

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

    .close-action {
      margin-left: var(--lv-space-xs);
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

      .drawer {
        width: 100vw;
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
    this.open = true
		void this.updateComplete.then(() => {
			const composer = this.shadowRoot?.querySelector('lv-chat-composer')
			composer?.shadowRoot?.querySelector('textarea')?.focus()
		})
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
    if (!changed.has('open') || !this.open) return
    const composer = this.shadowRoot?.querySelector('lv-chat-composer') as (HTMLElement & { remeasure(): void }) | null
    composer?.remeasure()
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
		const workspaceSuggestions = searchResults.filter((reference) => !isOnPageReference(reference, context))
    const conversationHref = agent.activeConversationId
      ? `/chats/${encodeURIComponent(agent.activeConversationId)}`
      : '/chats/new'
    return html`
		<aside class="drawer" aria-label="Dashboard agent" aria-hidden=${String(!this.open)} ?inert=${!this.open}>
        <header class="header">
          <div class="toolbar">
            <div class="title">${agentIcon()}<span>Agent</span></div>
            <div class="toolbar-actions">
              <button type="button" title="New chat" aria-label="New chat" @click=${this.newChat}>${lucideIcon(Plus)}</button>
              <a href=${conversationHref} title="Open full chat" aria-label="Open full chat">${lucideIcon(ExternalLink)}</a>
					  <button class="close-action" type="button" title="Close" aria-label="Close agent" @click=${this.closeDrawer}>${lucideIcon(X)}</button>
            </div>
          </div>
          <section class="context" aria-label="Included dashboard context">
            <div class="context-line">
              <span class="page-context">${context?.pageTitle || 'Current page'}</span>
              <span class="context-separator" aria-hidden="true">·</span>
              <span class="filter-context">${controls} ${controls === 1 ? 'filter' : 'filters'} · ${selections} ${selections === 1 ? 'selection' : 'selections'}</span>
            </div>
            ${this.referenceLimitMessage ? html`
              <div class="reference-limit-status" data-reference-limit-status role="status" aria-live="polite">${this.referenceLimitMessage}</div>
            ` : null}
          </section>
        </header>
        <lv-chat-thread
          surface="drawer"
          .transcript=${agent.transcript ?? []}
          .visuals=${this.visuals}
          .status=${agent.status}
          conversation-id=${agent.activeConversationId ?? ''}
        ></lv-chat-thread>
        <lv-chat-composer
          .value=${agent.composer.value ?? ''}
          .disabled=${this.pending || agent.composer.disabled}
          .pending=${this.pending}
          .placeholder=${agent.composer.placeholder || 'Ask about this dashboard…'}
          .references=${this.references}
          .referenceLimit=${context?.referenceLimit ?? defaultAgentReferenceLimit}
          .pinnedSuggestions=${pinnedSuggestions}
          .suggestions=${workspaceSuggestions}
          .suggestionQuery=${this.referenceSearch.query}
          .suggestionRequestId=${this.referenceSearch.requestId}
			.acceptedRunId=${latestAcceptedRunId(agent.transcript ?? [])}
          @lv-chat-references-change=${this.referencesChanged}
        ></lv-chat-composer>
      </aside>
    `
  }

  private newChat() {
    this.references = []
    this.referenceLimitMessage = ''
    this.notifyReferences()
		emitDomainEvent(this, domainEvents.chatNew, undefined)
  }

	private closeDrawer() {
		this.open = false
		emitDomainEvent(this, domainEvents.chatDrawerClose, undefined)
	}

	private referencesChanged(event: CustomEvent<ChatReferencesChangeDetail>) {
    this.references = event.detail.references ?? []
    this.referenceLimitMessage = ''
    this.notifyReferences()
  }

  private normalizedReferenceLimit(): number {
		return normalizeReferenceLimit(this.context?.referenceLimit ?? defaultAgentReferenceLimit)
  }

  private notifyReferences() {
    emitDomainEvent<ChatReferencesChangeDetail>(this, domainEvents.agentReferencesChange, { references: this.references })
  }
}

if (!customElements.get('lv-chat-drawer')) customElements.define('lv-chat-drawer', ChatDrawer)
