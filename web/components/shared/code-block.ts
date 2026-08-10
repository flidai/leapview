import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { unsafeHTML } from 'lit/directives/unsafe-html.js'
import type { HighlighterCore } from 'shiki/core'
import type { ShikiTransformer } from '@shikijs/types'
import { Check, Copy } from 'lucide'
import { lucideIcon } from './lucide-icons'

type CodeTheme = 'github-light' | 'github-dark'
type SupportedLanguage = 'json' | 'shellscript' | 'sql' | 'toon' | 'yaml'

let highlighterPromise: Promise<HighlighterCore> | null = null

function loadHighlighter(): Promise<HighlighterCore> {
  highlighterPromise ??= (async () => {
    const [
      { createHighlighterCore },
      { createJavaScriptRegexEngine },
      { default: sql },
      { default: json },
      { default: shellscript },
      { default: yaml },
      { default: githubDark },
      { default: githubLight },
      { toonLanguage },
    ] = await Promise.all([
      import('shiki/core'),
      import('shiki/engine/javascript'),
      import('@shikijs/langs/sql'),
      import('@shikijs/langs/json'),
      import('@shikijs/langs/shellscript'),
      import('@shikijs/langs/yaml'),
      import('@shikijs/themes/github-dark'),
      import('@shikijs/themes/github-light'),
      import('./toon-language'),
    ])
    return createHighlighterCore({
      themes: [githubLight, githubDark],
      langs: [json, shellscript, sql, toonLanguage, yaml],
      engine: createJavaScriptRegexEngine(),
    })
  })()
  return highlighterPromise
}

class CodeBlock extends LitElement {
  @property({ type: String }) code = ''
  @property({ type: String }) language = 'sql'
  @property({ type: Boolean, reflect: true }) compact = false
  @property({ type: Boolean, reflect: true }) format = false
  @property({ type: Boolean, reflect: true }) copy = false
  @property({ type: Boolean, reflect: true }) dense = false
  @property({ type: Boolean, reflect: true }) toolbar = false
  @property({ attribute: false }) highlightedLines: number[] = []
  @state() private highlighted = ''
  @state() private error = ''
  @state() private copied = false
  @state() private preparedCode = ''
  private preparedKey = ''
  private renderToken = 0
  private copiedTimeout = 0
  private highlightPromise: Promise<void> = Promise.resolve()
  private focusedLineNumbers = new Set<number>()

