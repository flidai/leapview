import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { Check, Code2, Copy, WrapText } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/code-block'

/** Read-only SQL presentation. Formatting never changes the source copied to the clipboard. */
class DataExplorerSQL extends LitElement {
  @property({ type: String }) sql = ''
  @property({ type: String }) emptyMessage = 'No SQL is available for this result.'
  @state() private wrap = false
  @state() private formatted = true
  @state() private copyState: 'idle' | 'copied' | 'failed' = 'idle'
  private copyTimer = 0
  private copyRequest = 0

  static styles = css`
    :host { display: block; min-width: 0; }
    .sql-view { min-width: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); overflow: hidden; }
    .sql-toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--base-size-8); border-bottom: var(--lv-border-muted); background: var(--lv-bg-panel-muted); padding: var(--base-size-8) var(--base-size-12); }
    .sql-label { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .sql-actions { display: flex; flex-wrap: wrap; min-width: 0; max-width: 100%; align-items: center; gap: var(--base-size-8); }
    button { display: inline-flex; align-items: center; justify-content: center; gap: var(--base-size-6); min-height: var(--control-medium-size); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding: 0 var(--base-size-8); font: var(--lv-type-caption); cursor: pointer; }
    button:hover { background: var(--lv-bg-control-hover); }
    button:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
    button[aria-pressed="true"] { background: var(--lv-bg-accent-muted); color: var(--lv-fg-accent); }
    .sql-status { margin: 0; color: var(--lv-fg-danger); font: var(--lv-type-caption); padding: var(--base-size-8) var(--base-size-12); }
    .sql-empty { color: var(--lv-fg-muted); font: var(--lv-type-body); }
    .sql-view lv-code-block .code-block-shell { border: 0; border-radius: 0; }
    .sql-view lv-code-block .shiki,
    .sql-view lv-code-block .code-block-fallback { max-height: min(36rem, 55vh); font-size: 14px; line-height: 1.6; white-space: pre; overflow-wrap: normal; word-break: normal; }
    .sql-view.wrap lv-code-block .shiki,
    .sql-view.wrap lv-code-block .code-block-fallback { white-space: pre-wrap; overflow-wrap: anywhere; }
    .sql-view.wrap lv-code-block .shiki code { min-width: 0; }
    .sql-view.wrap lv-code-block .shiki .line { display: inline; }
  `

  willUpdate(changed: Map<string, unknown>): void {
    if (changed.has('sql')) {
      this.copyRequest += 1
      window.clearTimeout(this.copyTimer)
      this.copyState = 'idle'
    }
  }

  disconnectedCallback(): void {
    this.copyRequest += 1
    window.clearTimeout(this.copyTimer)
    super.disconnectedCallback()
  }

  render() {
    if (!this.sql) return html`<p class="sql-empty">${this.emptyMessage}</p>`
    return html`
      <section class=${`sql-view${this.wrap ? ' wrap' : ''}`} aria-label="Generated SQL, read only">
        <div class="sql-toolbar">
          <span class="sql-label">SQL · Read only</span>
          <div class="sql-actions">
            <button type="button" aria-label="Format SQL" aria-pressed=${String(this.formatted)} title=${this.formatted ? 'Show original SQL formatting' : 'Format SQL for readability'} @click=${() => this.formatted = !this.formatted}>
              ${lucideIcon(Code2, { size: 14 })} Format: ${this.formatted ? 'On' : 'Off'}
            </button>
            <button type="button" aria-label="Wrap SQL lines" aria-pressed=${String(this.wrap)} title=${this.wrap ? 'Turn off line wrapping' : 'Wrap long SQL lines'} @click=${() => this.wrap = !this.wrap}>
              ${lucideIcon(WrapText, { size: 14 })} Wrap: ${this.wrap ? 'On' : 'Off'}
            </button>
            <button type="button" title="Copy the original executed SQL" @click=${this.copySQL}>
              ${lucideIcon(this.copyState === 'copied' ? Check : Copy, { size: 14 })} ${this.copyState === 'copied' ? 'Copied' : 'Copy SQL'}
            </button>
          </div>
        </div>
        ${keyed(`${this.formatted}:${this.sql}`, html`<lv-code-block .code=${this.sql} language="sql" ?format=${this.formatted}></lv-code-block>`)}
        <div role="status" aria-live="polite">${this.copyState === 'failed' ? html`<p class="sql-status">Could not copy SQL. Select the query and copy it manually.</p>` : this.copyState === 'copied' ? html`<span class="sql-label">SQL copied to clipboard.</span>` : nothing}</div>
      </section>
    `
  }

  private copySQL = async (): Promise<void> => {
    const sql = this.sql
    const request = ++this.copyRequest
    window.clearTimeout(this.copyTimer)
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(sql)
      } else {
        const textarea = document.createElement('textarea')
        textarea.value = sql
        textarea.setAttribute('readonly', '')
        textarea.style.position = 'fixed'
        textarea.style.opacity = '0'
        document.body.append(textarea)
        try {
          textarea.select()
          if (!document.execCommand('copy')) throw new Error('Clipboard unavailable')
        } finally {
          textarea.remove()
        }
      }
      if (request !== this.copyRequest || sql !== this.sql || !this.isConnected) return
      this.copyState = 'copied'
      this.copyTimer = window.setTimeout(() => this.copyState = 'idle', 1600)
    } catch {
      if (request === this.copyRequest && sql === this.sql && this.isConnected) this.copyState = 'failed'
    }
  }
}

if (!customElements.get('lv-data-explorer-sql')) customElements.define('lv-data-explorer-sql', DataExplorerSQL)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explorer-sql': DataExplorerSQL
  }
}
