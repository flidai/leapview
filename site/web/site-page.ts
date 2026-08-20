import { LitElement, css, html } from 'lit'
import { Blocks, Bot, Boxes, ChartNoAxesCombined, Check, CodeXml, Copy, Database, GitBranch, Radio, Server, SquareMousePointer, SquareTerminal, type IconNode } from 'lucide'
import { DatastarLit } from '../../web/components/shared/datastar-lit'
import { lucideIcon } from '../../web/components/shared/lucide-icons'
import '../../web/components/shared/brand-mark'
import '../../web/components/shared/code-block'
import { kpiLayoutFeatures } from '../../web/components/dashboard/visualization/kpi-layout'
import {
  layoutRequirements,
} from '../../web/components/dashboard/visualization/layout'
import { visualExampleHighlightLines } from './visual-example-highlights'
import type { VisualPayload } from './site-types'
import './site-shell'
import './site-docs-navigation'
import './site-article'
import './site-responsive-reference'
import './site-visual-showcase'



class SiteMarkdownCopy extends LitElement {
  static properties = {
    markdown: { type: String },
  }

  declare markdown: string

  private copied = false
  private resetTimer?: number

  static styles = css`
    :host {
      display: inline-block;
    }

    button {
      display: inline-flex;
      box-sizing: border-box;
      height: 33px;
      align-items: center;
      flex-shrink: 0;
      gap: var(--base-size-6);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: inherit;
      font-size: var(--text-body-size-small);
      line-height: 1.3;
      padding: 0 var(--base-size-12);
      transition: border-color var(--motion-duration-medium);
    }

    button:hover,
    button:focus-visible {
      border-color: var(--lv-button-border-hover);
    }

    button:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    @media (prefers-reduced-motion: reduce) {
      button {
        transition: none;
      }
    }
  `

  disconnectedCallback(): void {
    window.clearTimeout(this.resetTimer)
    super.disconnectedCallback()
  }

  render() {
    const label = this.copied ? 'Markdown copied' : 'Copy Markdown'
    return html`<button type="button" aria-label=${label} @click=${this.copyMarkdown}>
      ${lucideIcon(this.copied ? Check : Copy, { size: 16, strokeWidth: 2 })}
      <span>${this.copied ? 'Copied' : 'Copy Markdown'}</span>
    </button>`
  }

  private copyMarkdown = async (): Promise<void> => {
    if (!this.markdown) return

    try {
      await writeClipboard(this.markdown)
    } catch {
      return
    }

    this.copied = true
    this.requestUpdate()
    window.clearTimeout(this.resetTimer)
    this.resetTimer = window.setTimeout(() => {
      this.copied = false
      this.requestUpdate()
    }, 2_000)
  }
}

if (!customElements.get('lv-site-markdown-copy')) {
  customElements.define('lv-site-markdown-copy', SiteMarkdownCopy)
}

type ResolvedThemeMode = 'light' | 'dark'

let mermaidModule: Promise<(typeof import('mermaid'))['default']> | undefined
let mermaidRenderSequence = 0
let mermaidRenderQueue: Promise<void> = Promise.resolve()

function loadMermaid(): Promise<(typeof import('mermaid'))['default']> {
  mermaidModule ??= import('mermaid').then((module) => module.default)
  return mermaidModule
}