  createRenderRoot(): HTMLElement {
    return this
  }

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('leapview-theme-applied', this.handleThemeApplied)
  }

  disconnectedCallback(): void {
    document.removeEventListener('leapview-theme-applied', this.handleThemeApplied)
    window.clearTimeout(this.copiedTimeout)
    super.disconnectedCallback()
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has('code') || changed.has('language') || changed.has('format') || changed.has('highlightedLines')) {
      this.highlightPromise = this.highlight()
    }
    if (changed.has('highlighted')) this.applyFocusedLines()
  }

  focusLines(lines: readonly number[]): void {
    this.focusedLineNumbers = new Set(lines)
    this.toggleAttribute('data-line-focus', this.focusedLineNumbers.size > 0)
    this.applyFocusedLines()
  }

  clearFocusedLines(): void {
    this.focusedLineNumbers.clear()
    this.removeAttribute('data-line-focus')
    this.applyFocusedLines()
  }

  protected async getUpdateComplete(): Promise<boolean> {
    const complete = await super.getUpdateComplete()
    await this.highlightPromise
    await super.getUpdateComplete()
    return complete
  }

  render() {
    const code = this.displayCode
    return html`
      <style>
        ${codeBlockStyles}
      </style>
      <div class="code-block-shell">
        ${this.toolbar ? html`
          <div class="code-block-toolbar">
            <span class="code-block-language">${languageLabel(this.language)}</span>
            ${this.copy && code ? this.copyButton : nothing}
          </div>
        ` : this.copy && code ? this.copyButton : nothing}
        ${this.error
          ? html`<pre class="code-block-fallback"><code>${code}</code></pre>`
          : this.highlighted
            ? unsafeHTML(this.highlighted)
            : html`<pre class="code-block-fallback"><code>${code || 'Loading...'}</code></pre>`}
        ${this.error ? html`<p class="code-block-error">${this.error}</p>` : nothing}
      </div>
    `
  }

  private handleThemeApplied = (): void => {
    this.highlightPromise = this.highlight()
  }

  private async highlight(): Promise<void> {
    const token = ++this.renderToken
    const source = this.sourceCode
    const language = supportedLanguage(this.language)
    if (!source.trim() || !language) {
      this.setPreparedCode(source)
      this.highlighted = ''
      this.error = ''
      return
    }
    try {
      const code = await this.prepareCode(source, language)
      const highlighter = await loadHighlighter()
      if (token !== this.renderToken) return
      this.setPreparedCode(code)
      const highlightedLines = new Set(this.highlightedLines)
      const lineHighlightTransformer: ShikiTransformer = {
        name: 'leapview-highlighted-lines',
        line(hast, line) {
          hast.properties['data-code-line'] = String(line)
          if (highlightedLines.has(line)) this.addClassToHast(hast, 'code-block-highlighted-line')
        },
      }
      this.highlighted = highlighter.codeToHtml(code, {
        lang: language,
        theme: this.theme,
        transformers: [lineHighlightTransformer],
      })
      this.error = ''
    } catch {
      if (token !== this.renderToken) return
      this.highlighted = ''
      this.error = 'Syntax highlighting is unavailable.'
    }
  }

  private get theme(): CodeTheme {
    const colorScheme = document.documentElement.style.colorScheme
    if (colorScheme === 'dark') return 'github-dark'
    return 'github-light'
  }

  private get copyButton() {
    const label = this.copied ? 'Code copied' : 'Copy code'
    return html`<button type="button" class="code-block-copy" aria-label=${label} @click=${this.copyCode}>
      ${lucideIcon(this.copied ? Check : Copy, { size: 14, strokeWidth: 2 })}
      <span>${this.copied ? 'Copied' : 'Copy'}</span>
    </button>`
  }

  private get displayCode(): string {
    return this.preparedKey === this.codeKey ? this.preparedCode : this.sourceCode
  }

  private get sourceCode(): string {
    return this.code
  }

  private get codeKey(): string {
    return `${this.language.trim().toLowerCase()}\u0000${this.format ? 'format' : 'raw'}\u0000${this.sourceCode}`
  }

  private async prepareCode(code: string, language: SupportedLanguage): Promise<string> {
    if (!this.format || language !== 'sql') return code
    try {
      const { format: formatSQL } = await import('sql-formatter')
      return formatSQL(code, {
        language: 'duckdb',
        keywordCase: 'upper',
      }).trim()
    } catch {
      return code
    }
  }

  private setPreparedCode(code: string): void {
    this.preparedKey = this.codeKey
    this.preparedCode = code
  }

  private applyFocusedLines(): void {
    this.querySelectorAll<HTMLElement>('.line[data-code-line]').forEach((line) => {
      const lineNumber = Number(line.dataset.codeLine)
      line.classList.toggle('code-block-focused-line', this.focusedLineNumbers.has(lineNumber))
    })
  }

  private copyCode = async (event: Event): Promise<void> => {
    event.stopPropagation()
    try {
      await writeClipboard(this.displayCode)
      this.copied = true
      window.clearTimeout(this.copiedTimeout)
      this.copiedTimeout = window.setTimeout(() => {
        this.copied = false
      }, 1600)
    } catch {
      this.copied = false
    }
  }
}

async function writeClipboard(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value)
    return
  }

  const textarea = document.createElement('textarea')
  textarea.value = value
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.append(textarea)
  textarea.select()
  const copied = document.execCommand('copy')
  textarea.remove()
  if (!copied) throw new Error('clipboard write failed')
}

function supportedLanguage(language: string): SupportedLanguage | '' {
  const normalized = language.trim().toLowerCase()
  if (normalized === 'bash' || normalized === 'sh' || normalized === 'shell') return 'shellscript'
  if (normalized === 'yml') return 'yaml'
  if (normalized === 'json' || normalized === 'shellscript' || normalized === 'sql' || normalized === 'toon' || normalized === 'yaml') return normalized
  return ''
}

function languageLabel(language: string): string {
  const normalized = language.trim().toLowerCase()
  if (normalized === 'bash' || normalized === 'sh' || normalized === 'shell' || normalized === 'shellscript') return 'Shell'
  if (normalized === 'yml' || normalized === 'yaml') return 'YAML'
  if (normalized === 'json') return 'JSON'
  if (normalized === 'sql') return 'SQL'
  if (normalized === 'toon') return 'TOON'
  if (normalized === 'text' || normalized === 'txt' || normalized === '') return 'Text'
  return normalized.toUpperCase()
}

