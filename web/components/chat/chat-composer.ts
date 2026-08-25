import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Search, Send, X } from 'lucide'
import { domainEvents, emitDomainEvent } from '../shared/events'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/loading-spinner'
import {
  type ChatContextReference,
  type ChatReferenceSearchDetail,
  type ChatReferencesChangeDetail,
  defaultAgentReferenceLimit,
  matchesReferenceQuery,
  normalizeReferenceLimit,
  normalizedReferenceQuery,
  referenceIcon,
	referenceHierarchy,
  referenceIdentity,
  referenceKindLabel,
  uniqueReferences,
} from './reference'

const maxPinnedMentionSuggestions = 8
const maxGlobalMentionSuggestions = 8

class ChatComposer extends LitElement {
  @property({ type: String }) value = ''
  @property({ type: Boolean, reflect: true }) disabled = false
  @property({ type: Boolean, reflect: true }) pending = false
  @property({ type: String }) placeholder = 'Ask about dashboards, metrics, or models...'
	@property({ attribute: false }) references: ChatContextReference[] = []
	@property({ attribute: false }) pinnedSuggestions: ChatContextReference[] = []
	@property({ attribute: false }) suggestions: ChatContextReference[] = []
  @property({ type: Number, attribute: 'reference-limit' }) referenceLimit = defaultAgentReferenceLimit
  @property({ type: String, attribute: false }) suggestionQuery = ''
  @property({ type: Number, attribute: false }) suggestionRequestId = 0
	@property({ type: String, attribute: false }) acceptedRunId = ''
  @state() private draft = ''
	@state() private mentionIndex = 0
	@state() private mentionSearchPending = false
	@state() private acceptedSuggestions: ChatContextReference[] = []
  private lastSearchQuery: string | null = null
  private latestSearchRequestId = 0
	private acceptedSuggestionQuery = ''
	private acceptedSuggestionRequestId = 0
  private resizeObserver?: ResizeObserver
  private observedWidth = -1
	private acceptedRunInitialized = false

