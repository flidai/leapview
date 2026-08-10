import { LitElement, css, html, nothing, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Eye, Save, SquarePen } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/code-editor'
import '../shared/markdown-view'

type PromptMode = 'preview' | 'edit'

class AgentPromptEditor extends LitElement {
  @property({ type: String }) value = ''
  @property({ type: Boolean, reflect: true }) disabled = false
  @state() private mode: PromptMode = 'preview'
  @state() private draft = ''
  @state() private status = ''
  private draftInitialized = false

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

    .prompt-status {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      line-height: var(--base-text-lineHeight-tight);
    }

    .prompt-control-row {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--base-size-8);
      justify-content: flex-end;
      padding: var(--base-size-8);
    }

    .prompt-actions {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      justify-content: flex-end;
      gap: var(--base-size-8);
    }

    .prompt-primary-actions {
      display: inline-flex;
      align-items: center;
      gap: var(--base-size-8);
    }

    .mode-toggle {
      display: inline-flex;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      padding: 2px;
    }

    .mode-toggle button,
    .save-button {
      border: 0;
      border-radius: calc(var(--lv-radius-default) - 2px);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
      line-height: var(--base-text-lineHeight-tight);
      cursor: pointer;
    }

    .mode-toggle button {
      display: inline-grid;
      width: 2rem;
      height: 2rem;
      place-items: center;
      background: transparent;
      padding: 0;
      color: var(--lv-fg-muted);
    }

    .mode-toggle button.is-active {
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      box-shadow: var(--shadow-inset);
    }

    .mode-toggle button:focus-visible,
    .save-button:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .prompt-body {
      display: grid;
      min-width: 0;
      padding: var(--base-size-8) var(--base-size-12) var(--base-size-12);
    }

    lv-code-editor,
    lv-markdown-view {
      box-sizing: border-box;
      width: 100%;
      min-height: 22rem;
    }

    lv-markdown-view {
      max-height: 42rem;
      overflow: auto;
      padding: var(--base-size-16);
    }

    lv-code-editor {
      --lv-code-editor-border: 0;
      --lv-code-editor-font-size: var(--lv-agent-prompt-font-size);
      --lv-code-editor-line-height: var(--lv-agent-prompt-line-height);
      --lv-code-editor-radius: 0;
    }

    .prompt-panel {
      min-width: 0;
      grid-area: 1 / 1;
    }

    .prompt-panel.is-hidden {
      visibility: hidden;
      pointer-events: none;
    }

    .save-button {
      display: inline-flex;
      align-items: center;
      gap: var(--base-size-6);
      background: var(--lv-bg-accent);
      padding: var(--base-size-6) var(--base-size-12);
      color: var(--lv-fg-on-accent);
    }

    .save-button:disabled {
      cursor: not-allowed;
      opacity: 0.6;
    }

    @media (max-width: 640px) {
      .prompt-control-row {
        padding-inline: var(--base-size-8);
      }

      .prompt-actions {
        width: 100%;
      }

      .prompt-status {
        margin-right: auto;
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    this.adoptValueAttribute()
  }

  attributeChangedCallback(name: string, oldValue: string | null, value: string | null): void {
    super.attributeChangedCallback(name, oldValue, value)
    if (name !== 'value' || oldValue === value || this.dirty) return
    this.value = value ?? ''
    this.draft = this.value
    this.draftInitialized = true
  }

  protected willUpdate(changed: PropertyValues<this>): void {
    if ((changed.has('value') || !this.draftInitialized) && !this.dirty) {
      this.draft = this.promptSource
      this.draftInitialized = true
    }
  }

  render() {
    const prompt = this.currentPrompt
    const canSave = !this.disabled && this.dirty && prompt.trim().length > 0
    const status = this.statusLabel
    const showSave = canSave
    return html`
      <div class="prompt-editor">
        <div class="prompt-control-row">
          <div class="prompt-actions">
            <div class="prompt-primary-actions">
              ${status ? html`<span class="prompt-status">${status}</span>` : nothing}
              ${showSave ? html`
                <button class="save-button" type="button" @click=${this.savePrompt}>
                  ${lucideIcon(Save, { size: 14, strokeWidth: 2 })}
                  <span>Save</span>
                </button>
              ` : nothing}
            </div>
            <div class="mode-toggle" role="group" aria-label="System prompt view mode">
              ${this.renderModeButton('preview')}
              ${this.renderModeButton('edit')}
            </div>
          </div>
        </div>
        <div class="prompt-body">
          <div
            class=${this.mode === 'preview' ? 'prompt-panel' : 'prompt-panel is-hidden'}
            aria-hidden=${String(this.mode !== 'preview')}
            ?inert=${this.mode !== 'preview'}
          >
            ${this.renderPreview(prompt)}
          </div>
          <div
            class=${this.mode === 'edit' ? 'prompt-panel' : 'prompt-panel is-hidden'}
            aria-hidden=${String(this.mode !== 'edit')}
            ?inert=${this.mode !== 'edit'}
          >
            ${this.renderEditor(prompt)}
          </div>
        </div>
      </div>
    `
  }

  private renderModeButton(mode: PromptMode) {
    const label = mode === 'preview' ? 'Preview' : 'Edit'
    return html`
      <button
        class=${this.mode === mode ? 'is-active' : ''}
        type="button"
        aria-label=${label}
        aria-pressed=${String(this.mode === mode)}
        title=${label}
        @click=${() => {
          if (!this.dirty) this.draft = this.promptSource
          this.mode = mode
        }}
      >${lucideIcon(mode === 'preview' ? Eye : SquarePen, { size: 15, strokeWidth: 2 })}</button>
    `
  }

  private renderEditor(prompt: string) {
    return html`
      <lv-code-editor
        aria-label="System prompt"
        language="markdown"
        value=${prompt}
        .value=${prompt}
        ?disabled=${this.disabled}
        @lv-code-editor-change=${this.updateDraftFromCodeEditor}
      ></lv-code-editor>
    `
  }

  private renderPreview(prompt: string) {
    return html`<lv-markdown-view compact .value=${prompt} emptyText="No system prompt configured."></lv-markdown-view>`
  }

  private updateDraftFromCodeEditor(event: CustomEvent<{ value: string }>): void {
    this.draft = event.detail.value
    this.draftInitialized = true
    this.status = this.dirty ? 'unsaved' : ''
  }

  private savePrompt(): void {
    const systemPrompt = this.currentPrompt.trim()
    if (this.disabled || !systemPrompt) return
    this.dispatchEvent(new CustomEvent('lv-agent-system-prompt-save', {
      bubbles: true,
      composed: true,
      detail: { systemPrompt },
    }))
    this.value = systemPrompt
    this.draft = systemPrompt
    this.status = 'saved'
  }

  private get statusLabel(): string {
    if (this.disabled) return 'Read-only'
    if (this.dirty) return 'Unsaved changes'
    if (this.status === 'saved' && this.mode === 'edit') return 'Saved'
    return ''
  }

  private get promptSource(): string {
    return this.value || this.getAttribute('value') || ''
  }

  private get dirty(): boolean {
    if (!this.draftInitialized) return false
    return this.draft !== this.promptSource
  }

  private get currentPrompt(): string {
    if (this.dirty) return this.draft
    if (this.draft) return this.draft
    return this.promptSource
  }

  private adoptValueAttribute(): void {
    if (this.dirty || this.value !== '') return
    const value = this.getAttribute('value')
    if (value === null) return
    this.value = value
    this.draft = value
    this.draftInitialized = true
  }
}

if (!customElements.get('lv-agent-prompt-editor')) customElements.define('lv-agent-prompt-editor', AgentPromptEditor)
