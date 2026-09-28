import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChartColumn, Check, ChevronRight, Copy, Pencil } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import type { ChatArtifactSignal, ChatStatus, ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { agentIcon } from './agent-icon'
import { referenceHierarchy, referenceIcon, referenceKindLabel } from './reference'
import { chatThreadStyles } from './chat-thread-styles'
import { dataExplorerURL } from '../data/data-explorer-url'
import type { DashboardTimeGrain } from '../../generated/dashboard'
import type { DataExplorerCommand } from '../../generated/signals'
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
    const recoveredErrors = new Set<ChatTranscriptItemSignal>()
    const earlierAssistantMessages = new Set<ChatTranscriptItemSignal>()
    const completedTools = new Set<string>()
    let hasLaterAssistant = false
    for (let index = transcript.length - 1; index >= 0; index--) {
      const item = transcript[index]
      if (item.kind === 'user') {
        completedTools.clear()
        hasLaterAssistant = false
      } else if (item.kind === 'tool' && item.name) {
        if (this.toolStatus(item) === 'complete') completedTools.add(item.name)
        else if (this.toolStatus(item) === 'error' && completedTools.has(item.name)) recoveredErrors.add(item)
      } else if (item.kind === 'assistant' || item.kind === 'summary') {
        if (hasLaterAssistant) earlierAssistantMessages.add(item)
        hasLaterAssistant = true
      }
    }
    const visibleTranscript = transcript.filter((item) => {
      if (earlierAssistantMessages.has(item)) return false
      if (item.kind !== 'tool') return true
      const status = this.toolStatus(item)
      return (status === 'error' && !recoveredErrors.has(item)) || (status === 'complete' && Boolean(item.artifact))
    })
    const unavailable = !this.status.enabled && transcript.length === 0
    const empty = visibleTranscript.length === 0 && !this.status.running
    const showWorking = this.status.running

    return html`
      <div class="thread">
        ${this.copyError ? html`<div class="copy-error" role="alert">${this.copyError}</div>` : nothing}
        <div class="scroll" @scroll=${this.onScroll}>
          <div class=${`stack${empty ? ' is-empty' : ''}`}>
            ${unavailable ? this.renderEmptyState('Agent unavailable', this.status.error || 'Agent is not configured.') : nothing}
            ${!unavailable && this.status.error ? html`<div class="alert" role="alert">${this.status.error}</div>` : nothing}
            ${empty && !unavailable ? this.renderEmptyState('Start a conversation') : nothing}
            ${groupTranscript(visibleTranscript).map((unit) => this.renderUnit(unit))}
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
    return html`
      <article class="agent-turn">
        <div class="agent-stack">
          ${items.map((item) => this.renderAgentItem(item))}
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
        return this.renderToolOutcome(item)
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
      <article class=${['message', role, error ? 'error' : ''].filter(Boolean).join(' ')} role=${error ? 'alert' : nothing}>
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

  private renderToolOutcome(item: ChatTranscriptItemSignal) {
    const status = this.toolStatus(item)
    if (status === 'complete' && item.artifact) return this.renderArtifact(item.artifact, item)
    if (status === 'error') return this.renderMessage('error', item.error?.trim() || 'A requested operation failed.', false, true)
    return nothing
  }

  private renderArtifact(artifact: ChatArtifactSignal, item?: ChatTranscriptItemSignal) {
    const payload = this.resolvedVisuals[artifact.id] || null
    const explorerHref = payload && payload.visualID === artifact.id ? queryVisualExplorerURL(item, artifact.type, artifact.id) : ''
    if (this.surface === 'page' && payload) {
      const title = payload?.spec.title?.trim() || artifact.summary?.trim() || 'Visual result'
      const kind = ['table', 'matrix', 'pivot'].includes(payload.spec.kind) ? 'Table' : 'Chart'
      return html`<button class="artifact-card" type="button" data-visual-id=${artifact.id} aria-label=${`Open visual details: ${title}`} @click=${() => this.openVisual(artifact.id, explorerHref, title)}>
        <span class="artifact-card-icon" aria-hidden="true">${lucideIcon(ChartColumn, { size: 18 })}</span>
        <span class="artifact-card-copy"><strong>${title}</strong><span>${kind} · Open details</span></span>
        <span class="artifact-card-chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 18 })}</span>
      </button>`
    }
    return html`<lv-visual-artifact type=${artifact.type} artifact-id=${artifact.id} .payload=${payload ?? null} .explorerHref=${explorerHref}></lv-visual-artifact>`
  }

  private openVisual(artifactId: string, explorerHref: string, title: string): void {
    this.dispatchEvent(new CustomEvent('lv-chat-visual-open', {
      bubbles: true,
      composed: true,
      detail: { artifactId, explorerHref, title },
    }))
  }

  private toolStatus(item: ChatTranscriptItemSignal): string {
    const status = item.status || 'running'
    if (status !== 'running' && status !== 'pending') return status
    if (!this.status.running) return 'interrupted'
    if (this.status.runId && item.runId && this.status.runId !== item.runId) return 'interrupted'
    return status
  }
}

