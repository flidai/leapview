import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Box, ChevronRight, FileText, LayoutDashboard, LayoutPanelTop, Wrench, type IconNode } from 'lucide'
import type { ChatArtifactSignal, ChatStatus, ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { lucideIcon } from '../shared/lucide-icons'
import { referenceHierarchy, referenceIcon, referenceKindLabel } from './reference'
import '../shared/code-block'
import '../shared/markdown-view'
import '../shared/visual-artifact'

type ChatRenderUnit =
  | { kind: 'user'; item: ChatTranscriptItemSignal }
  | { kind: 'agent'; items: ChatTranscriptItemSignal[] }

type ToolPreviewLanguage = 'json' | 'toon' | 'text'
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

  static styles = css`
    :host {
      box-sizing: border-box;
      display: block;
      height: 100%;
      min-height: 0;
      overflow: hidden;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    *,
    *::before,
    *::after {
      box-sizing: inherit;
    }

    .thread {
      display: grid;
      height: 100%;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr);
      overflow: hidden;
	      background: var(--lv-bg-app);
    }

    .scroll {
      height: 100%;
      min-height: 0;
      overflow: auto;
      overscroll-behavior: contain;
      padding: var(--lv-chat-thread-padding);
    }

    .stack {
      display: grid;
      width: min(100%, var(--lv-chat-stack-width));
      margin-inline: auto;
      gap: var(--lv-chat-stack-gap);
    }

    .alert {
      display: grid;
      gap: var(--lv-space-sm);
      align-content: center;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--lv-chat-thread-padding);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
      text-align: center;
    }

    .alert {
      border-color: var(--lv-line-danger-muted);
      background: var(--lv-bg-danger-muted);
      color: var(--lv-fg-default);
      text-align: left;
    }

    .message {
      display: grid;
      max-width: min(var(--lv-chat-message-width), 100%);
    }

    .message.user {
      justify-self: end;
    }

    .agent-turn,
    .message.error {
      justify-self: start;
    }

    .label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .bubble {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--lv-chat-bubble-padding-block) var(--lv-chat-bubble-padding-inline);
      font: var(--lv-type-body);
      line-height: var(--base-text-lineHeight-relaxed);
      overflow-wrap: anywhere;
    }

    .bubble.plain {
      white-space: pre-wrap;
    }

    .agent-turn {
      display: grid;
      max-width: min(var(--lv-chat-message-width), 100%);
    }

    .agent-stack {
      display: grid;
      gap: var(--lv-chat-agent-item-gap);
    }

    .agent-markdown {
      display: block;
    }

    .user .bubble {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-panel-muted);
    }

		.user-turn-bubble {
			display: grid;
			gap: var(--lv-space-sm);
		}

		.turn-references {
			display: flex;
			flex-wrap: wrap;
			gap: var(--lv-space-2xs);
		}

		.turn-reference {
			display: inline-flex;
			width: fit-content;
			max-width: 100%;
			min-width: 0;
			align-items: center;
			gap: var(--lv-space-xs);
			border-radius: var(--lv-radius-default);
			background: var(--lv-bg-control);
			color: var(--lv-fg-default);
			padding: var(--lv-space-xs) var(--lv-space-sm);
			text-decoration: none;
			white-space: nowrap;
		}

		.turn-reference:hover,
		.turn-reference:focus-visible {
			background: var(--lv-bg-control-hover);
			outline: 0;
		}

		.turn-reference-icon,
		.turn-reference-icon svg {
			width: 14px;
			height: 14px;
		}

		.turn-reference-name {
			min-width: 0;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.turn-message-text {
			white-space: pre-wrap;
		}

    :host([surface='drawer']) .thread {
      background: var(--lv-bg-app);
    }

    :host([surface='drawer']) .scroll {
      padding: var(--lv-space-lg) calc(var(--lv-space-lg) + var(--lv-space-xs)) var(--lv-space-sm);
    }

    :host([surface='drawer']) .user .bubble {
      border-color: transparent;
      border-radius: var(--lv-radius-large);
      padding: var(--lv-space-sm) var(--lv-space-md);
    }

    .message.error .bubble {
      border-color: var(--lv-line-danger-muted);
      background: var(--lv-bg-danger-muted);
    }

    .tool-call {
      display: grid;
      width: fit-content;
      max-width: 100%;
      margin-block: var(--lv-chat-agent-tool-gap);
      gap: var(--lv-space-sm);
    }

    .tool-call.has-artifact {
      width: min(100%, 48rem);
    }

    .tool-trigger {
      display: inline-flex;
      width: fit-content;
      max-width: 100%;
      align-items: center;
      gap: var(--lv-chat-activity-gap);
      border: 0;
      border-radius: var(--lv-radius-tight);
      background: transparent;
      padding: var(--lv-space-2xs) 0;
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      line-height: var(--base-text-lineHeight-snug);
      text-align: left;
      transition: color var(--lv-transition-fast);
    }

    .tool-icon {
      display: inline-flex;
      width: var(--lv-chat-activity-icon-size);
      height: var(--lv-chat-activity-icon-size);
      flex: 0 0 var(--lv-chat-activity-icon-size);
      color: currentColor;
    }

    .tool-icon svg {
      display: block;
      width: 100%;
      height: 100%;
      fill: none;
      stroke: currentColor;
      stroke-width: 1.8;
      stroke-linecap: round;
      stroke-linejoin: round;
    }

    .tool-call.running .tool-trigger {
      color: var(--lv-fg-warning);
    }

    .tool-call.running .tool-icon {
      animation: pulse 1.1s ease-in-out infinite;
    }

    .tool-call.error .tool-trigger {
      color: var(--lv-fg-danger);
    }

    .tool-trigger:hover,
    .tool-trigger:focus-visible {
      color: var(--lv-fg-default);
    }

    .tool-call.error .tool-trigger:hover,
    .tool-call.error .tool-trigger:focus-visible {
      color: var(--lv-fg-danger);
    }

    .tool-trigger:focus-visible {
      outline: var(--lv-border-width-focus) solid var(--lv-line-emphasis);
      outline-offset: var(--lv-space-xs);
    }

    .activity-text {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .tool-chevron {
      display: inline-flex;
      width: var(--lv-chat-activity-icon-size);
      height: var(--lv-chat-activity-icon-size);
      flex: 0 0 var(--lv-chat-activity-icon-size);
      opacity: 0;
      transform: translateX(calc(-1 * var(--lv-space-xs)));
      transition:
        opacity var(--lv-transition-fast),
        transform var(--lv-transition-fast);
    }

    .tool-chevron svg {
      display: block;
      width: 100%;
      height: 100%;
      fill: none;
      stroke: currentColor;
      stroke-width: 1.8;
      stroke-linecap: round;
      stroke-linejoin: round;
    }

    .tool-trigger:hover .tool-chevron,
    .tool-trigger:focus-visible .tool-chevron,
    .tool-trigger[aria-expanded='true'] .tool-chevron {
      opacity: 1;
      transform: translateX(0);
    }

    .tool-trigger[aria-expanded='true'] .tool-chevron {
      transform: rotate(90deg);
    }

    .tool-details {
      display: grid;
      max-width: min(42rem, 100%);
      gap: var(--lv-space-md);
      border-left: var(--lv-border-width-focus) solid var(--lv-line-muted);
      padding-left: var(--lv-space-lg);
      color: var(--lv-fg-muted);
      font: var(--lv-type-secondary);
      animation: tool-details-open var(--lv-transition-normal);
      transform-origin: top left;
    }

    .tool-detail-block {
      display: grid;
      gap: var(--lv-space-xs);
    }

    .tool-detail-label {
      color: var(--lv-fg-muted);
      font-weight: var(--base-text-weight-medium);
    }

    .tool-detail-block pre {
      max-height: var(--lv-chat-tool-max-height);
      max-width: 100%;
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      margin: 0;
      padding: var(--lv-chat-pre-padding-block) var(--lv-chat-pre-padding-inline);
      color: var(--lv-fg-default);
      font: var(--lv-type-code-block);
      white-space: pre-wrap;
    }

    .tool-detail-block lv-code-block {
      max-width: 100%;
    }

    .tool-error {
      color: var(--lv-fg-danger);
    }

    lv-visual-artifact {
      display: block;
      width: 100%;
      min-width: 0;
      overflow: hidden;
    }

    lv-visual-artifact:not([type='table']):not([type='matrix']):not([type='pivot']) {
      height: 18rem;
    }

    lv-visual-artifact:is([type='table'], [type='matrix'], [type='pivot']) {
      height: 22rem;
    }

    @keyframes tool-details-open {
      from {
        opacity: 0;
        transform: translateY(calc(-1 * var(--lv-chat-tool-disclosure-offset)));
      }
      to {
        opacity: 1;
        transform: translateY(0);
      }
    }

    @keyframes pulse {
      0%,
      100% {
        opacity: 0.45;
      }
      50% {
        opacity: 1;
      }
    }

    @media (max-width: 720px) {
      .scroll {
        padding: var(--lv-chat-thread-padding-compact);
      }
    }
  `

  render() {
    const transcript = this.resolvedTranscript

    return html`
      <div class="thread">
        <div class="scroll">
          <div class="stack">
            ${this.status.error ? html`<div class="alert">${this.status.error}</div>` : nothing}
            ${groupTranscript(transcript).map((unit) => this.renderUnit(unit))}
          </div>
        </div>
      </div>
    `
  }

  protected firstUpdated() {
    this.scheduleScrollToBottom()
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

  private scheduleScrollToBottom() {
    if (this.scrollFrame) cancelAnimationFrame(this.scrollFrame)
    this.scrollFrame = requestAnimationFrame(() => {
      this.scrollFrame = 0
      const scroll = this.renderRoot.querySelector<HTMLElement>('.scroll')
      if (!scroll) return
      scroll.scrollTop = scroll.scrollHeight
    })
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
							const hierarchy = referenceHierarchy(reference).join(' › ')
							const kind = referenceKindLabel(reference.reference.type)
							const tooltip = [reference.name, hierarchy, kind].filter(Boolean).join(' · ')
							return html`
								<a class="turn-reference" href=${reference.href} title=${tooltip} aria-label=${tooltip}>
									<span class="turn-reference-icon" aria-hidden="true">${referenceIcon(reference.reference.type, reference.visualType)}</span>
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
      case 'summary':
      case 'assistant':
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
        ${item.inputJson || item.argumentsJson ? this.renderToolCode('Input', item.inputJson || item.argumentsJson || '', toolInputLanguage(item)) : nothing}
        ${item.resultJson ? this.renderToolCode(status === 'error' ? 'Error result' : 'Result', item.resultJson, toolResultLanguage(item)) : nothing}
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
  query_semantic_model: Box,
  query_dashboard_visual: LayoutPanelTop,
  query_visual: LayoutPanelTop,
  // Historical transcript entries retain their original icons after the
  // curated catalog cutover.
  list_dashboards: LayoutDashboard,
  describe_dashboard: FileText,
  list_semantic_models: Box,
  describe_model: Box,
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
  return previewLanguage((item as ChatTranscriptItemWithFormats).inputFormat, item.inputJson || item.argumentsJson || '', 'json')
}

function toolResultLanguage(item: ChatTranscriptItemSignal): ToolPreviewLanguage {
  return previewLanguage((item as ChatTranscriptItemWithFormats).resultFormat, item.resultJson || '', 'toon')
}

function previewLanguage(format: string | undefined, value: string, fallback: ToolPreviewLanguage): ToolPreviewLanguage {
  const normalized = (format || '').trim().toLowerCase()
  if (normalized === 'json' || normalized === 'toon' || normalized === 'text') return normalized
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
    default:
      return 'Running'
  }
}

if (!customElements.get('lv-chat-thread')) customElements.define('lv-chat-thread', ChatThread)
