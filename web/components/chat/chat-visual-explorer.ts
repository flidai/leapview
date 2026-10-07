import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { ChatTranscriptItemSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import '../shared/visual-artifact'
import { visualDataExplorerHref } from './visual-action-links'

class ChatVisualExplorer extends LitElement {
  @property({ attribute: 'chat-href' }) chatHref = ''
  @property({ attribute: 'model-href' }) modelHref = ''
  @state() private visual: VisualizationEnvelope | null = null
  @state() private transcript: ChatTranscriptItemSignal | null = null

  connectedCallback(): void {
    super.connectedCallback()
    try {
      this.visual = JSON.parse(this.getAttribute('data-visual') || 'null')
      this.transcript = JSON.parse(this.getAttribute('data-transcript') || 'null')
    } catch {
      this.visual = null
      this.transcript = null
    }
  }

  static styles = css`
    :host { display:block; height:100%; overflow:auto; color:var(--lv-fg-default); }
    * { box-sizing:border-box; }
    main { max-width:1440px; margin:0 auto; padding:24px; }
    header { display:flex; flex-wrap:wrap; align-items:start; justify-content:space-between; gap:16px; margin-bottom:20px; }
    h1 { font:var(--lv-type-title, 600 24px/1.3 sans-serif); margin:6px 0; }
    .eyebrow, .description { color:var(--lv-fg-muted); font:var(--lv-type-body); margin:0; }
    nav { display:flex; gap:8px; flex-wrap:wrap; }
    a { color:var(--lv-fg-link, var(--lv-fg-default)); text-decoration:none; }
    nav a { display:inline-flex; padding:8px 12px; border:var(--lv-border-default); border-radius:var(--lv-radius-default); background:var(--lv-bg-panel); font:var(--lv-type-body); }
    a:hover { text-decoration:underline; }
    a:focus-visible, summary:focus-visible { outline:2px solid var(--lv-fg-link, currentColor); outline-offset:3px; }
    lv-visual-artifact { display:block; height:clamp(320px, 58vh, 680px); margin-bottom:20px; }
    .metadata { display:flex; gap:16px; flex-wrap:wrap; color:var(--lv-fg-muted); font:var(--lv-type-caption); margin:12px 0; }
    .audit { display:grid; gap:10px; }
    details { border:var(--lv-border-default); border-radius:var(--lv-radius-default); background:var(--lv-bg-panel); overflow:hidden; }
    summary { cursor:pointer; padding:12px 16px; font:var(--lv-type-body); }
    pre { margin:0; padding:0 16px 16px; overflow:auto; max-height:420px; font:12px/1.6 monospace; white-space:pre; }
    @media (max-width:700px) { main { padding:16px; } nav { width:100%; } }
  `

  render() {
    const visual = this.visual
    const item = this.transcript
    if (!visual || !item) return html`<main><h1>Visual unavailable</h1><a href=${this.chatHref}>Back to chat</a></main>`
    const explorerHref = visualDataExplorerHref(item)
    const input = parseRecord(item.argumentsJson)
    const authoredVisual = parseRecordValue(input.visual)
    const result = parseRecord(item.resultJson)
    const filters = input.filters
    const date = item.createdAt ? new Date(item.createdAt) : null
    const recordedAt = date && !Number.isNaN(date.valueOf()) ? date.toLocaleString() : ''
    return html`
      <main>
        <header>
          <div><p class="eyebrow">Visual Explorer</p><h1>${visual.spec.title || item.artifact?.summary || 'Saved visual'}</h1>
            <p class="description">Saved result from this chat. Inspect the original query, filters, and chart settings below.</p></div>
          <nav aria-label="Visual actions">
            <a href=${this.chatHref}>Back to chat</a>
            ${explorerHref ? html`<a href=${explorerHref}>Edit query in Explorer</a>` : nothing}
            ${!explorerHref && this.modelHref ? html`<a href=${this.modelHref}>Start a new query from this model</a>` : nothing}
          </nav>
        </header>
        ${!explorerHref && this.modelHref ? html`<p class="description">This visual uses query settings the query editor cannot yet represent. The saved chart and full original settings remain available below; starting a new query opens the model with fresh settings.</p>` : nothing}
        <lv-visual-artifact type=${item.artifact?.type || visual.spec.kind} artifact-id=${visual.visualID} .payload=${visual}></lv-visual-artifact>
        <div class="metadata">
          ${recordedAt ? html`<span>Recorded ${recordedAt}</span>` : nothing}
          ${typeof input.semanticModelId === 'string' ? html`<span>Model: ${input.semanticModelId}</span>` : nothing}
          ${typeof result.datasetId === 'string' ? html`<span>Dataset: ${result.datasetId}</span>` : nothing}
        </div>
        <section class="audit" aria-label="Visual audit details">
          <details open><summary>Original query</summary><pre>${pretty(authoredVisual.query ?? input)}</pre></details>
          <details><summary>Filters${Array.isArray(filters) ? ` (${filters.length})` : ''}</summary><pre>${pretty(filters ?? [])}</pre></details>
          <details><summary>Chart settings</summary><pre>${pretty(authoredVisual)}</pre></details>
          <details><summary>Full tool input</summary><pre>${pretty(input)}</pre></details>
          <details><summary>Result and data provenance</summary><pre>${pretty(result)}</pre></details>
          <details><summary>Rendered visual specification</summary><pre>${pretty(visual.spec)}</pre></details>
        </section>
      </main>
    `
  }
}

function parseRecord(raw?: string): Record<string, unknown> {
  try { return parseRecordValue(JSON.parse(raw || '{}')) } catch { return {} }
}

function parseRecordValue(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function pretty(value: unknown): string { return JSON.stringify(value, null, 2) ?? '' }

if (!customElements.get('lv-chat-visual-explorer')) customElements.define('lv-chat-visual-explorer', ChatVisualExplorer)
