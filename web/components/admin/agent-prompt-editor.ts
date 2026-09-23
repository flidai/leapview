import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { CodeXml, Eye } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/code-editor'
import '../shared/markdown-view'

type PromptMode = 'rendered' | 'raw'

class AgentPromptEditor extends LitElement {
  @property({ type: String }) value = ''
  @property({ type: Boolean, reflect: true }) disabled = false
  @state() private mode: PromptMode = 'rendered'

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      color: var(--lv-fg-default);
      --lv-agent-prompt-font-size: var(--text-codeBlock-size);
      --lv-agent-prompt-line-height: var(--base-text-lineHeight-snug);
      font-family: var(--fontStack-system);
    }

    .prompt-editor {
      display: grid;
      min-width: 0;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
    }

    .prompt-header {
      display: flex;
      align-items: start;
      justify-content: space-between;
      gap: var(--base-size-16);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-16);
    }

    .prompt-heading {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
    }

    .prompt-heading h3,
    .prompt-heading p {
      margin: 0;
    }

    .prompt-heading h3 {
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .prompt-heading p {
      max-width: 48rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
      line-height: var(--base-text-lineHeight-snug);
    }

    .managed-badge {
      flex: 0 0 auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel-muted);
      padding: var(--base-size-2) var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .prompt-control-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-12);
      min-height: 2.5rem;
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-4) var(--base-size-8) var(--base-size-4) var(--base-size-16);
      background: var(--lv-bg-panel-muted);
    }

    .prompt-source-label {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-code-block);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .mode-toggle {
      display: inline-flex;
      flex: 0 0 auto;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: 2px;
    }

    .mode-toggle button {
      display: inline-grid;
      width: 1.75rem;
      height: 1.75rem;
      place-items: center;
      border: 0;
      border-radius: calc(var(--lv-radius-default) - 2px);
      background: transparent;
      padding: 0;
      color: var(--lv-fg-muted);
      cursor: pointer;
    }

    .mode-toggle button:hover {
      color: var(--lv-fg-default);
    }

    .mode-toggle button.is-active {
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-default);
      box-shadow: var(--shadow-inset);
    }

    .mode-toggle button:focus-visible {
      position: relative;
      z-index: 1;
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 1px;
    }

    .prompt-body,
    .prompt-panel {
      display: grid;
      min-width: 0;
    }

    lv-markdown-view,
    .raw-markdown {
      box-sizing: border-box;
      width: 100%;
      min-height: 22rem;
      max-height: 42rem;
      overflow: auto;
      padding: var(--base-size-16);
    }

    .raw-markdown {
      margin: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-monospace);
      font-size: var(--lv-agent-prompt-font-size);
      line-height: var(--lv-agent-prompt-line-height);
      overflow-wrap: anywhere;
      tab-size: 2;
      white-space: pre-wrap;
    }

    @media (max-width: 640px) {
      .prompt-header {
        display: grid;
      }

      .prompt-control-row {
        padding-left: var(--base-size-12);
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    this.adoptValueAttribute()
  }

  attributeChangedCallback(name: string, oldValue: string | null, value: string | null): void {
    super.attributeChangedCallback(name, oldValue, value)
    if (name !== 'value' || oldValue === value) return
    this.value = value ?? ''
  }

  render() {
    const prompt = this.promptSource
    return html`
      <div class="prompt-editor">
        <div class="prompt-header">
          <div class="prompt-heading">
            <h3>System instructions</h3>
            <p>View the rendered instructions or inspect their raw Markdown source.</p>
          </div>
          ${this.disabled ? html`<span class="managed-badge">Deployment managed</span>` : nothing}
        </div>
        <div class="prompt-control-row">
          <span class="prompt-source-label">/SYSTEM.md</span>
          <div class="mode-toggle" role="group" aria-label="System instructions display">
            ${this.renderModeButton('rendered')}
            ${this.renderModeButton('raw')}
          </div>
        </div>
        <div class="prompt-body">
          <div class="prompt-panel" role="region" aria-label=${this.mode === 'rendered' ? 'Rendered system instructions' : 'Raw system instructions'}>
            ${this.mode === 'rendered' ? this.renderPreview(prompt) : this.renderRaw(prompt)}
          </div>
        </div>
      </div>
    `
  }

  private renderModeButton(mode: PromptMode) {
    const label = mode === 'rendered' ? 'Rendered Markdown' : 'Raw Markdown'
    return html`
      <button
        class=${this.mode === mode ? 'is-active' : ''}
        type="button"
        aria-label=${label}
        aria-pressed=${String(this.mode === mode)}
        title=${label}
        @click=${() => { this.mode = mode }}
      >${lucideIcon(mode === 'rendered' ? Eye : CodeXml, { size: 15, strokeWidth: 1.75 })}</button>
    `
  }

  private renderRaw(prompt: string) {
    return html`<pre class="raw-markdown" tabindex="0"><code>${prompt}</code></pre>`
  }

  private renderPreview(prompt: string) {
    return html`<lv-markdown-view compact .value=${prompt} emptyText="No system prompt configured."></lv-markdown-view>`
  }

  private get promptSource(): string {
    return this.value || this.getAttribute('value') || ''
  }

  private adoptValueAttribute(): void {
    if (this.value !== '') return
    const value = this.getAttribute('value')
    if (value !== null) this.value = value
  }
}

if (!customElements.get('lv-agent-prompt-editor')) customElements.define('lv-agent-prompt-editor', AgentPromptEditor)
