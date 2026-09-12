import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronRight, FileText, LayoutDashboard, LayoutPanelTop, Waypoints, Wrench, type IconNode } from 'lucide'
import type { ChatArtifactSignal, ChatStatus, ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { lucideIcon } from '../shared/lucide-icons'
import { agentIcon } from './agent-icon'
import { referenceHierarchy, referenceIcon, referenceKindLabel } from './reference'
import '../shared/code-block'
import { chatThreadStyles } from './chat-thread-styles'
import '../shared/markdown-view'
import '../shared/visual-artifact'

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
  @state() private expandedToolCalls = new Set<string>()
  private scrollFrame = 0
  private shouldAutoScroll = true

  static styles = chatThreadStyles

  render() {
    const transcript = this.resolvedTranscript
    const unavailable = !this.status.enabled && transcript.length === 0
    const empty = transcript.length === 0 && !this.status.running
    const showWorking = this.status.running && !transcript.some((item) => item.kind === 'tool' && item.status === 'running')

    return html`
      <div class="thread">
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
		if (references.length === 0) return this.renderMessage('user', item.text || '-')
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
			</article>
		`
	}

  private renderAgentTurn(items: ChatTranscriptItemSignal[]) {
    return html`
      <article class="agent-turn">
        <div class="agent-stack">
          ${items.map((item) => this.renderAgentItem(item))}
        </div>
      </article>
    `
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
    const status = item.status || 'running'
    const label = toolCallLabel(item)
    const key = toolCallKey(item)
    const detailsID = toolDetailsID(key)
    const expanded = this.expandedToolCalls.has(key)
    return html`
      <div
        class=${['tool-call', item.artifact ? 'has-artifact' : '', status === 'running' ? 'running' : '', status === 'complete' ? 'done' : '', status === 'error' ? 'error' : ''].filter(Boolean).join(' ')}
        title=${`${label}: ${statusLabel(status)}`}
      >
        <button
          class="tool-trigger"
          type="button"
          aria-expanded=${expanded ? 'true' : 'false'}
          aria-controls=${detailsID}
          @click=${() => this.toggleToolCall(key)}
        >
          <span class="tool-icon" aria-hidden="true">${toolIcon(item.name)}</span>
          <span class="activity-text">${label}</span>
          <span class="tool-chevron" aria-hidden="true">${chevronRightIcon()}</span>
        </button>
        ${status === 'complete' && item.artifact ? this.renderArtifact(item.artifact) : nothing}
        ${expanded ? this.renderToolDetails(item, detailsID) : nothing}
      </div>
    `
  }

  private renderArtifact(artifact: ChatArtifactSignal) {
    const payload = this.resolvedVisuals[artifact.id] || null
    return html`<lv-visual-artifact type=${artifact.type} artifact-id=${artifact.id} .payload=${payload ?? null}></lv-visual-artifact>`
  }

  private renderToolDetails(item: ChatTranscriptItemSignal, detailsID: string) {
    const status = item.status || 'running'
    return html`
      <div class="tool-details" id=${detailsID}>
        ${item.argumentsJson || item.inputJson ? this.renderToolCode('Input', item.argumentsJson || item.inputJson || '', toolInputLanguage(item)) : nothing}
        ${item.resultJson ? this.renderToolCode(toolResultLabel(item, status), item.resultJson, toolResultLanguage(item)) : nothing}
        ${!item.resultJson && item.error ? html`<div class="tool-error">${item.error}</div>` : nothing}
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
    if (next.has(key)) {
      next.delete(key)
    } else {
      next.add(key)
    }
    this.expandedToolCalls = next
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
  return name
    .replace(/_/g, ' ')
    .trim()
    .replace(/\b\w/g, (match) => match.toUpperCase())
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
  // Historical transcript entries retain their original icons after the
  // curated catalog cutover.
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
    case 'complete':
      return 'Complete'
    case 'error':
      return 'Failed'
    case 'streaming':
      return 'Streaming'
    case 'pending':
      return 'Queued'
    default:
      return 'Running'
  }
}

if (!customElements.get('lv-chat-thread')) customElements.define('lv-chat-thread', ChatThread)
