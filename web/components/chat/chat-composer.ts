import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { AtSign, FileText, Paperclip, Search, Send, Square, X } from 'lucide'
import { attachedMessage, readAttachedMessage, readChatFile, maxAttachmentCount, maxAttachmentCharacters, type ChatFileAttachment } from './attachments'
import { domainEvents, emitDomainEvent } from '../shared/events'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/loading-spinner'
import { chatComposerStyles } from './chat-composer-styles'
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
const continueResponsePrompt = 'Continue your previous response from where you stopped.'

class ChatComposer extends LitElement {
  @property({ type: String }) value = ''
  @property({ type: Boolean, reflect: true }) disabled = false
  @property({ type: Boolean, reflect: true }) pending = false
  @property({ type: Boolean, reflect: true }) running = false
  @property({ type: Boolean, reflect: true }) canContinue = false
  @property({ type: String, attribute: false }) runId = ''
  @property({ type: String }) placeholder = 'Ask about dashboards, metrics, or models...'
	@property({ attribute: false }) references: ChatContextReference[] = []
	@property({ attribute: false }) pinnedSuggestions: ChatContextReference[] = []
	@property({ attribute: false }) suggestions: ChatContextReference[] = []
  @property({ type: Number, attribute: 'reference-limit' }) referenceLimit = defaultAgentReferenceLimit
  @property({ type: String, attribute: false }) suggestionQuery = ''
	@property({ type: Number, attribute: false }) suggestionRequestId = 0
	@property({ type: String, attribute: false }) acceptedRunId = ''
	@property({ type: String, attribute: 'edit-message-id' }) editMessageId = ''
	@property({ type: Boolean, reflect: true }) editing = false
  @state() private draft = ''
  @state() private files: ChatFileAttachment[] = []
  @state() private readingFiles = false
  @state() private fileError = ''
  @state() private draggingFiles = false
  private fileDropTarget?: EventTarget
  private dragDepth = 0
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

  static styles = chatComposerStyles

	protected willUpdate(changed: Map<string, unknown>) {
		if (!changed.has('acceptedRunId')) return
		if (this.acceptedRunInitialized && this.acceptedRunId && this.acceptedRunId !== changed.get('acceptedRunId')) {
			this.consumeAcceptedTurn()
		}
		this.acceptedRunInitialized = true
	}