function resolvedThemeMode(): ResolvedThemeMode {
  const colorScheme = document.documentElement.style.colorScheme
  if (colorScheme === 'dark' || colorScheme === 'light') return colorScheme
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function mermaidAccessibleTitle(source: string): string {
  const accessibilityTitle = source.match(/^\s*accTitle:\s*(.+?)\s*$/m)?.[1]
  if (accessibilityTitle) return accessibilityTitle

  const frontmatter = source.match(/^---\s*\n([\s\S]*?)\n---\s*\n/)
  const frontmatterTitle = frontmatter?.[1].match(/^title:\s*["']?(.+?)["']?\s*$/m)?.[1]
  return frontmatterTitle || 'Documentation diagram'
}

class SiteMermaid extends LitElement {
  static properties = {
    source: { type: String },
  }

  declare source: string
  private renderGeneration = 0
  private readonly handleThemeApplied = (event: Event): void => {
    const detail = (event as CustomEvent<{ resolvedMode?: string }>).detail
    const theme = detail?.resolvedMode === 'dark' ? 'dark' : 'light'
    if (this.dataset.renderedTheme !== theme) void this.draw(theme)
  }

  static styles = css`
    :host {
      display: block;
      width: 100%;
      min-width: 0;
      color: var(--lv-fg-default);
    }

    figure {
      display: grid;
      min-width: 0;
      margin: 0;
      gap: var(--base-size-12);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-20);
    }

    .canvas {
      display: grid;
      min-width: 0;
      min-height: var(--base-size-64);
      place-items: center;
      overflow: auto hidden;
    }

    .canvas svg {
      display: block;
      width: auto;
      max-width: 100%;
      height: auto;
      max-height: min(38rem, 70svh);
    }

    figcaption,
    .error {
      margin: 0;
      color: var(--lv-fg-muted);
      font-size: var(--text-body-size-small);
      line-height: var(--base-text-lineHeight-relaxed);
    }

    figcaption {
      text-align: center;
    }

    .error {
      color: var(--lv-fg-danger);
    }

    [hidden] {
      display: none;
    }

    @media (width < 48rem) {
      figure {
        padding: var(--base-size-12);
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('leapview-theme-applied', this.handleThemeApplied)
  }

  disconnectedCallback(): void {
    document.removeEventListener('leapview-theme-applied', this.handleThemeApplied)
    this.renderGeneration += 1
    super.disconnectedCallback()
  }

  protected updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('source')) {
      this.setAttribute('aria-label', mermaidAccessibleTitle(this.source ?? ''))
      void this.draw(resolvedThemeMode())
    }
  }

  render() {
    const title = mermaidAccessibleTitle(this.source ?? '')
    return html`<figure>
      <div class="canvas" aria-busy="true"></div>
      <p class="error" role="alert" hidden></p>
      <figcaption>${title}</figcaption>
    </figure>`
  }

  private async draw(theme: ResolvedThemeMode): Promise<void> {
    const generation = ++this.renderGeneration
    await this.updateComplete
    const source = this.source?.trim()
    const canvas = this.renderRoot.querySelector<HTMLElement>('.canvas')
    const error = this.renderRoot.querySelector<HTMLElement>('.error')
    if (!source || !canvas || !error) return

    canvas.setAttribute('aria-busy', 'true')
    error.hidden = true
    const task = async (): Promise<void> => {
      try {
        const mermaid = await loadMermaid()
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          suppressErrorRendering: true,
          theme: 'base',
          fontFamily: cssToken(this, '--fontStack-system'),
          themeVariables: mermaidThemeVariables(this),
          flowchart: { htmlLabels: false, useMaxWidth: true },
        })
        const id = `leapview-docs-diagram-${++mermaidRenderSequence}`
        const result = await mermaid.render(id, source)
        if (generation !== this.renderGeneration || !this.isConnected) return

        canvas.innerHTML = result.svg
        const svg = canvas.querySelector('svg')
        if (svg) {
          svg.setAttribute('role', 'img')
          svg.style.maxWidth = '100%'
          svg.style.height = 'auto'
        }
        result.bindFunctions?.(canvas)
        canvas.setAttribute('aria-busy', 'false')
        this.dataset.renderedTheme = theme
      } catch (cause) {
        if (generation !== this.renderGeneration || !this.isConnected) return
        canvas.replaceChildren()
        canvas.setAttribute('aria-busy', 'false')
        error.textContent = `Diagram could not be rendered: ${cause instanceof Error ? cause.message : String(cause)}`
        error.hidden = false
      }
    }

    const queued = mermaidRenderQueue.then(task, task)
    mermaidRenderQueue = queued.then(
      () => undefined,
      () => undefined,
    )
    await queued
  }
}

function cssToken(element: Element, name: string): string {
  const value = getComputedStyle(element).getPropertyValue(name).trim()
  if (!value) throw new Error(`Required diagram token ${name} is unavailable`)
  return value
}

function mermaidThemeVariables(element: Element): Record<string, string> {
  const background = cssToken(element, '--lv-bg-panel')
  const foreground = cssToken(element, '--lv-fg-default')
  const muted = cssToken(element, '--lv-fg-muted')
  const accent = cssToken(element, '--lv-fg-accent')
  const accentBackground = cssToken(element, '--lv-bg-accent-muted')
  const control = cssToken(element, '--lv-bg-control')
  const border = cssToken(element, '--lv-line-muted')

  return {
    background,
    primaryColor: accentBackground,
    primaryTextColor: foreground,
    primaryBorderColor: accent,
    secondaryColor: control,
    secondaryTextColor: foreground,
    secondaryBorderColor: border,
    tertiaryColor: background,
    tertiaryTextColor: foreground,
    tertiaryBorderColor: border,
    lineColor: muted,
    textColor: foreground,
    mainBkg: accentBackground,
    nodeBorder: accent,
    clusterBkg: control,
    clusterBorder: border,
    edgeLabelBackground: background,
    noteBkgColor: control,
    noteBorderColor: border,
    noteTextColor: foreground,
  }
}

if (!customElements.get('lv-site-mermaid')) {
  customElements.define('lv-site-mermaid', SiteMermaid)
}

function enhanceDocsCodeBlocks(): void {
  document.querySelectorAll<HTMLElement>('.site-docs-article pre').forEach((pre) => {
    if (pre.closest('lv-code-block, lv-site-mermaid')) return

    const code = pre.querySelector('code')
    const languageClass = Array.from(code?.classList ?? []).find((name) => name.startsWith('language-'))
    const language = languageClass?.slice('language-'.length).toLowerCase() ?? ''
    if (language === 'mermaid') {
      const diagram = document.createElement('lv-site-mermaid') as SiteMermaid
      diagram.source = code?.textContent ?? pre.textContent ?? ''
      pre.replaceWith(diagram)
      return
    }
    const block = document.createElement('lv-code-block') as HTMLElement & {
      clearFocusedLines(): void
      code: string
      copy: boolean
      focusLines(lines: readonly number[]): void
      highlightedLines: number[]
      toolbar: boolean
    }

    block.setAttribute('language', language || 'text')
    block.code = code?.textContent ?? pre.textContent ?? ''
    const keyFields = pre.previousElementSibling
    const visualExample = keyFields?.matches('.site-visual-key-fields') ? keyFields.previousElementSibling : null
    if (language === 'yaml' && keyFields instanceof HTMLElement && visualExample?.matches('lv-site-visual-example')) {
      const fields = JSON.parse(keyFields.dataset.keyFields ?? '[]') as string[]
      const exampleID = visualExample.getAttribute('example-id') ?? ''
      block.dataset.visualExample = exampleID
      block.dataset.highlightedFields = fields.join(',')
      block.highlightedLines = visualExampleHighlightLines(block.code, fields)
      block.id = `visual-example-${exampleID}-yaml`
      enhanceVisualKeyFieldControls(keyFields, block)
    }
    block.copy = true
    block.toolbar = true
    pre.replaceWith(block)
  })
}

function enhanceVisualKeyFieldControls(
  container: HTMLElement,
  block: HTMLElement & { clearFocusedLines(): void; code: string; focusLines(lines: readonly number[]): void },
): void {
  let focusedField = ''
  let hoveredField = ''
  const lines = new Map<string, number[]>()
  const apply = (): void => {
    const field = focusedField || hoveredField
    if (!field) {
      block.clearFocusedLines()
      return
    }
    block.focusLines(lines.get(field) ?? [])
  }

  container.querySelectorAll<HTMLButtonElement>('[data-visual-key-field]').forEach((control) => {
    const field = control.dataset.visualKeyField ?? ''
    lines.set(field, visualExampleHighlightLines(block.code, [field]))
    control.setAttribute('aria-controls', block.id)
    control.addEventListener('focus', () => {
      focusedField = field
      apply()
    })
    control.addEventListener('blur', () => {
      focusedField = ''
      apply()
    })
    control.addEventListener('pointerenter', () => {
      hoveredField = field
      apply()
    })
    control.addEventListener('pointerleave', () => {
      hoveredField = ''
      apply()
    })
  })
}

type CalloutKind = 'note' | 'tip' | 'experimental' | 'warning' | 'danger'

const calloutKinds: Record<string, { kind: CalloutKind; label: string }> = {
  CAUTION: { kind: 'danger', label: 'Caution' },
  DANGER: { kind: 'danger', label: 'Danger' },
  EXPERIMENTAL: { kind: 'experimental', label: 'Experimental' },
  IMPORTANT: { kind: 'note', label: 'Important' },
  NOTE: { kind: 'note', label: 'Note' },
  TIP: { kind: 'tip', label: 'Tip' },
  WARNING: { kind: 'warning', label: 'Warning' },
}

function enhanceDocsCallouts(): void {
  document.querySelectorAll<HTMLElement>('.site-docs-article blockquote').forEach((blockquote) => {
    if (blockquote.classList.contains('site-docs-callout')) return
    const paragraph = blockquote.querySelector<HTMLElement>(':scope > p')
    if (!paragraph) return

    const walker = document.createTreeWalker(paragraph, NodeFilter.SHOW_TEXT)
    const markerNode = walker.nextNode() as Text | null
    const marker = markerNode?.data.match(/^\s*\[!(NOTE|TIP|EXPERIMENTAL|WARNING|CAUTION|DANGER|IMPORTANT)\]\s*/i)
    if (!markerNode || !marker) return

    const definition = calloutKinds[marker[1].toUpperCase()]
    markerNode.data = markerNode.data.slice(marker[0].length)
    blockquote.classList.add('site-docs-callout', `site-docs-callout-${definition.kind}`)
    blockquote.dataset.callout = definition.kind

    const label = document.createElement('p')
    label.className = 'site-docs-callout-label'
    const strong = document.createElement('strong')
    strong.textContent = definition.label
    label.append(strong)
    blockquote.prepend(label)
  })
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

enhanceDocsCodeBlocks()
enhanceDocsCallouts()

const featureIcons: Record<string, IconNode> = {
  agent: Bot,
  blocks: Blocks,
  boxes: Boxes,
  chart: ChartNoAxesCombined,
  'code-xml': CodeXml,
  database: Database,
  'git-branch': GitBranch,
  radio: Radio,
  server: Server,
  'square-mouse-pointer': SquareMousePointer,
  terminal: SquareTerminal,
}

class SiteFeatureIcon extends LitElement {
  static properties = {
    name: { type: String },
  }

  declare name: string

  static styles = css`
    :host {
      display: grid;
      width: var(--control-large-size);
      height: var(--control-large-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-control);
      color: var(--lv-fg-accent);
    }

    :host([plain]) {
      width: var(--base-size-28);
      height: var(--base-size-28);
      border: 0;
      border-radius: 0;
      background: transparent;
      color: var(--lv-fg-muted);
    }
  `

  render() {
    return lucideIcon(featureIcons[this.name] ?? Blocks, {
      size: 22,
      strokeWidth: 1.8,
    })
  }
}

if (!customElements.get('lv-site-feature-icon')) {
  customElements.define('lv-site-feature-icon', SiteFeatureIcon)
}



class SiteVisualExample extends DatastarLit(LitElement) {
  static properties = {
    exampleId: { type: String, attribute: 'example-id' },
  }

  declare exampleId: string

  static styles = css`
    :host {
      display: block;
      min-height: 28rem;
      margin-block: var(--base-size-24);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
      box-shadow: var(--shadow-resting-small);
      overflow: hidden;
    }

    lv-visualization-host {
      display: block;
      height: 28rem;
    }

    :host([type='kpi']) {
      min-height: 0;
      padding: var(--base-size-16);
      overflow: auto;
    }

    .layout-gallery {
      display: flex;
      flex-wrap: wrap;
      align-items: start;
      gap: var(--base-size-20);
    }

    .layout-preview {
      display: grid;
      gap: var(--base-size-8);
      margin: 0;
    }

    .layout-frame {
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
    }

    .layout-frame lv-visualization-host {
      width: 100%;
      height: 100%;
    }

    figcaption {
      color: var(--lv-fg-muted);
      font-size: var(--text-caption-size);
      line-height: var(--base-text-lineHeight-tight);
    }

  `

  render() {
    const visuals = this.signal<VisualPayload[]>('visuals', [])
    const visual = visuals.find((candidate) => candidate.visualID === this.exampleId)
    const visualType = visual?.spec.kind ?? ''
    if (this.getAttribute('type') !== visualType) {
      queueMicrotask(() => {
        if (visualType) this.setAttribute('type', visualType)
        else this.removeAttribute('type')
      })
    }
    if (!visual) return null
    if (visual.spec.kind !== 'kpi') {
      return html`<lv-visualization-host .envelope=${visual}></lv-visualization-host>`
    }
    const requirements = layoutRequirements('kpi', kpiLayoutFeatures(visual))
    return html`<div class="layout-gallery" aria-label="Automatic responsive layouts">
      ${requirements.map((requirement) => html`
        <figure class="layout-preview" data-layout-preview=${requirement.layout}>
          <div
            class="layout-frame"
            style=${`width: ${requirement.minimum.width}px; height: ${requirement.minimum.height}px`}
          >
            <lv-visualization-host .envelope=${visual}></lv-visualization-host>
          </div>
          <figcaption>
            ${requirement.minimum.width}×${requirement.minimum.height} · Automatically selected: ${requirement.layout}
          </figcaption>
        </figure>
      `)}
    </div>`
  }
}

if (!customElements.get('lv-site-visual-example')) {
  customElements.define('lv-site-visual-example', SiteVisualExample)
}



async function loadRouteComponents(): Promise<void> {
  const imports: Promise<unknown>[] = []
  if (document.querySelector('lv-site-visual-showcase, lv-site-visual-example, lv-site-responsive-widget-reference')) {
    imports.push(import('../../web/components/dashboard/visualization/host'))
  }
  if (document.querySelector('lv-site-responsive-widget-reference')) {
    imports.push(import('../../web/components/dashboard/filters/filter-control'))
  }
  if (document.querySelector('lv-site-flow-background')) {
    imports.push(import('./site-flow-background'))
  }
  await Promise.all(imports)
}

void loadRouteComponents()