const codeBlockStyles = `
  lv-code-block {
    display: block;
    min-width: 0;
    max-width: 100%;
  }

  lv-code-block .code-block-shell {
    position: relative;
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
    border: var(--lv-border-muted);
    border-radius: var(--borderRadius-medium, 6px);
    background: var(--lv-bg-panel-muted);
  }

  lv-code-block .shiki,
  lv-code-block .code-block-fallback {
    box-sizing: border-box;
    max-width: 100%;
    max-height: min(44rem, 68vh);
    margin: 0;
    overflow: auto;
    border: 0;
    border-radius: 0;
    padding: var(--base-size-16);
    font: var(--lv-type-code-block);
    tab-size: 2;
  }

  lv-code-block[compact] .shiki,
  lv-code-block[compact] .code-block-fallback {
    max-height: var(--lv-chat-tool-max-height, 18rem);
    padding: var(--lv-chat-pre-padding-block, var(--base-size-8)) var(--lv-chat-pre-padding-inline, var(--base-size-12));
    font: var(--lv-type-caption);
    line-height: var(--base-text-lineHeight-snug);
    white-space: pre;
  }

  lv-code-block[dense] .shiki,
  lv-code-block[dense] .code-block-fallback {
    max-height: min(22rem, 52vh);
    padding: var(--base-size-12);
    font: var(--lv-type-caption);
    line-height: var(--base-text-lineHeight-normal);
  }

  lv-code-block[copy]:not([toolbar]) .shiki,
  lv-code-block[copy]:not([toolbar]) .code-block-fallback {
    padding-top: calc(var(--base-size-16) + var(--control-medium-size, 32px));
  }

  lv-code-block[copy][compact] .shiki,
  lv-code-block[copy][compact] .code-block-fallback,
  lv-code-block[copy][dense] .shiki,
  lv-code-block[copy][dense] .code-block-fallback {
    padding-top: calc(var(--base-size-12) + var(--control-medium-size, 32px));
  }

  lv-code-block .code-block-copy {
    position: absolute;
    top: var(--base-size-8);
    right: var(--base-size-8);
    z-index: 1;
    display: inline-flex;
    min-height: var(--control-medium-size, 32px);
    align-items: center;
    gap: var(--base-size-6, 6px);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-muted);
    cursor: pointer;
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
    padding: 0 var(--base-size-8);
  }

  lv-code-block .code-block-copy:hover,
  lv-code-block .code-block-copy:focus-visible {
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
    outline: 0;
  }

  lv-code-block .code-block-toolbar {
    display: flex;
    min-height: var(--control-medium-size, 32px);
    align-items: center;
    justify-content: space-between;
    border-bottom: var(--lv-border-muted);
    padding-left: var(--base-size-16);
  }

  lv-code-block .code-block-language {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
  }

  lv-code-block[toolbar] .code-block-copy {
    position: static;
    min-height: var(--control-medium-size, 32px);
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
  }

  lv-code-block .shiki code,
  lv-code-block .code-block-fallback code {
    font-family: inherit;
  }

  lv-code-block .shiki code {
    display: block;
    min-width: max-content;
  }

  lv-code-block .shiki .line {
    box-sizing: border-box;
    display: inline-block;
    min-width: 100%;
  }

  lv-code-block .shiki .code-block-highlighted-line {
    position: relative;
    background: var(--lv-bg-accent-muted);
  }

  lv-code-block .shiki .code-block-highlighted-line::before {
    position: absolute;
    inset-block: 0;
    inset-inline-start: 0;
    width: var(--base-size-4);
    background: var(--lv-line-accent);
    content: '';
  }

  lv-code-block[data-line-focus] .shiki .code-block-highlighted-line {
    background: transparent;
  }

  lv-code-block[data-line-focus] .shiki .code-block-highlighted-line::before {
    background: transparent;
  }

  lv-code-block[data-line-focus] .shiki .code-block-focused-line {
    background: var(--lv-bg-accent-muted);
  }

  lv-code-block[data-line-focus] .shiki .code-block-focused-line::before {
    background: var(--lv-line-accent);
  }

  lv-code-block .code-block-fallback {
    color: var(--lv-fg-default);
    background: var(--lv-bg-panel-muted);
    white-space: pre;
  }

  lv-code-block .code-block-error {
    margin: 0;
    border-top: var(--lv-border-muted);
    padding: var(--base-size-8) var(--base-size-16);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }
`

if (!customElements.get('lv-code-block')) {
  customElements.define('lv-code-block', CodeBlock)
}
