import { LitElement, css, html, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import { loadMonacoRuntime } from './monaco-runtime'

type MonacoApi = Awaited<ReturnType<typeof loadMonacoRuntime>>
type MonacoEditor = ReturnType<MonacoApi['editor']['create']>
type MonacoModel = ReturnType<MonacoApi['editor']['createModel']>
type MonacoDisposable = ReturnType<MonacoEditor['onDidChangeModelContent']>

const monacoStylesheetPath = '/static/monaco-editor-css.css'

class CodeEditor extends LitElement {
  @property({ type: String }) value = ''
  @property({ type: String }) language = 'text'
  @property({ type: Boolean, reflect: true }) disabled = false
  @property({ attribute: 'aria-label' }) ariaLabel = 'Code editor'
  @state() private loading = true
  @state() private error = ''

  private monaco: MonacoApi | null = null
  private editor: MonacoEditor | null = null
  private model: MonacoModel | null = null
  private contentChangeDisposable: MonacoDisposable | null = null
  private suppressChange = false
  private hasLocalValueChange = false

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      color: var(--lv-fg-default);
      --lv-code-editor-font-family: var(--fontStack-monospace);
      --lv-code-editor-font-size: var(--text-codeBlock-size);
      --lv-code-editor-line-height: var(--base-text-lineHeight-snug);
      font-family: var(--fontStack-system);
    }

    .editor-shell {
      position: relative;
      display: grid;
      overflow: hidden;
      min-width: 0;
      min-height: 22rem;
      border: var(--lv-code-editor-border, var(--lv-border-muted));
      border-radius: var(--lv-code-editor-radius, var(--lv-radius-default));
      background: var(--lv-bg-panel);
    }

    .monaco-host {
      box-sizing: border-box;
      width: 100%;
      height: 100%;
      min-height: 22rem;
    }

    .monaco-host.is-loading {
      opacity: 0;
      pointer-events: none;
    }

    .loading {
      position: absolute;
      top: var(--base-size-8);
      right: var(--base-size-8);
      display: grid;
      place-items: center;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-4) var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      pointer-events: none;
    }

    textarea {
      box-sizing: border-box;
      width: 100%;
      min-height: 22rem;
      resize: vertical;
      border: 0;
      background: var(--lv-bg-panel);
      padding: var(--base-size-16);
      color: var(--lv-fg-default);
      font: inherit;
      font-family: var(--lv-code-editor-font-family);
      font-size: var(--lv-code-editor-font-size);
      line-height: var(--lv-code-editor-line-height);
      white-space: pre-wrap;
    }

    textarea:disabled {
      cursor: not-allowed;
      opacity: 0.72;
    }

    .fallback-note {
      margin: 0;
      border-top: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-16);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

  `

  connectedCallback(): void {
    super.connectedCallback()
    this.adoptValueAttribute()
  }

  attributeChangedCallback(name: string, oldValue: string | null, value: string | null): void {
    super.attributeChangedCallback(name, oldValue, value)
    if (name === 'value' && oldValue !== value && !this.hasLocalValueChange) {
      this.value = value ?? ''
    }
  }

  firstUpdated(): void {
    void this.initializeEditor()
  }

  protected updated(changed: PropertyValues<this>): void {
    if (changed.has('value')) this.syncValueToEditor()
    if (changed.has('language')) this.syncLanguageToEditor()
    if (changed.has('disabled') || changed.has('ariaLabel')) this.syncOptionsToEditor()
  }

  disconnectedCallback(): void {
    this.disposeEditor()
    super.disconnectedCallback()
  }

  render() {
    return html`
      ${!this.error ? html`<link data-monaco-styles rel="stylesheet" href=${monacoStylesheetPath}>` : ''}
      <div class="editor-shell">
        ${this.error ? html`
          <textarea
            aria-label=${this.ariaLabel}
            .value=${this.sourceValue}
            ?disabled=${this.disabled}
            @input=${this.handleFallbackInput}
          ></textarea>
          <p class="fallback-note">${this.error}</p>
        ` : html`
          <div class=${this.loading ? 'monaco-host is-loading' : 'monaco-host'} aria-label=${this.ariaLabel}></div>
          ${this.loading ? html`<div class="loading">Loading editor...</div>` : ''}
        `}
      </div>
    `
  }

  private async initializeEditor(): Promise<void> {
    try {
      const host = this.shadowRoot?.querySelector<HTMLElement>('.monaco-host')
      if (!host) return
      await this.waitForMonacoStyles()
      if (!this.isConnected) return
      const monaco = await loadMonacoRuntime()
      if (!this.isConnected) return
      this.monaco = monaco
      const typography = this.editorTypography()
      this.model = monaco.editor.createModel(this.sourceValue, this.languageID)
      this.editor = monaco.editor.create(host, {
        model: this.model,
        ariaLabel: this.ariaLabel,
        automaticLayout: true,
        contextmenu: false,
        cursorWidth: 1,
        fontFamily: typography.fontFamily,
        fontSize: typography.fontSize,
        folding: false,
        glyphMargin: false,
        lineDecorationsWidth: 12,
        lineHeight: typography.lineHeight,
        lineNumbers: 'on',
        lineNumbersMinChars: 3,
        minimap: { enabled: false },
        readOnly: this.disabled,
        scrollBeyondLastLine: false,
        tabSize: 2,
        theme: currentTheme(),
        wordWrap: 'on',
        wrappingIndent: 'same',
      })
      this.contentChangeDisposable = this.editor.onDidChangeModelContent(() => {
        if (this.suppressChange || !this.editor) return
        const value = this.editor.getValue()
        this.hasLocalValueChange = true
        this.value = value
        this.dispatchChange(value)
      })
      this.loading = false
      this.error = ''
    } catch {
      this.disposeEditor()
      this.loading = false
      this.error = 'Editor failed to load. Using basic text editing.'
    }
  }

  private syncValueToEditor(): void {
    const value = this.sourceValue
    if (!this.editor || this.editor.getValue() === value) return
    this.suppressChange = true
    this.editor.setValue(value)
    this.suppressChange = false
  }

  private syncLanguageToEditor(): void {
    if (!this.monaco || !this.model) return
    this.monaco.editor.setModelLanguage(this.model, this.languageID)
  }

  private syncOptionsToEditor(): void {
    this.editor?.updateOptions({
      ariaLabel: this.ariaLabel,
      readOnly: this.disabled,
    })
  }

  private handleFallbackInput(event: Event): void {
    const value = (event.target as HTMLTextAreaElement).value
    this.hasLocalValueChange = true
    this.value = value
    this.dispatchChange(value)
  }

  private dispatchChange(value: string): void {
    this.dispatchEvent(new CustomEvent('lv-code-editor-change', {
      bubbles: true,
      composed: true,
      detail: { value },
    }))
  }

  private disposeEditor(): void {
    this.contentChangeDisposable?.dispose()
    this.contentChangeDisposable = null
    this.editor?.dispose()
    this.editor = null
    this.model?.dispose()
    this.model = null
  }

  private get languageID(): string {
    if (['markdown', 'yaml', 'json', 'sql', 'text'].includes(this.language)) return this.language
    return 'text'
  }

  private get sourceValue(): string {
    if (!this.hasLocalValueChange && this.value === '') return this.getAttribute('value') || ''
    return this.value
  }

  private adoptValueAttribute(): void {
    if (this.hasLocalValueChange || this.value !== '') return
    const value = this.getAttribute('value')
    if (value !== null) this.value = value
  }

  private editorTypography(): { fontFamily: string; fontSize: number; lineHeight: number } {
    const styles = getComputedStyle(this)
    const fontFamily = styles.getPropertyValue('--lv-code-editor-font-family').trim() || 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace'
    const fontSize = cssLengthToPixels(styles.getPropertyValue('--lv-code-editor-font-size'), 14)
    const lineHeightToken = styles.getPropertyValue('--lv-code-editor-line-height').trim()
    const lineHeight = Math.round(cssLineHeightToPixels(lineHeightToken, fontSize, fontSize * 1.35))
    return { fontFamily, fontSize, lineHeight }
  }

  private waitForMonacoStyles(): Promise<void> {
    const link = this.shadowRoot?.querySelector<HTMLLinkElement>('link[data-monaco-styles]')
    if (!link) return Promise.resolve()
    if (link.sheet) return Promise.resolve()
    return new Promise((resolve, reject) => {
      const cleanup = () => {
        link.removeEventListener('load', handleLoad)
        link.removeEventListener('error', handleError)
      }
      const handleLoad = () => {
        cleanup()
        resolve()
      }
      const handleError = () => {
        cleanup()
        reject(new Error('Monaco stylesheet failed to load'))
      }
      link.addEventListener('load', handleLoad, { once: true })
      link.addEventListener('error', handleError, { once: true })
    })
  }
}

function cssLengthToPixels(value: string, fallback: number): number {
  const trimmed = value.trim()
  if (!trimmed) return fallback
  const numeric = Number.parseFloat(trimmed)
  if (!Number.isFinite(numeric)) return fallback
  if (trimmed.endsWith('rem')) return numeric * rootFontSize()
  if (trimmed.endsWith('em')) return numeric * fallback
  return numeric
}

function cssLineHeightToPixels(value: string, fontSize: number, fallback: number): number {
  const trimmed = value.trim()
  if (!trimmed) return fallback
  if (trimmed.endsWith('px') || trimmed.endsWith('rem') || trimmed.endsWith('em')) {
    return cssLengthToPixels(trimmed, fallback)
  }
  const numeric = Number.parseFloat(trimmed)
  if (!Number.isFinite(numeric)) return fallback
  return numeric * fontSize
}

function rootFontSize(): number {
  const size = Number.parseFloat(getComputedStyle(document.documentElement).fontSize)
  return Number.isFinite(size) ? size : 16
}

function currentTheme(): 'github-light' | 'github-dark' {
  if (document.documentElement.style.colorScheme === 'dark') return 'github-dark'
  return 'github-light'
}

if (!customElements.get('lv-code-editor')) customElements.define('lv-code-editor', CodeEditor)