  static styles = css`
    :host {
      position: relative;
      display: block;
      background: linear-gradient(to bottom, transparent, var(--lv-bg-app) var(--lv-space-lg));
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    form {
			position: relative;
      width: min(calc(100% - var(--lv-space-lg) - var(--lv-space-lg)), var(--lv-chat-stack-width));
      margin-inline: auto;
      padding: calc(var(--lv-space-lg) + var(--lv-space-sm)) var(--lv-space-lg) var(--lv-space-lg);
    }

    .composer-surface {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: end;
      gap: var(--lv-space-sm);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      padding: var(--lv-space-sm);
      box-shadow: none;
      cursor: text;
      transition:
        background var(--lv-transition-fast),
        border-color var(--lv-transition-fast),
        box-shadow var(--lv-transition-fast);
    }

    .composer-surface:hover:not(.is-disabled) {
      border-color: var(--lv-line-muted);
      box-shadow: none;
    }

    .composer-surface:focus-within {
      border-color: var(--lv-line-accent-muted);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-bg-accent-muted);
    }

    .composer-surface.is-disabled {
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      box-shadow: none;
      cursor: not-allowed;
    }

    textarea {
      box-sizing: border-box;
      min-height: calc(var(--lv-control-large) + var(--lv-space-sm));
      max-height: 160px;
      width: 100%;
      grid-column: 1;
      grid-row: 1;
      resize: none;
      overflow-y: auto;
      border: 0;
      border-radius: calc(var(--lv-radius-default) - var(--lv-space-2xs));
      background: transparent;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      padding: var(--lv-space-xs) var(--lv-space-sm);
      outline: 0;
    }

    textarea:focus {
      outline: 0;
    }

    textarea::placeholder {
      color: var(--lv-fg-muted);
    }

    .actions {
      display: flex;
      grid-column: 2;
      grid-row: 1;
      min-height: var(--lv-control-medium);
      align-items: center;
      justify-content: flex-end;
    }

		.mention-picker {
			display: grid;
			position: absolute;
			inset: auto var(--lv-space-lg) calc(100% - var(--lv-space-lg) - var(--lv-space-sm));
			z-index: var(--zIndex-dropdown);
			max-height: 180px;
			overflow: auto;
			border: var(--lv-border-muted);
			border-radius: var(--lv-radius-large);
			background: var(--lv-bg-panel);
			padding: var(--lv-space-xs);
			box-shadow: var(--lv-shadow-floating-sm);
		}

		.mention-option {
			display: grid;
			width: 100%;
			height: auto;
			min-height: var(--lv-control-small);
			grid-template-columns: 16px minmax(0, 1fr);
			align-items: center;
			gap: var(--lv-space-xs);
			border: 0;
			border-radius: var(--lv-radius-default);
			background: transparent;
			color: var(--lv-fg-default);
			padding: var(--lv-space-2xs) var(--lv-space-sm);
			box-shadow: none;
			text-align: left;
		}

		.mention-group {
			display: grid;
		}

		.mention-section-label {
			min-width: 0;
			padding: var(--lv-space-xs) var(--lv-space-sm) var(--lv-space-2xs);
			color: var(--lv-fg-muted);
			font: var(--lv-type-caption);
		}

		.mention-icon {
			display: grid;
			width: 16px;
			height: 16px;
			place-items: center;
			color: var(--lv-fg-muted);
		}

		.mention-icon svg {
			width: 14px;
			height: 14px;
		}

		.mention-copy {
			display: grid;
			min-width: 0;
			align-items: baseline;
			grid-template-columns: minmax(0, 2fr) minmax(0, 3fr) 88px;
			gap: var(--lv-space-sm);
		}

		.mention-title,
		.mention-hierarchy,
		.mention-type {
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.mention-hierarchy,
		.mention-type {
			min-width: 0;
			color: var(--lv-fg-muted);
			font: var(--lv-type-caption);
		}

		.mention-type {
			text-align: right;
		}

		.mention-status {
			display: flex;
			min-height: var(--lv-control-small);
			align-items: center;
			gap: var(--lv-space-sm);
			padding: var(--lv-space-2xs) var(--lv-space-sm);
			color: var(--lv-fg-muted);
			font: var(--lv-type-caption);
		}

		.mention-status svg {
			width: 14px;
			height: 14px;
		}

		.selected-references {
			display: flex;
			grid-column: 1 / -1;
			grid-row: 1;
			flex-wrap: wrap;
			gap: var(--lv-space-xs);
			padding: var(--lv-space-xs) var(--lv-space-sm) 0;
		}

		.reference-chip {
			display: inline-flex;
			width: auto;
			height: 24px;
			max-width: 100%;
			align-items: center;
			gap: var(--lv-space-xs);
			border: 0;
			border-radius: var(--lv-radius-full);
			background: var(--lv-bg-control);
			color: var(--lv-fg-default);
			padding: 0 var(--lv-space-sm);
			font: var(--lv-type-caption);
			cursor: pointer;
		}

		.reference-chip svg {
			width: 12px;
			height: 12px;
		}

		.composer-surface:has(.selected-references) textarea,
		.composer-surface:has(.selected-references) .actions {
			grid-row: 2;
		}

		.mention-option[data-active='true'],
		.mention-option:hover {
			background: var(--lv-bg-control-hover);
			transform: none;
		}

    .send-button {
      display: inline-flex;
      width: var(--lv-button-height, var(--lv-control-medium));
      height: var(--lv-button-height, var(--lv-control-medium));
      min-width: var(--lv-button-height, var(--lv-control-medium));
      align-items: center;
      justify-content: center;
      border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-accent-border-rest, var(--lv-accent));
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      background: var(--lv-button-accent-bg-rest, var(--lv-accent));
      color: var(--lv-button-accent-fg-rest, var(--lv-accent-fg));
      cursor: pointer;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      padding: 0;
      box-shadow: var(--lv-button-shadow-resting, var(--shadow-resting-small));
      transition:
        background var(--duration-fast) var(--ease-lv),
        border-color var(--duration-fast) var(--ease-lv),
        color var(--duration-fast) var(--ease-lv),
        transform var(--duration-fast) var(--ease-lv);
    }

    .send-button svg {
      width: var(--lv-button-icon-size, var(--base-size-16));
      height: var(--lv-button-icon-size, var(--base-size-16));
    }

    .send-button:hover:not(:disabled) {
      border-color: var(--lv-button-accent-border-hover, var(--lv-accent));
      background: var(--lv-button-accent-bg-hover, var(--lv-accent));
      transform: translateY(-1px);
    }

    .send-button:focus-visible {
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: var(--focus-outline-offset, var(--lv-space-xs));
    }

    .send-button:disabled {
      border-color: var(--lv-button-accent-border-disabled, var(--lv-line-default));
      background: var(--lv-button-accent-bg-disabled, var(--lv-bg-control));
      color: var(--lv-button-accent-fg-disabled, var(--lv-fg-muted));
      cursor: not-allowed;
      opacity: 1;
      box-shadow: none;
    }

    textarea:disabled {
      cursor: not-allowed;
      color: var(--lv-fg-muted);
      opacity: 1;
    }
    @media (max-width: 560px) {
      form {
        width: min(calc(100% - var(--lv-space-md) - var(--lv-space-md)), var(--lv-chat-stack-width));
        padding: calc(var(--lv-space-lg) + var(--lv-space-sm)) var(--lv-space-md) var(--lv-space-md);
      }
    }

    @media (pointer: coarse) {
      .actions {
        min-height: calc(var(--lv-control-large) + var(--lv-space-xs));
      }

      .send-button {
        --lv-button-height: calc(var(--lv-control-large) + var(--lv-space-xs));
      }
    }
  `