type JsonRecord = Record<string, unknown>
type ExploreDimension = { sourceField: string; field: string; resultAlias?: string; grain?: DashboardTimeGrain; alias?: string }

function queryVisualExplorerURL(item: ChatTranscriptItemSignal | undefined, artifactType: string, artifactID: string): string {
  if (item?.kind !== 'tool' || item.name !== 'query_visual' || item.status !== 'complete' || !item.argumentsJson || !item.resultJson) return ''

  let input: unknown
  let result: unknown
  try {
    input = JSON.parse(item.argumentsJson)
    result = JSON.parse(item.resultJson)
  } catch {
    return ''
  }
  if (!isRecord(input) || !isNonEmptyString(input.semanticModelId) || !isRecord(input.visual) || !isRecord(result)) return ''
  if (!isRecord(result.semanticModelRef) || result.semanticModelRef.id !== input.semanticModelId || result.id !== artifactID || result.type !== artifactType || result.ok !== true || !isNonEmptyString(result.datasetId) || !Array.isArray(result.fields)) return ''
  if (!isEmptyArray(input.filters)) return ''

  const visual = input.visual
  if (!isEmptyRecord(visual.datasets) || !isEmptyArray(visual.calculations) || !isEmptyArray(visual.interactions)) return ''
  if (!isRecord(visual.query) || visual.query.type !== 'aggregate' || visual.type !== artifactType) return ''

  const query = visual.query
  if (!hasOnlyKeys(query, ['type', 'dimensions', 'metrics', 'sort', 'limit'])) return ''
  if (!Array.isArray(query.dimensions) || !Array.isArray(query.metrics)) return ''

  const dimensions: string[] = []
  const sortFields = new Map<string, string>()
  const selectedFields = new Set<string>()
  const timeFields = new Set<string>()
  let time: { field: string; grain: DashboardTimeGrain; alias?: string } | undefined
  for (const value of query.dimensions) {
    const dimension = exploreDimension(value, result.fields, result.semanticModelRef.id)
    if (!dimension || selectedFields.has(dimension.field)) return ''
    selectedFields.add(dimension.field)
    if (dimension.grain) {
      if (time) return ''
      time = { field: dimension.field, grain: dimension.grain, ...(dimension.alias ? { alias: dimension.alias } : {}) }
      timeFields.add(dimension.sourceField)
      if (dimension.alias) timeFields.add(dimension.alias)
    } else {
      if (dimension.alias) return ''
      dimensions.push(dimension.field)
      sortFields.set(dimension.sourceField, dimension.field)
      if (dimension.resultAlias) sortFields.set(dimension.resultAlias, dimension.field)
    }
  }

  const metrics: string[] = []
  for (const value of query.metrics) {
    const metric = exploreMetric(value)
    if (!metric || selectedFields.has(metric)) return ''
    selectedFields.add(metric)
    metrics.push(metric)
    sortFields.set(metric, metric)
  }
  if (selectedFields.size === 0) return ''

  const sort = exploreSort(query.sort, sortFields, timeFields)
  if (sort === null) return ''

  const explicitLimit = query.limit
  const maxRows = isRecord(visual.dataBudget) ? visual.dataBudget.maxRows : undefined
  const limit = explicitLimit ?? maxRows ?? 50
  if (!Number.isSafeInteger(limit) || (limit as number) <= 0 || (limit as number) > 1000) return ''

  return dataExplorerURL({
    mode: 'explore',
    explore: {
      semanticModelId: input.semanticModelId.trim(),
      datasetId: result.datasetId,
      dimensions,
      metrics,
      filters: [],
      sort,
      ...(time ? { time } : {}),
      limit: limit as number,
    },
  } as unknown as DataExplorerCommand)
}

