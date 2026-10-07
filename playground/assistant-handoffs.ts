import { LitElement, css, html } from 'lit'
import { state } from 'lit/decorators.js'
import type { ChatTranscriptItemSignal } from '../web/generated/signals'
import { createArtifactFixture } from './content-fixtures'
import { visualExplorerHref } from '../web/components/chat/visual-action-links'
import '../web/components/chat/chat-thread'
import '../web/components/chat/chat-visual-panel'

/** Production components with local receipts; destination links never navigate. */
class AssistantHandoffs extends LitElement {
  @state() private visualOpen = false
  @state() private destination = 'Choose a handoff to inspect its destination.'
  private readonly envelope = createArtifactFixture('ready')
  private readonly transcript: ChatTranscriptItemSignal[] = [
    {
      id: 'chart', kind: 'tool', name: 'query_visual', status: 'complete', runId: 'demo-run', toolCallId: 'demo-chart',
      artifact: { id: this.envelope.visualID, type: 'bar', summary: 'Regional revenue' },
      argumentsJson: JSON.stringify({ semanticModelId: 'sales', visual: { type: 'bar', query: { type: 'aggregate', dimensions: ['region'], metrics: ['revenue'], limit: 50 } } }),
      resultJson: JSON.stringify({ ok: true, id: this.envelope.visualID, type: 'bar', datasetId: 'orders', semanticModelRef: { id: 'sales' }, fields: [{ fieldId: 'sales.region', role: 'dimension', explorerFieldId: 'orders.region' }] }),
    },
    {
      id: 'dashboard', kind: 'tool', name: 'create_dashboard_draft', status: 'complete', runId: 'demo-run', toolCallId: 'demo-dashboard',
      argumentsJson: JSON.stringify({ title: 'Finance overview' }),
      resultJson: JSON.stringify({ lifecycle: { id: 'finance', draft: { id: 'draft-1' } } }),
    },
    {
      id: 'search', kind: 'tool', name: 'catalog_search', status: 'complete', runId: 'demo-run', toolCallId: 'demo-search',
      argumentsJson: JSON.stringify({ query: 'finance', kinds: ['semantic_model'], domain: 'sales', limit: 10 }),
      resultJson: JSON.stringify({ items: [], count: 0, hasMore: false }),
    },
  ]

  static styles = css`
    :host { display:block; min-width:0; }
    .preview { display:grid; grid-template-columns:minmax(0, 1fr); min-height:24rem; }
    .preview.open { grid-template-columns:minmax(16rem, 1fr) minmax(18rem, 1fr); }
    lv-chat-thread { height:28rem; }
    lv-chat-visual-panel { height:32rem; }
    .destination { overflow-wrap:anywhere; color:var(--lv-fg-muted); font:var(--lv-type-body); }
    @media (max-width:700px) { .preview.open { grid-template-columns:minmax(0, 1fr); } }
  `

  render() {
    return html`<p class="destination">Illustrative recorded actions. Open the chart, then use its three-dot menu. Links display their destination without navigating or creating data.</p>
      <p class="destination" role="status">${this.destination}</p>
      <div class=${this.visualOpen ? 'preview open' : 'preview'} @click=${this.inspectLink}>
        <lv-chat-thread conversation-id="demo-conversation" .status=${{ enabled: true, running: false }}
          .transcript=${this.transcript} .visuals=${{ [this.envelope.visualID]: this.envelope }}
          @lv-chat-visual-open=${() => { this.visualOpen = true }}></lv-chat-thread>
        ${this.visualOpen ? html`<lv-chat-visual-panel title="Regional revenue" .artifactId=${this.envelope.visualID}
          .payload=${this.envelope} .auditHref=${visualExplorerHref('demo-conversation', this.envelope.visualID, 'demo-run')}
          @lv-chat-visual-close=${() => { this.visualOpen = false }}></lv-chat-visual-panel>` : null}
      </div>`
  }

  private inspectLink(event: MouseEvent): void {
    const link = event.composedPath().find(node => node instanceof HTMLAnchorElement)
    if (!(link instanceof HTMLAnchorElement)) return
    event.preventDefault()
    this.destination = `${link.textContent?.trim()} → ${link.getAttribute('href')}`
    this.dispatchEvent(new CustomEvent('playground-event', { bubbles: true, composed: true, detail: { name: 'assistant-handoff', detail: { href: link.getAttribute('href') } } }))
  }
}

if (!customElements.get('playground-assistant-handoffs')) customElements.define('playground-assistant-handoffs', AssistantHandoffs)
