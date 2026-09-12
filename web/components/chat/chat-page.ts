import { LitElement, css, html } from 'lit'
import { state } from 'lit/decorators.js'
import { CircleHelp, LayoutDashboard, TrendingUp, type IconNode } from 'lucide'
import type { AgentContextSignal, AgentReferenceSearchSignal, AgentReferenceSignal, ChatConversationSummary, ChatPageSignal, ChatSignal, ChatTranscriptItemSignal } from '../../generated/signals'
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
  @state() private references: AgentReferenceSignal[] = []
	@state() private editMessageId = ''
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

    .main.list-main {
      height: auto;
      min-height: 100svh;
      grid-template-rows: minmax(0, 1fr);
      overflow: visible;
    }

    .main.new-main {
      grid-template-rows: minmax(0, 1fr);
    }

    .conversation-titlebar {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr);
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
      min-width: 0;
      min-height: 0;
      overflow: auto;
      background: var(--lv-bg-app);
    }

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
      gap: var(--lv-space-sm);
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
      display: grid;
      justify-items: center;
      gap: var(--lv-space-sm);
      text-align: center;
    }

    .new-chat-kicker {
      display: inline-flex;
      align-items: center;
      gap: var(--lv-space-sm);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    .agent-mark {
      display: grid;
      width: var(--lv-control-medium);
      height: var(--lv-control-medium);
      place-items: center;
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-accent-muted);
      color: var(--lv-accent);
    }

    .agent-mark svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .new-chat-title {
      max-width: 100%;
      font: var(--lv-type-page-title);
    }

    .new-chat-description {
      max-width: 520px;
      margin: calc(var(--lv-space-lg) + var(--lv-space-xs)) 0 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .prompt-starters {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: var(--lv-space-sm);
      margin-top: var(--lv-space-xs);
    }

    .prompt-starter {
      display: grid;
      min-width: 0;
      min-height: 92px;
      grid-template-columns: var(--lv-control-medium) minmax(0, 1fr);
      align-content: start;
      align-items: start;
      gap: var(--lv-space-sm);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      padding: var(--lv-space-md);
      text-align: left;
      cursor: pointer;
      transition:
        background var(--lv-transition-fast),
        border-color var(--lv-transition-fast),
        transform var(--lv-transition-fast);
    }

    .prompt-starter:hover:not(:disabled) {
      border-color: var(--lv-line-accent-muted);
      background: var(--lv-bg-control-hover);
      transform: translateY(-1px);
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
      width: var(--lv-control-medium);
      height: var(--lv-control-medium);
      place-items: center;
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-accent);
    }

    .prompt-starter-icon svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .prompt-starter-copy {
      display: grid;
      min-width: 0;
      gap: var(--lv-space-xs);
    }

    .prompt-starter-label {
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .prompt-starter-prompt {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      line-height: var(--base-text-lineHeight-normal);
    }

    .new-chat-context-hint {
      margin: var(--lv-space-md) 0 0;
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

      .prompt-starters {
        grid-template-columns: 1fr;
      }

      .prompt-starter {
        min-height: 0;
      }
    }
  `

  updated(): void {
    checkSignalContract('chat page', this.page, {
      title: 'required',
    })
    checkSignalContract('chat agent', this.agent, {
      transcript: 'required',
      status: 'required',
      composer: 'required',
    })
		this.syncEditState()
    this.navigateFromDraft()
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

  get agent(): ChatSignal {
    return this.signal<ChatSignal>('agent', emptyAgent)
  }

  get visuals(): Record<string, VisualizationEnvelope> {
    return this.signal<Record<string, VisualizationEnvelope>>('visuals', {})
  }

  get pending(): boolean {
    return this.signal<boolean>('agentTurnPending', false) || Boolean(this.agent.status?.running)
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
    const page = this.page
    const agent = this.agent ?? emptyAgent
    const status = agent.status ?? emptyAgent.status
    const composer = agent.composer ?? emptyAgent.composer
    const view = page?.view ?? 'conversation'
    const isList = view === 'list'
    const isNew = view === 'new'
    const title = conversationTitle(agent)
    return html`
      <div class="route">
        <section class=${['main', isList ? 'list-main' : '', isNew ? 'new-main' : ''].filter(Boolean).join(' ')} aria-label="LeapView chats">
          ${isList || isNew ? null : this.renderConversationTitlebar(title)}
          <div class="body">
            ${isList ? this.renderListView(agent) : isNew ? this.renderNewView(composer, status) : this.renderConversationView(agent, status, composer)}
          </div>
          <lv-visual-modal></lv-visual-modal>
        </section>
      </div>
    `
  }

  private renderConversationTitlebar(title: string) {
    return html`
      <div class="conversation-titlebar">
        <h1>${title}</h1>
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
            <div class="new-chat-kicker"><span class="agent-mark" aria-hidden="true">${agentIcon()}</span><span>LeapView Agent</span></div>
            <h1 id="new-chat-title" class="new-chat-title">Ask about your data</h1>
            <p class="new-chat-description">Get clear answers grounded in the dashboards, metrics, and models you can access.</p>
          </div>
          <div class="prompt-starters" aria-label="Example questions">
            ${promptStarters.map((starter) => html`
              <button class="prompt-starter" type="button" ?disabled=${disabled} @click=${() => this.selectPromptStarter(starter.prompt)}>
                <span class="prompt-starter-icon" aria-hidden="true">${lucideIcon(starter.icon, { size: 16, strokeWidth: 2 })}</span>
                <span class="prompt-starter-copy">
                  <span class="prompt-starter-label">${starter.label}</span>
                  <span class="prompt-starter-prompt">${starter.prompt}</span>
                </span>
              </button>
            `)}
          </div>
          <p class="new-chat-context-hint">Type <kbd>@</kbd> to attach a dashboard, metric, model, page, or visual.</p>
        </section>
        ${this.renderComposer(composer, status, true)}
      </div>
    `
  }

  private renderConversationView(agent: ChatSignal, status: ChatSignal['status'], composer: ChatSignal['composer']) {
    return html`
      <div class="thread-stack">
        <lv-chat-thread
          .transcript=${agent.transcript ?? []}
          .visuals=${this.visuals ?? {}}
          .status=${status}
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
