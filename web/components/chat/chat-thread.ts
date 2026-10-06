import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronRight, Check, Copy, FileText, Save, Plus, LayoutDashboard, LayoutPanelTop, Pencil, Waypoints, Wrench, type IconNode } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import type { ChatArtifactSignal, ChatStatus, ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { agentIcon } from './agent-icon'
import { referenceHierarchy, referenceIcon, referenceKindLabel } from './reference'
import { chatThreadStyles } from './chat-thread-styles'
import '../shared/markdown-view'
import '../shared/code-block'
import '../shared/visual-artifact'
import { readAttachedMessage } from './attachments'

type ChatRenderUnit =
  | { kind: 'user'; item: ChatTranscriptItemSignal }
  | { kind: 'agent'; items: ChatTranscriptItemSignal[] }

type ToolPreviewLanguage = 'json' | 'toon' | 'text' | 'yaml'
type ChatTranscriptItemWithFormats = ChatTranscriptItemSignal & {
  inputFormat?: string
  resultFormat?: string
}

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
  @property({ type: Boolean }) dashboardPreviewAvailable = false
  @property({ attribute: false }) pageArtifacts: ChatArtifactSignal[] = []
  @property({ type: String }) pageTitle = ''
  @property({ attribute: false }) selectedVisualId = ''
  @property({ attribute: false }) dashboardVisualIds: string[] = []
  @property({ attribute: false }) savedVisualIds: string[] = []
  @property({ attribute: false }) savingVisualId = ''
  @state() private expandedVisuals = new Set<string>()
  @state() private expandedToolCalls = new Set<string>()
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
    const showWorking = this.status.running && !transcript.some((item) => item.kind === 'tool' && this.toolStatus(item) === 'running')

    return html`
      <div class="thread">
        ${this.copyError ? html`<div class="copy-error" role="alert">${this.copyError}</div>` : nothing}
        <div class="scroll" @scroll=${this.onScroll}>
          <div class=${`stack${empty ? ' is-empty' : ''}`}>
            ${unavailable ? this.renderEmptyState('Agent unavailable', this.status.error || 'Agent is not configured.') : nothing}
            ${!unavailable && this.status.error ? html`<div class="alert" role="alert">${this.status.error}</div>` : nothing}
            ${empty && !unavailable ? this.renderEmptyState('Start a conversation') : nothing}
            ${groupTranscript(transcript).map((unit) => this.renderUnit(unit))}
            ${this.dashboardPreviewAvailable && this.pageArtifacts.length ? html`<section class="page-visuals" aria-label="Current page visuals"><h3>${this.pageTitle || 'Current page'} visuals</h3>${this.pageArtifacts.map(artifact => this.renderArtifact(artifact))}</section>` : nothing}
            ${showWorking ? html`
              <div class="working" role="status" aria-label="Working" aria-live="polite">
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
    if (changed.has('transcript') || changed.has('transcriptAttribute') || changed.has('status') || changed.has('conversationId') || changed.has('pageArtifacts')) {
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
		const message = readAttachedMessage(item.text || '-')
		if (references.length === 0 && message.files.length === 0) return html`<article class="message user">${this.renderBubble(item.text || '-', false)}${item.edited ? html`<span class="edited-label">Edited</span>` : nothing}${this.messageActions(item.id, item.text || '', item, true)}</article>`
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
					<div class="turn-message-text">${message.text}</div>
          ${message.files.map(file => html`<details class="message-attachment"><summary>${lucideIcon(FileText)} ${file.name}</summary><pre>${file.text}</pre></details>`)}
				</div>
				${item.edited ? html`<span class="edited-label">Edited</span>` : nothing}${this.messageActions(item.id, item.text || '', item, true)}
			</article>
		`
	}

  private renderAgentTurn(items: ChatTranscriptItemSignal[]) {
    const text = items.filter(item => item.kind === 'assistant').map(item => item.markdown || item.text || '').filter(Boolean).join('\n\n')
    const isVisual = (item: ChatTranscriptItemSignal) => item.kind === 'tool' && Boolean(item.artifact) && this.toolStatus(item) === 'complete'
    const activity = items.filter(item => item.kind === 'tool' && !isVisual(item))
    const running = this.status.running && items.includes(this.resolvedTranscript[this.resolvedTranscript.length - 1])
    const lastAnswerOrTool = [...items].reverse().find(item => item.kind === 'tool' || (item.kind === 'assistant' && Boolean(item.markdown || item.text)))
    const noFinalAnswer = !running && !this.status.error && !items.some(item => item.kind === 'error' || isVisual(item))
      && activity.some(item => this.toolStatus(item) === 'error')
      && lastAnswerOrTool?.kind === 'tool'
    return html`
      <article class="agent-turn">
        <div class="agent-stack">
          ${activity.length ? html`<details class="run-activity">
            <summary>${running ? 'Working…' : 'View steps'}${lucideIcon(ChevronRight, { size: 14 })}</summary>
            <div class="run-activity-steps">${activity.map(item => this.renderAgentItem(item))}</div>
          </details>` : nothing}
          ${items.filter(item => item.kind !== 'tool' || isVisual(item)).map(item => this.renderAgentItem(item))}
          ${noFinalAnswer ? html`<p class="run-notice" role="status">This request stopped before a final answer was ready. You can ask the agent to continue.</p>` : nothing}
        </div>
        ${text && !this.status.running ? this.messageActions(items[0].id, text, undefined, false) : nothing}
      </article>
    `
  }

  private messageActions(id: string, text: string, prompt: ChatTranscriptItemSignal | undefined, user: boolean) {
    return html`<div class="message-actions" role="group" aria-label=${user ? 'Your message actions' : 'Answer actions'}>
      <button type="button" title=${this.copiedId === id ? 'Copied' : 'Copy'} aria-label=${this.copiedId === id ? 'Copied' : 'Copy message'} @click=${() => this.copyMessage(id, text)}>${lucideIcon(this.copiedId === id ? Check : Copy, { size: 16 })}</button>
      ${user && prompt ? html`<button type="button" title="Edit in message input" aria-label="Edit message" ?disabled=${this.status.running || !this.status.enabled} @click=${() => this.reuseMessage(prompt)}>${lucideIcon(Pencil, { size: 16 })}</button>` : nothing}
      ${this.copiedId === id ? html`<span class="copy-confirmation" role="status">Copied</span>` : nothing}
    </div>`
  }

  private reuseMessage(item: ChatTranscriptItemSignal) {
    if (this.status.running || !this.status.enabled) return
    const editMessageId = typeof item.id === 'string' ? item.id.trim() : ''
    // An edit without a persisted message ID cannot be represented safely.
    if (!editMessageId || item.kind !== 'user') return
    this.dispatchEvent(new CustomEvent('lv-chat-reuse', {
      bubbles: true,
      composed: true,
      detail: { text: item.text || '', references: item.references || [], editMessageId },
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
        return this.renderTool(item)
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

  private renderTool(item: ChatTranscriptItemSignal) {
    const status = this.toolStatus(item)
    const label = toolCallLabel(item)
    const key = toolCallKey(item)
    const detailsID = toolDetailsID(key)
    const expanded = this.expandedToolCalls.has(key)
    const stateLabel = statusLabel(status)
    if (status === 'complete' && item.artifact && this.dashboardPreviewAvailable) return this.renderArtifact(item.artifact)
    return html`
      <div
        class=${['tool-call', item.artifact ? 'has-artifact' : '', status === 'running' ? 'running' : '', status === 'complete' ? 'done' : '', status === 'error' ? 'error' : '', status === 'interrupted' ? 'interrupted' : ''].filter(Boolean).join(' ')}
        title=${`${label}: ${stateLabel}`}
      >
        <button
          class="tool-trigger"
          type="button"
          aria-expanded=${expanded ? 'true' : 'false'}
          aria-controls=${detailsID}
          aria-label=${`${label}, ${stateLabel}. ${expanded ? 'Hide' : 'Show'} details`}
          @click=${() => this.toggleToolCall(key)}
        >
          <span class="tool-icon" aria-hidden="true">${toolIcon(item.name)}</span>
          <span class="activity-text">${label}</span>
          <span class="tool-status" aria-hidden="true">${stateLabel}</span>
          <span class="tool-chevron" aria-hidden="true">${chevronRightIcon()}</span>
        </button>
        ${status === 'complete' && item.artifact ? this.renderArtifact(item.artifact) : nothing}
        ${expanded ? this.renderToolDetails(item, detailsID) : nothing}
      </div>
    `
  }

  private renderArtifact(artifact: ChatArtifactSignal) {
    const payload = this.resolvedVisuals[artifact.id] || null
    const title = payload?.spec.title || artifact.summary || 'Visual'
    if (this.dashboardPreviewAvailable) {
      const kind = payload?.spec.kind ?? artifact.type
      const label = kind === 'kpi' ? 'Metric' : ['table', 'matrix', 'pivot'].includes(kind) ? 'Table' : 'Chart'
      return html`
        <button class="visual-reference" type="button" aria-label=${`Open ${title} in visuals sidebar`}
          aria-pressed=${this.selectedVisualId === artifact.id}
          @click=${() => this.dispatchEvent(new CustomEvent('lv-chat-dashboard-preview', {
            detail: { artifactId: artifact.id }, bubbles: true, composed: true,
          }))}>
          <span class="visual-reference-icon">${lucideIcon(LayoutPanelTop)}</span>
          <span class="visual-reference-copy"><span class="visual-reference-title">${title}</span><span class="visual-reference-hint">${label} · Open details</span></span>
          <span class="visual-reference-chevron">${lucideIcon(ChevronRight)}</span>
        </button>
      `
    }
    const expanded = this.expandedVisuals.has(artifact.id)
    return html`
      <button class="dashboard-preview-link" type="button" aria-expanded=${expanded} @click=${() => {
        const next = new Set(this.expandedVisuals)
        if (expanded) next.delete(artifact.id)
        else next.add(artifact.id)
        this.expandedVisuals = next
      }}>${lucideIcon(LayoutPanelTop)} ${expanded ? 'Hide visual' : 'View inside'}</button>
      ${expanded ? html`${this.renderArtifactActions(artifact.id)}<lv-visual-artifact type=${artifact.type} artifact-id=${artifact.id} .payload=${payload ?? null}></lv-visual-artifact>` : nothing}
    `
  }

  private renderArtifactActions(id: string) {
    const saved = this.savedVisualIds.includes(id)
    const added = this.dashboardVisualIds.includes(id)
    const save = (add: boolean) => this.dispatchEvent(new CustomEvent('lv-save-agent-visual', {
      bubbles: true, composed: true, detail: { artifactId: id, add },
    }))
    return html`<div class="artifact-actions">
      <button type="button" ?disabled=${Boolean(this.savingVisualId)} aria-pressed=${saved} title=${saved ? 'Unsave visual' : 'Save visual'} @click=${() => save(false)}>${lucideIcon(saved ? Check : Save)} ${this.savingVisualId === id ? 'Updating…' : saved ? 'Saved' : 'Unsaved'}</button>
      <button type="button" ?disabled=${added || Boolean(this.savingVisualId)} @click=${() => { if (!added) save(true) }}>${lucideIcon(added ? Check : Plus)} ${added ? 'Added to dashboard' : 'Add to dashboard'}</button>
    </div>`
  }

  captureScroll(): { top: number; autoScroll: boolean } {
    return { top: this.renderRoot.querySelector<HTMLElement>('.scroll')?.scrollTop ?? 0, autoScroll: this.shouldAutoScroll }
  }

  restoreScroll(position: { top: number; autoScroll: boolean }): void {
    if (this.scrollFrame) cancelAnimationFrame(this.scrollFrame)
    this.scrollFrame = 0
    const scroll = this.renderRoot.querySelector<HTMLElement>('.scroll')
    if (scroll) scroll.scrollTop = position.top
    this.shouldAutoScroll = position.autoScroll
  }

  private renderToolDetails(item: ChatTranscriptItemSignal, detailsID: string) {
    const status = this.toolStatus(item)
    return html`
      <div class="tool-details" id=${detailsID}>
        ${item.argumentsJson || item.inputJson ? this.renderToolCode('Input', item.argumentsJson || item.inputJson || '', toolInputLanguage(item)) : nothing}
        ${item.resultJson ? this.renderToolCode(toolResultLabel(item, status), item.resultJson, toolResultLanguage(item)) : nothing}
        ${item.error && !hasStructuredErrorResult(item.resultJson) ? html`<div class="tool-error" role="alert">${item.error}</div>` : nothing}
        ${!item.argumentsJson && !item.inputJson && !item.resultJson && !item.error
          ? html`<div class="tool-empty">No details available.</div>`
          : nothing}
      </div>
    `
  }

  private renderToolCode(label: string, value: string, language: ToolPreviewLanguage) {
    return html`
      <div class="tool-detail-block">
        <div class="tool-detail-label">${label}</div>
        <lv-code-block compact language=${language} .code=${value}></lv-code-block>
      </div>
    `
  }

  private toggleToolCall(key: string) {
    const next = new Set(this.expandedToolCalls)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    this.expandedToolCalls = next
  }

  private toolStatus(item: ChatTranscriptItemSignal): string {
    const status = item.status || 'running'
    if (status !== 'running' && status !== 'pending') return status
    if (!this.status.running) return 'interrupted'
    if (this.status.runId && item.runId && this.status.runId !== item.runId) return 'interrupted'
    return status
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

function toolCallLabel(item: ChatTranscriptItemSignal): string {
  const title = item.title || titleFromToolName(item.name || '')
  return title || 'Tool'
}

function titleFromToolName(name: string): string {
  return name.replace(/_/g, ' ').trim().replace(/\b\w/g, (match) => match.toUpperCase())
}

const toolIconContent: Record<string, IconNode> = {
  catalog_search: LayoutDashboard,
  catalog_list: LayoutDashboard,
  catalog_get: FileText,
  docs_search: FileText,
  docs_read: FileText,
  query_semantic_model: Waypoints,
  query_dashboard_visual: LayoutPanelTop,
  query_visual: LayoutPanelTop,
  list_dashboards: LayoutDashboard,
  describe_dashboard: FileText,
  list_semantic_models: Waypoints,
  describe_model: Waypoints,
  query_dashboard_page: LayoutPanelTop,
}

function toolIcon(name = '') {
  return lucideIcon(toolIconContent[name] ?? Wrench)
}

function chevronRightIcon() {
  return lucideIcon(ChevronRight)
}

function toolCallKey(item: ChatTranscriptItemSignal): string {
  return item.toolCallId || item.id || `${item.name || 'tool'}:${item.createdAt || ''}`
}

function toolDetailsID(key: string): string {
  return `tool-details-${key.replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

function toolInputLanguage(item: ChatTranscriptItemSignal): ToolPreviewLanguage {
  return previewLanguage((item as ChatTranscriptItemWithFormats).inputFormat, item.argumentsJson || item.inputJson || '', 'json')
}

function toolResultLanguage(item: ChatTranscriptItemSignal): ToolPreviewLanguage {
  return previewLanguage((item as ChatTranscriptItemWithFormats).resultFormat, item.resultJson || '', 'toon')
}

function toolResultLabel(item: ChatTranscriptItemSignal, status: string): string {
  if (status === 'error') return 'Error result'
  if (item.name === 'export_dashboard_yaml' && toolResultLanguage(item) === 'yaml') return 'Dashboard YAML'
  return 'Result'
}

function hasStructuredErrorResult(value: string | undefined): boolean {
  if (!value) return false
  try {
    const result = JSON.parse(value)
    return result !== null && typeof result === 'object' && 'error' in result && result.error != null
  } catch {
    return false
  }
}

function previewLanguage(format: string | undefined, value: string, fallback: ToolPreviewLanguage): ToolPreviewLanguage {
  const normalized = (format || '').trim().toLowerCase()
  if (normalized === 'json' || normalized === 'toon' || normalized === 'text' || normalized === 'yaml') return normalized
  if (isJSON(value)) return 'json'
  return fallback
}

function isJSON(value: string): boolean {
  const trimmed = value.trim()
  if (!trimmed || !['{', '['].includes(trimmed[0])) return false
  try {
    JSON.parse(trimmed)
    return true
  } catch {
    return false
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case 'complete': return 'Complete'
    case 'error': return 'Failed'
    case 'streaming': return 'Streaming'
    case 'pending': return 'Queued'
    case 'interrupted': return 'Interrupted'
    default: return 'Running'
  }
}

if (!customElements.get('lv-chat-thread')) customElements.define('lv-chat-thread', ChatThread)