function exploreDimension(value: unknown, fields: unknown[], semanticModelID: string): ExploreDimension | undefined {
  let sourceField: string
  let grain: DashboardTimeGrain | undefined
  let alias: string | undefined
  if (isNonEmptyString(value)) {
    sourceField = value.trim()
  } else {
    if (!isRecord(value) || !hasOnlyKeys(value, ['dimension', 'grain', 'alias']) || !isNonEmptyString(value.dimension)) return
    sourceField = value.dimension.trim()
    const grains: DashboardTimeGrain[] = ['second', 'minute', 'hour', 'day', 'week', 'month', 'quarter', 'year']
    if (value.grain !== undefined && (typeof value.grain !== 'string' || !grains.includes(value.grain as DashboardTimeGrain))) return
    if (value.alias !== undefined && !isNonEmptyString(value.alias)) return
    if (value.alias !== undefined && value.grain === undefined) return
    grain = value.grain as DashboardTimeGrain | undefined
    alias = isNonEmptyString(value.alias) ? value.alias.trim() : undefined
  }

  const semanticFieldID = sourceField.includes('.') ? sourceField : `${semanticModelID}.${sourceField}`
  const usage = fields.find((value) => isRecord(value) && value.role === 'dimension' && value.fieldId === semanticFieldID && (!alias || value.alias === alias))
  if (!isRecord(usage) || !isNonEmptyString(usage.explorerFieldId)) return
  return {
    sourceField,
    field: usage.explorerFieldId.trim(),
    ...(isNonEmptyString(usage.alias) ? { resultAlias: usage.alias.trim() } : {}),
    ...(grain ? { grain } : {}),
    ...(alias ? { alias } : {}),
  }
}

function exploreMetric(value: unknown): string | undefined {
  if (isNonEmptyString(value)) return value.trim()
  if (!isRecord(value) || !hasOnlyKeys(value, ['metric']) || !isNonEmptyString(value.metric)) return
  return value.metric.trim()
}

function exploreSort(value: unknown, fields: Map<string, string>, timeFields: Set<string>): { field: string; direction: 'asc' | 'desc' }[] | null {
  if (value === undefined) return []
  if (!Array.isArray(value)) return null
  const sort: { field: string; direction: 'asc' | 'desc' }[] = []
  for (const item of value) {
    if (!isRecord(item) || !hasOnlyKeys(item, ['field', 'direction']) || !isNonEmptyString(item.field)) return null
    const sourceField = item.field.trim()
    const targetField = fields.get(sourceField)
    if ((item.direction !== 'asc' && item.direction !== 'desc') || !targetField || timeFields.has(sourceField)) return null
    sort.push({ field: targetField, direction: item.direction })
  }
  return sort
}

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function hasOnlyKeys(value: JsonRecord, allowed: string[]): boolean {
  return Object.keys(value).every((key) => allowed.includes(key))
}

function isEmptyArray(value: unknown): boolean {
  return value === undefined || (Array.isArray(value) && value.length === 0)
}

function isEmptyRecord(value: unknown): boolean {
  return value === undefined || (isRecord(value) && Object.keys(value).length === 0)
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

if (!customElements.get('lv-chat-thread')) customElements.define('lv-chat-thread', ChatThread)