  updated(changed: Map<string, unknown>) {
    if (changed.has('value')) {
		if (this.value) {
			const message = readAttachedMessage(this.value)
			this.draft = message.text
			if (message.files.length) this.files = message.files
		}
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
    const root = this.getRootNode()
    this.fileDropTarget = root instanceof ShadowRoot ? root.host : this
    this.fileDropTarget.addEventListener('dragenter', this.fileDragEnter as EventListener)
    this.fileDropTarget.addEventListener('dragleave', this.fileDragLeave as EventListener)
    this.fileDropTarget.addEventListener('dragover', this.fileDragOver as EventListener)
    this.fileDropTarget.addEventListener('drop', this.dropFiles as EventListener)
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
    this.fileDropTarget?.removeEventListener('dragenter', this.fileDragEnter as EventListener)
    this.fileDropTarget?.removeEventListener('dragleave', this.fileDragLeave as EventListener)
    this.fileDropTarget?.removeEventListener('dragover', this.fileDragOver as EventListener)
    this.fileDropTarget?.removeEventListener('drop', this.dropFiles as EventListener)
    this.resizeObserver?.disconnect()
    this.resizeObserver = undefined
    super.disconnectedCallback()
  }

  public remeasure(): void {
    this.resizeTextarea()
  }

  public focusInput(): void {
    this.shadowRoot?.querySelector<HTMLTextAreaElement>('textarea')?.focus()
  }

  public setDraft(value: string, focus = true): void {
    const message = readAttachedMessage(value)
    this.draft = message.text
    this.files = message.files
    this.mentionIndex = 0
    this.mentionSearchPending = false
    this.lastSearchQuery = null
    void this.updateComplete.then(() => {
      const textarea = this.shadowRoot?.querySelector<HTMLTextAreaElement>('textarea')
      if (!textarea) return
      if (focus) {
        textarea.focus()
        textarea.setSelectionRange(value.length, value.length)
      }
      this.resizeTextarea(textarea)
    })
  }

  /** Read the current draft edited by the user or set through setDraft(). */
  public getDraft(): string {
    return this.draft
  }

  render() {
		const blocked = this.disabled || this.pending
		const isEditing = this.editing || Boolean(this.editMessageId.trim())
		const showStop = this.running
		const stopDisabled = !this.runId.trim()
		const continueDisabled = this.disabled || this.pending || this.running || this.readingFiles || this.files.length > 0 || isEditing || this.draft.trim() !== ''
		const activeMention = this.activeMention()
		const mentionGroups = this.mentionSuggestionGroups()
		const mentions = [...mentionGroups.pinned, ...mentionGroups.global]
		const referenceLimitReached = this.referenceLimitReached()
    return html`
      <form @submit=${this.submit}>
        <input class="file-input" type="file" multiple accept=".pdf,.txt,.md,.csv,.json,.log" aria-label="Choose files to attach" @change=${this.chooseFiles} />
        ${this.draggingFiles ? html`<div class="file-drop-hint" role="status">Drop files to attach</div>` : nothing}
        ${this.files.length ? html`<div class="attached-files" aria-label="Attached files">${this.files.map(file=>html`<span class="file-chip">${lucideIcon(FileText)}<span title=${file.name}>${file.name}</span><button type="button" aria-label=${`Remove ${file.name}`} ?disabled=${this.pending} @click=${()=>{this.files=this.files.filter(item=>item.id!==file.id)}}>${lucideIcon(X)}</button></span>`)}</div>` : nothing}
        ${this.readingFiles ? html`<p class="file-status" role="status">Reading files…</p>` : nothing}
        ${this.fileError ? html`<p class="file-error" role="alert">${this.fileError}</p>` : nothing}
			${isEditing ? html`
				<div class="edit-banner" role="status" aria-label="Editing message">
					<span>Editing message</span>
					<button class="cancel-edit" type="button" ?disabled=${blocked} @click=${this.cancelEdit}>Cancel</button>
				</div>
			` : null}
			${activeMention ? html`
				<div id="chat-context-options" class="mention-picker" role="listbox" aria-label="Add LeapView context" aria-busy=${String(this.mentionSearchPending)}>
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
            role="combobox"
            aria-autocomplete="list"
            aria-haspopup="listbox"
            aria-expanded=${String(Boolean(activeMention))}
            aria-controls=${activeMention ? 'chat-context-options' : nothing}
            aria-activedescendant=${activeMention && mentions.length > 0 ? this.mentionOptionID(this.mentionIndex) : nothing}
            placeholder=${this.placeholder}
            rows="1"
            @input=${this.input}
            @keydown=${this.keydown}
          ></textarea>
          <div class="actions">
            <button class="context-button" type="button" aria-label="Add files" title="Add files" ?disabled=${this.pending || this.running || this.readingFiles} @click=${()=>this.shadowRoot?.querySelector<HTMLInputElement>('.file-input')?.click()}>${lucideIcon(Paperclip)}</button>
            <button
              class="context-button context-picker-button"
              type="button"
              aria-label="Add context"
              title="Add context"
              ?disabled=${blocked || referenceLimitReached}
              @click=${this.openContextPicker}
            >
              ${lucideIcon(AtSign)}
            </button>
            ${showStop ? html`
              <button
                class="send-button stop-button"
                type="button"
                aria-label="Stop response"
                title="Stop response"
                ?disabled=${stopDisabled}
                @click=${this.stop}
              >
                ${lucideIcon(Square)}
              </button>
            ` : html`
              <button
						class=${['send-button', isEditing ? 'is-editing' : ''].filter(Boolean).join(' ')}
                type="submit"
				  aria-label=${this.pending ? 'Sending' : isEditing ? 'Save & send' : 'Send'}
				  title=${this.pending ? 'Sending' : isEditing ? 'Save & send' : 'Send'}
                ?disabled=${this.disabled || this.pending || this.readingFiles || (this.draft.trim() === '' && !this.files.length)}
              >
							${this.pending ? html`<lv-loading-spinner size="small" aria-hidden="true"></lv-loading-spinner>` : lucideIcon(Send)}
							${isEditing ? html`<span>Save &amp; send</span>` : null}
              </button>
            `}
          </div>
        </div>
        ${this.canContinue ? html`
          <div class="continuation-action">
            <button
              class="continue-button"
              type="button"
              aria-label="Continue response"
              title=${continueDisabled ? 'Clear your draft to continue the previous response' : 'Continue response'}
              ?disabled=${continueDisabled}
              @click=${this.continueResponse}
            >
              Continue response
            </button>
          </div>
        ` : null}
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

  private hasFiles(event: DragEvent): boolean {
    return Boolean(event.dataTransfer?.types.includes('Files'))
  }

  private fileDragEnter = (event: DragEvent): void => {
    if (!this.hasFiles(event)) return
    event.preventDefault()
    this.dragDepth++
    this.draggingFiles = !this.pending && !this.running
  }

  private fileDragLeave = (event: DragEvent): void => {
    if (!this.hasFiles(event)) return
    this.dragDepth = Math.max(0, this.dragDepth - 1)
    if (!this.dragDepth) this.draggingFiles = false
  }

  private fileDragOver = (event: DragEvent): void => {
    if (!this.hasFiles(event)) return
    event.preventDefault()
    if (event.dataTransfer) event.dataTransfer.dropEffect = this.pending || this.running ? 'none' : 'copy'
  }

  private dropFiles = (event: DragEvent): void => {
    if (!this.hasFiles(event)) return
    event.preventDefault()
    event.stopPropagation()
    this.dragDepth = 0
    this.draggingFiles = false
    void this.addFiles(Array.from(event.dataTransfer?.files ?? []))
  }

  private chooseFiles = (event: Event): void => {
    const input = event.target as HTMLInputElement
    void this.addFiles(Array.from(input.files ?? []))
    input.value = ''
  }

  private async addFiles(files: File[]): Promise<void> {
    if (this.pending || this.running || this.readingFiles) return
    this.fileError = ''
    this.readingFiles = true
    const errors: string[] = []
    try {
      for (const file of files) {
        if (this.files.length >= maxAttachmentCount) {
          errors.push(`Attach up to ${maxAttachmentCount} files at a time.`)
          break
        }
        if (this.files.some(existing => existing.name === file.name && existing.size === file.size)) continue
        try {
          const attachment = await readChatFile(file)
          if (this.files.reduce((total, item) => total + item.text.length, 0) + attachment.text.length > maxAttachmentCharacters) {
            throw new Error('The attached files contain too much text. Remove a file or use shorter excerpts.')
          }
          this.files = [...this.files, attachment]
        } catch (error) {
          errors.push(error instanceof Error ? error.message : `Could not read ${file.name}.`)
        }
      }
    } finally {
      this.fileError = errors.join(' ')
      this.readingFiles = false
    }
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

	private openContextPicker = (): void => {
		if (this.disabled || this.pending || this.referenceLimitReached()) return
		const textarea = this.shadowRoot?.querySelector<HTMLTextAreaElement>('textarea')
		if (!textarea) return
		const caret = textarea.selectionStart ?? this.draft.length
		const prefix = this.draft.slice(0, caret)
		const suffix = this.draft.slice(caret)
		const separator = prefix.length > 0 && !/\s$/.test(prefix) ? ' ' : ''
		const nextCaret = prefix.length + separator.length + 1
		this.draft = `${prefix}${separator}@${suffix}`
		textarea.value = this.draft
		textarea.focus()
		textarea.setSelectionRange(nextCaret, nextCaret)
		this.mentionIndex = 0
		this.requestMentionSearch('')
		this.resizeTextarea(textarea)
	}

  private submit(event: Event) {
    event.preventDefault()
    this.dispatchSubmit()
  }

	private dispatchSubmit() {
    const input = attachedMessage(this.draft.trim(), this.files)
    if (this.disabled || this.pending || this.readingFiles || input === '') return
		const editMessageId = this.editMessageId.trim()
		if (this.editing && !editMessageId) return
		emitDomainEvent(this, domainEvents.chatSubmit, {
			input,
			references: this.references,
			...(editMessageId ? { editMessageId } : {}),
		})
	}

	private stop = (): void => {
		const runId = this.runId.trim()
		if (!this.running || !runId) return
		this.dispatchEvent(new CustomEvent('lv-chat-stop', {
			bubbles: true,
			composed: true,
			detail: { runId },
		}))
	}

	private continueResponse = (): void => {
		if (this.files.length || this.readingFiles) return
		if (this.disabled || this.pending || this.running || !this.canContinue || this.editing || this.editMessageId.trim() || this.draft.trim() !== '') return
		emitDomainEvent(this, domainEvents.chatSubmit, {
			input: continueResponsePrompt,
			references: this.references,
		})
	}

	private cancelEdit = (): void => {
		this.files = []
		this.fileError = ''
		const editMessageId = this.editMessageId.trim()
		if (!this.editing && !editMessageId) return
		this.editMessageId = ''
		this.editing = false
		this.draft = ''
		this.references = []
		this.notifyReferences()
		void this.updateComplete.then(() => this.resizeTextarea())
		this.dispatchEvent(new CustomEvent('lv-chat-edit-cancel', {
			bubbles: true,
			composed: true,
			detail: { editMessageId },
		}))
	}

	private consumeAcceptedTurn() {
    this.files = []
    this.fileError = ''
		this.editMessageId = ''
		this.editing = false
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
		const hierarchy = referenceHierarchy(reference).join(' / ')
		return html`
			<button
				id=${this.mentionOptionID(index)}
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

	private mentionOptionID(index: number): string {
		return `chat-context-option-${index}`
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