	protected willUpdate(changed: Map<string, unknown>) {
		if (!changed.has('acceptedRunId')) return
		if (this.acceptedRunInitialized && this.acceptedRunId !== changed.get('acceptedRunId')) {
			this.consumeAcceptedTurn()
		}
		this.acceptedRunInitialized = true
	}

  updated(changed: Map<string, unknown>) {
    if (changed.has('value')) {
		if (this.value) this.draft = this.value
		void this.updateComplete.then(() => this.resizeTextarea())
    }
		if (
			(changed.has('suggestions') || changed.has('suggestionQuery') || changed.has('suggestionRequestId'))
			&& this.mentionSearchPending
			&& this.isCurrentSuggestionResponse()
		) {
			this.acceptedSuggestions = [...this.suggestions]
			this.acceptedSuggestionQuery = this.suggestionQuery
			this.acceptedSuggestionRequestId = this.suggestionRequestId
			this.mentionSearchPending = false
		}
  }

  connectedCallback() {
    super.connectedCallback()
    this.draft = this.value || ''
  }

  protected firstUpdated() {
    this.resizeTextarea()
    this.resizeObserver = new ResizeObserver(([entry]) => {
      const width = Math.round(entry?.contentRect.width ?? 0)
      if (width === this.observedWidth) return
      this.observedWidth = width
      this.resizeTextarea()
    })
    this.resizeObserver.observe(this)
  }

  disconnectedCallback() {
    this.resizeObserver?.disconnect()
    this.resizeObserver = undefined
    super.disconnectedCallback()
  }

  public remeasure(): void {
    this.resizeTextarea()
  }

