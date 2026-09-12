import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Copy, Check, Pencil, RotateCcw } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import type { ChatArtifactSignal, ChatStatus, ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { agentIcon } from './agent-icon'
import { referenceHierarchy, referenceIcon, referenceKindLabel } from './reference'
import { chatThreadStyles } from './chat-thread-styles'
import '../shared/markdown-view'
import '../shared/visual-artifact'

type ChatRenderUnit =
  | { kind: 'user'; item: ChatTranscriptItemSignal }
  | { kind: 'agent'; items: ChatTranscriptItemSignal[] }

const jsonConverter = <T,>(fallback: T) => ({
  fromAttribute(value: string | null): T {
    if (!value) return fallback
    try {
      return JSON.parse(value) as T
    } catch {
      return fallback
    }
  },
  toAttribute(value: T): string {
    return JSON.stringify(value ?? fallback)
  },
})

class ChatThread extends LitElement {
  @property({ attribute: false }) transcript: ChatTranscriptItemSignal[] = []
  @property({ attribute: 'transcript', converter: jsonConverter<ChatTranscriptItemSignal[]>([]) }) transcriptAttribute: ChatTranscriptItemSignal[] = []
  @property({ attribute: false }) visuals: Record<string, VisualizationEnvelope> = {}
  @property({ attribute: 'visuals', converter: jsonConverter<Record<string, VisualizationEnvelope>>({}) }) visualsAttribute: Record<string, VisualizationEnvelope> = {}
  @property({ attribute: 'status', converter: jsonConverter<ChatStatus>({ enabled: false, running: false }) }) status: ChatStatus = { enabled: false, running: false }
  @property({ attribute: 'conversation-id' }) conversationId = ''
  @property({ reflect: true }) surface: 'page' | 'drawer' = 'page'
  @state() private copiedId = ''
  @state() private copyError = ''
  private copyTimer = 0
  private scrollFrame = 0
  private shouldAutoScroll = true

  static styles = chatThreadStyles

  render() {
    const transcript = this.resolvedTranscript
    const unavailable = !this.status.enabled && transcript.length === 0
    const empty = transcript.length === 0 && !this.status.running
    const showWorking = this.status.running

    return html`
      <div class="thread">
        ${this.copyError ? html`<div class="copy-error" role="alert">${this.copyError}</div>` : nothing}
        <div class="scroll" @scroll=${this.onScroll}>
          <div class=${`stack${empty ? ' is-empty' : ''}`}>
            ${unavailable ? this.renderEmptyState('Agent unavailable', this.status.error || 'Agent is not configured.') : nothing}
            ${!unavailable && this.status.error ? html`<div class="alert" role="alert">${this.status.error}</div>` : nothing}
            ${empty && !unavailable ? this.renderEmptyState('Start a conversation') : nothing}
            ${groupTranscript(transcript).map((unit) => this.renderUnit(unit))}
            ${showWorking ? html`
              <div class="working" role="status" aria-live="polite">
                <span>Working</span>
                <span class="working-dots" aria-hidden="true"><i></i><i></i><i></i></span>
              </div>
            ` : nothing}
          </div>
        </div>
      </div>
    `
  }

  protected firstUpdated() {
    this.scheduleScrollToBottom(true)
  }

  protected updated(changed: Map<string, unknown>) {
    if (changed.has('transcript') || changed.has('transcriptAttribute') || changed.has('status') || changed.has('conversationId')) {
      this.scheduleScrollToBottom()
    }
  }

  disconnectedCallback() {
    if (this.scrollFrame) cancelAnimationFrame(this.scrollFrame)
    window.clearTimeout(this.copyTimer)
    this.scrollFrame = 0
    super.disconnectedCallback()
  }

  private get resolvedTranscript(): ChatTranscriptItemSignal[] {
    return Array.isArray(this.transcript) && this.transcript.length > 0 ? this.transcript : this.transcriptAttribute
  }

  private get resolvedVisuals(): Record<string, VisualizationEnvelope> {
    return hasKeys(this.visuals) ? this.visuals : this.visualsAttribute
  }

  private scheduleScrollToBottom(force = false) {
    if (!force && !this.shouldAutoScroll) return
    if (this.scrollFrame) cancelAnimationFrame(this.scrollFrame)
    this.scrollFrame = requestAnimationFrame(() => {
      this.scrollFrame = 0
      const scroll = this.renderRoot.querySelector<HTMLElement>('.scroll')
      if (!scroll) return
      scroll.scrollTop = scroll.scrollHeight
      this.shouldAutoScroll = true
    })
  }

  private onScroll = (event: Event): void => {
    const scroll = event.currentTarget as HTMLElement
    this.shouldAutoScroll = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 80
  }

  private renderEmptyState(title: string, detail = '') {
    return html`
      <div class="empty-state" role="status">
        <span class="empty-icon" aria-hidden="true">${agentIcon()}</span>
        <strong class="empty-title">${title}</strong>
        ${detail ? html`<span class="empty-detail">${detail}</span>` : nothing}
      </div>
    `
  }

  private renderUnit(unit: ChatRenderUnit) {
    if (unit.kind === 'user') return this.renderUserTurn(unit.item)
    return this.renderAgentTurn(unit.items)
  }

	private renderUserTurn(item: ChatTranscriptItemSignal) {
		const references = item.references ?? []
		if (references.length === 0) return html`<article class="message user">${this.renderBubble(item.text || '-', false)}${item.edited ? html`<span class="edited-label">Edited</span>` : nothing}${this.messageActions(item.id, item.text || '', item, true)}</article>`
		return html`
			<article class="message user">
				<div class="bubble plain user-turn-bubble">
					<div class="turn-references" aria-label="Context for this message">
						${references.map((reference) => {
							const hierarchy = referenceHierarchy(reference).join(' / ')
							const kind = referenceKindLabel(reference.reference.kind)
							const tooltip = [reference.name, hierarchy, kind].filter(Boolean).join(' · ')
							return html`
								<a class="turn-reference" href=${reference.href} title=${tooltip} aria-label=${tooltip}>
									<span class="turn-reference-icon" aria-hidden="true">${referenceIcon(reference.reference.kind, reference.visualType)}</span>
									<span class="turn-reference-name">${reference.name}</span>
								</a>
							`
						})}
					</div>
					<div class="turn-message-text">${item.text || '-'}</div>
				</div>
				${item.edited ? html`<span class="edited-label">Edited</span>` : nothing}${this.messageActions(item.id, item.text || '', item, true)}
			</article>
		`
	}

  private renderAgentTurn(items: ChatTranscriptItemSignal[]) {
    const text = items.filter(item => item.kind === 'assistant').map(item => item.markdown || item.text || '').filter(Boolean).join('\n\n')
    const index = this.resolvedTranscript.indexOf(items[0])
    const prompt = this.resolvedTranscript.slice(0, index).reverse().find(item => item.kind === 'user')
    return html`
      <article class="agent-turn">
        <div class="agent-stack">
          ${items.map((item) => this.renderAgentItem(item))}
        </div>
        ${text && !this.status.running ? this.messageActions(items[0].id, text, prompt, false) : nothing}
      </article>
    `
  }

  private messageActions(id: string, text: string, prompt: ChatTranscriptItemSignal | undefined, user: boolean) {
    return html`<div class="message-actions" role="group" aria-label=${user ? 'Your message actions' : 'Answer actions'}>
      <button type="button" title=${this.copiedId === id ? 'Copied' : 'Copy'} aria-label=${this.copiedId === id ? 'Copied' : 'Copy message'} @click=${() => this.copyMessage(id, text)}>${lucideIcon(this.copiedId === id ? Check : Copy, { size: 16 })}</button>
      ${prompt ? html`<button type="button" title=${user ? 'Edit in message input' : 'Ask again'} aria-label=${user ? 'Edit message' : 'Ask again'} ?disabled=${this.status.running || !this.status.enabled} @click=${() => this.reuseMessage(prompt, user)}>${lucideIcon(user ? Pencil : RotateCcw, { size: 16 })}</button>` : nothing}
      ${this.copiedId === id ? html`<span class="copy-confirmation" role="status">Copied</span>` : nothing}
    </div>`
  }

  private reuseMessage(item: ChatTranscriptItemSignal, edit: boolean) {
    if (this.status.running || !this.status.enabled) return
    if (edit && item.kind === 'user') {
      const editMessageId = typeof item.id === 'string' ? item.id.trim() : ''
      // An edit without a persisted message ID cannot be represented safely.
      // Leave the composer untouched rather than turning it into a new prompt.
      if (!editMessageId) return
      this.dispatchEvent(new CustomEvent('lv-chat-reuse', {
        bubbles: true,
        composed: true,
        detail: { text: item.text || '', references: item.references || [], editMessageId },
      }))
      return
    }
    this.dispatchEvent(new CustomEvent('lv-chat-reuse', {
      bubbles: true,
      composed: true,
      detail: { text: item.text || '', references: item.references || [] },
    }))
  }

  private async copyMessage(id: string, text: string) {
    this.copyError = ''
    try {
      await navigator.clipboard.writeText(text)
      this.copiedId = id
      window.clearTimeout(this.copyTimer)
      this.copyTimer = window.setTimeout(() => { this.copiedId = '' }, 1800)
    } catch {
      this.copyError = 'Could not copy. Select the message text and copy it manually.'
    }
  }

  private renderAgentItem(item: ChatTranscriptItemSignal) {
    switch (item.kind) {
      case 'tool':
        return item.status === 'complete' && item.artifact ? this.renderArtifact(item.artifact) : nothing
      case 'error':
        return this.renderMessage('error', item.text || item.error || '-', false, true)
      case 'assistant': {
        const content = item.markdown || item.text || ''
        return content ? this.renderAssistantContent(content) : nothing
      }
      case 'summary':
      default:
        return this.renderAssistantContent(item.markdown || item.text || '-')
    }
  }

  private renderMessage(role: string, content: string, renderMarkdown = false, error = false) {
    return html`
      <article class=${['message', role, error ? 'error' : ''].filter(Boolean).join(' ')}>
        ${this.renderBubble(content, renderMarkdown)}
      </article>
    `
  }

  private renderBubble(content: string, renderMarkdown: boolean) {
    const className = ['bubble', renderMarkdown ? 'markdown' : 'plain'].join(' ')
    return html`<div class=${className}>${renderMarkdown ? html`<lv-markdown-view .value=${content}></lv-markdown-view>` : content}</div>`
  }

  private renderAssistantContent(content: string) {
    return html`<lv-markdown-view class="agent-markdown" .value=${content}></lv-markdown-view>`
  }

  private renderArtifact(artifact: ChatArtifactSignal) {
    const payload = this.resolvedVisuals[artifact.id] || null
    return html`<lv-visual-artifact type=${artifact.type} artifact-id=${artifact.id} .payload=${payload ?? null}></lv-visual-artifact>`
  }
}

function hasKeys(value: Record<string, unknown> | undefined): boolean {
  return !!value && Object.keys(value).length > 0
}

function groupTranscript(transcript: ChatTranscriptItemSignal[]): ChatRenderUnit[] {
  const units: ChatRenderUnit[] = []
  let agentItems: ChatTranscriptItemSignal[] = []
  const flushAgent = () => {
    if (agentItems.length === 0) return
    units.push({ kind: 'agent', items: agentItems })
    agentItems = []
  }

  for (const item of transcript) {
    if (!isVisibleTranscriptItem(item)) continue
    if (item.kind === 'user') {
      flushAgent()
      units.push({ kind: 'user', item })
      continue
    }
    agentItems.push(item)
  }
  flushAgent()
  return units
}

function isVisibleTranscriptItem(item: ChatTranscriptItemSignal): boolean {
  return item.kind !== 'tool' || (item.status === 'complete' && Boolean(item.artifact))
}

if (!customElements.get('lv-chat-thread')) customElements.define('lv-chat-thread', ChatThread)