  render() {
    const blocked = this.disabled || this.pending
		const activeMention = this.activeMention()
		const mentionGroups = this.mentionSuggestionGroups()
		const mentions = [...mentionGroups.pinned, ...mentionGroups.global]
		const referenceLimitReached = this.referenceLimitReached()
    return html`
      <form @submit=${this.submit}>
			${activeMention ? html`
				<div class="mention-picker" role="listbox" aria-label="Add LeapView context" aria-busy=${String(this.mentionSearchPending)}>
					${mentionGroups.pinned.length > 0 ? html`
						<div class="mention-group" role="group" aria-label="On this page">
							<div class="mention-section-label">On this page</div>
							${mentionGroups.pinned.map((reference, index) => this.renderMentionOption(reference, index))}
						</div>
					` : null}
					${mentionGroups.global.length > 0 ? html`
						<div class="mention-group" role="group" aria-label=${mentionGroups.pinned.length > 0 ? 'All accessible' : 'Results'}>
							${mentionGroups.pinned.length > 0 ? html`<div class="mention-section-label">All accessible</div>` : null}
							${mentionGroups.global.map((reference, index) => this.renderMentionOption(reference, mentionGroups.pinned.length + index))}
						</div>
					` : null}
					${mentions.length === 0 ? html`
						<div class="mention-status" role="status">
							${lucideIcon(Search)}<span>${referenceLimitReached
								? `Up to ${this.normalizedReferenceLimit()} items can be attached`
								: this.mentionSearchPending ? 'Searching…' : 'No matching context'}</span>
						</div>
					` : null}
				</div>
			` : null}
        <div
			class=${['composer-surface', blocked ? 'is-disabled' : ''].filter(Boolean).join(' ')}
			@click=${this.focusComposer}
		>
			${this.references.length ? html`
				<div class="selected-references" aria-label="Attached context">
					${this.references.map((reference) => html`
						<button class="reference-chip" type="button" title="Remove ${reference.name}" @click=${() => this.removeReference(reference)}>
							${referenceIcon(reference.reference.kind)}<span>${reference.name}</span>${lucideIcon(X)}
						</button>
					`)}
				</div>
			` : null}
          <textarea
            .value=${this.draft}
            ?disabled=${this.disabled}
            aria-label=${this.placeholder.replace(/\.{3}$/, '') || 'Ask about dashboards, metrics, or models'}
            placeholder=${this.placeholder}
            rows="1"
            @input=${this.input}
            @keydown=${this.keydown}
          ></textarea>
          <div class="actions">
            <button
						class="send-button"
              type="submit"
              aria-label=${this.pending ? 'Sending' : 'Send'}
              title="Send"
              ?disabled=${this.disabled || this.pending || this.draft.trim() === ''}
            >
              ${this.pending ? html`<lv-loading-spinner aria-hidden="true"></lv-loading-spinner>` : lucideIcon(Send)}
            </button>
          </div>
        </div>
      </form>
    `
  }

  private input(event: Event) {
    const textarea = event.target as HTMLTextAreaElement
    this.draft = textarea.value
		this.mentionIndex = 0
		const mention = this.activeMention(textarea)
		this.requestMentionSearch(mention?.query ?? null)
    this.resizeTextarea(textarea)
  }

	private keydown(event: KeyboardEvent) {
		const mention = this.activeMention()
		const mentions = this.mentionSuggestions()
		const touchPrimary = window.matchMedia('(pointer: coarse)').matches
		if (mention) {
			if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
				if (mentions.length === 0) return
				event.preventDefault()
				const direction = event.key === 'ArrowDown' ? 1 : -1
				this.mentionIndex = (this.mentionIndex + direction + mentions.length) % mentions.length
				void this.updateComplete.then(() => this.scrollActiveMentionIntoView())
				return
			}
			if (event.key === 'Enter' && !event.shiftKey && !touchPrimary && mentions.length > 0) {
				event.preventDefault()
				this.selectMention(mentions[this.mentionIndex] ?? mentions[0])
				return
			}
			if (event.key === 'Escape') {
				event.preventDefault()
				this.removeActiveMention()
				return
			}
		}
    if (event.key !== 'Enter' || event.shiftKey || touchPrimary) return
    event.preventDefault()
    this.dispatchSubmit()
  }

	private focusComposer(event: MouseEvent) {
		const target = event.target
		if (target instanceof Element && target.closest('button, textarea, a, input, select')) return
		this.shadowRoot?.querySelector<HTMLTextAreaElement>('textarea')?.focus()
	}

  private submit(event: Event) {
    event.preventDefault()
    this.dispatchSubmit()
  }

  private dispatchSubmit() {
    const input = this.draft.trim()
    if (this.disabled || this.pending || input === '') return
    emitDomainEvent(this, domainEvents.chatSubmit, { input, references: this.references })
  }

	private consumeAcceptedTurn() {
		this.draft = ''
		this.references = []
		this.mentionIndex = 0
		this.mentionSearchPending = false
		this.lastSearchQuery = null
		this.notifyReferences()
		void this.updateComplete.then(() => this.resizeTextarea())
	}

	private mentionSuggestions(): ChatContextReference[] {
		const groups = this.mentionSuggestionGroups()
		return [...groups.pinned, ...groups.global]
	}

	private mentionSuggestionGroups(): { pinned: ChatContextReference[]; global: ChatContextReference[] } {
		const mention = this.activeMention()
		if (!mention || this.referenceLimitReached()) return { pinned: [], global: [] }
		const query = mention.query.toLocaleLowerCase()
		const selected = new Set(this.references.map(referenceIdentity))
		const pinnedCandidates = uniqueReferences(this.pinnedSuggestions)
			.filter((reference) => !selected.has(referenceIdentity(reference)))
			.filter((reference) => matchesReferenceQuery(reference, query))
		const pinned = pinnedCandidates.slice(0, maxPinnedMentionSuggestions)
		const excluded = new Set([...selected, ...pinnedCandidates.map(referenceIdentity)])
		const global = uniqueReferences(this.currentSuggestionCandidates())
			.filter((reference) => !excluded.has(referenceIdentity(reference)))
			.filter((reference) => matchesReferenceQuery(reference, query))
			.slice(0, maxGlobalMentionSuggestions)
		return { pinned, global }
	}

	private renderMentionOption(reference: ChatContextReference, index: number) {
						const kindLabel = referenceKindLabel(reference.reference.kind)
		const hierarchy = referenceHierarchy(reference).join(' › ')
		return html`
			<button
				type="button"
				class="mention-option"
				role="option"
				aria-label=${[reference.name, hierarchy, kindLabel].filter(Boolean).join(', ')}
				aria-selected=${String(index === this.mentionIndex)}
				data-active=${String(index === this.mentionIndex)}
				@mousedown=${(event: MouseEvent) => event.preventDefault()}
				@click=${() => this.selectMention(reference)}
			>
								<span class="mention-icon" aria-hidden="true">${referenceIcon(reference.reference.kind, reference.visualType)}</span>
				<span class="mention-copy">
					<span class="mention-title">${reference.name}</span>
					<span class="mention-hierarchy">${hierarchy}</span>
					<span class="mention-type">${kindLabel}</span>
				</span>
			</button>
		`
	}

	private selectMention(reference: ChatContextReference | undefined) {
		if (!reference || this.referenceLimitReached()) return
		this.removeActiveMention()
		if (!this.references.some((current) => referenceIdentity(current) === referenceIdentity(reference))) {
			this.references = [...this.references, reference]
		}
		this.mentionIndex = 0
		this.lastSearchQuery = null
		emitDomainEvent<ChatReferencesChangeDetail>(this, domainEvents.chatReferencesChange, { references: this.references })
		void this.updateComplete.then(() => this.shadowRoot?.querySelector('textarea')?.focus())
	}

	private removeReference(reference: ChatContextReference) {
		this.references = this.references.filter((current) => referenceIdentity(current) !== referenceIdentity(reference))
		this.notifyReferences()
		this.requestMentionSearch(this.activeMention()?.query ?? null)
	}

	private requestMentionSearch(query: string | null) {
		if (query === null || this.referenceLimitReached()) {
			this.lastSearchQuery = null
			this.mentionSearchPending = false
			return
		}
		if (query === this.lastSearchQuery) return
		this.lastSearchQuery = query
		this.mentionSearchPending = true
		this.latestSearchRequestId += 1
		this.acceptedSuggestions = []
		this.acceptedSuggestionQuery = ''
		this.acceptedSuggestionRequestId = 0
		emitDomainEvent<ChatReferenceSearchDetail>(this, domainEvents.chatReferenceSearch, {
			query,
			requestId: this.latestSearchRequestId,
		})
	}

	private notifyReferences() {
		emitDomainEvent<ChatReferencesChangeDetail>(this, domainEvents.chatReferencesChange, { references: this.references })
	}

	private isCurrentSuggestionResponse(): boolean {
		if (this.suggestionRequestId === 0 && this.suggestionQuery === '') return true
		const mention = this.activeMention()
		return Boolean(
			mention
			&& this.suggestionRequestId === this.latestSearchRequestId
			&& normalizedReferenceQuery(this.suggestionQuery) === normalizedReferenceQuery(mention.query),
		)
	}

	private currentSuggestionCandidates(): ChatContextReference[] {
		if (this.suggestionRequestId === 0 && this.suggestionQuery === '') return this.suggestions
		if (this.isCurrentSuggestionResponse()) return this.suggestions
		const mention = this.activeMention()
		if (!mention) return []
		return this.acceptedSuggestionRequestId === this.latestSearchRequestId
			&& normalizedReferenceQuery(this.acceptedSuggestionQuery) === normalizedReferenceQuery(mention.query)
			? this.acceptedSuggestions
			: []
	}

	private activeMention(textarea = this.shadowRoot?.querySelector('textarea') as HTMLTextAreaElement | null): { start: number; end: number; query: string } | null {
		const end = textarea?.selectionStart ?? this.draft.length
		const beforeCaret = this.draft.slice(0, end)
		const match = beforeCaret.match(/(?:^|\s)@([^@\n]*)$/)
		if (!match) return null
		const start = beforeCaret.lastIndexOf('@')
		return { start, end, query: (match[1] ?? '').trim() }
	}

	private removeActiveMention() {
		const textarea = this.shadowRoot?.querySelector('textarea') as HTMLTextAreaElement | null
		const mention = this.activeMention(textarea)
		if (!mention) return
		const before = this.draft.slice(0, mention.start).replace(/\s+$/, '')
		const after = this.draft.slice(mention.end)
		this.draft = before + after
		this.mentionSearchPending = false
		this.lastSearchQuery = null
		void this.updateComplete.then(() => {
			const next = this.shadowRoot?.querySelector('textarea') as HTMLTextAreaElement | null
			if (!next) return
			next.setSelectionRange(before.length, before.length)
			next.focus()
		})
	}

	private scrollActiveMentionIntoView() {
		const active = this.shadowRoot?.querySelector<HTMLElement>('.mention-option[data-active="true"]')
		active?.scrollIntoView({ block: 'nearest' })
	}

	private normalizedReferenceLimit(): number {
		return normalizeReferenceLimit(this.referenceLimit)
	}

	private referenceLimitReached(): boolean {
		return this.references.length >= this.normalizedReferenceLimit()
	}

  private resizeTextarea(textarea = this.shadowRoot?.querySelector('textarea') as HTMLTextAreaElement | null) {
    if (!textarea) return
    if (textarea.getBoundingClientRect().width <= 0) {
      textarea.style.height = ''
      return
    }
    const maxHeight = 160
    textarea.style.height = 'auto'
    const height = Math.min(textarea.scrollHeight, maxHeight)
    textarea.style.height = `${height}px`
    textarea.style.overflowY = textarea.scrollHeight > maxHeight ? 'auto' : 'hidden'
  }
}

if (!customElements.get('lv-chat-composer')) customElements.define('lv-chat-composer', ChatComposer)
