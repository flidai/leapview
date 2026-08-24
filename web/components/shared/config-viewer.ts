import { LitElement, css, html, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronsDown, ChevronsUp } from 'lucide'
import { parse as parseYAML } from 'yaml'
import { lucideIcon } from './lucide-icons'
import './code-block'

type ConfigValue = null | boolean | number | string | ConfigValue[] | { [key: string]: ConfigValue }
type ConfigObject = { [key: string]: ConfigValue }
type ViewMode = 'outline' | 'raw'

class ConfigViewer extends LitElement {
  @property({ type: String }) configuration = ''
  @property({ type: String }) language = 'yaml'
  @state() private documentValue: ConfigValue | null = null
  @state() private parseError = ''
  @state() private query = ''
  @state() private expandedPaths = new Set<string>()
  @state() private viewMode: ViewMode = 'outline'
  private parsedConfiguration = ''

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
    }

    .viewer {
      min-width: 0;
      border: 0;
      background: transparent;
      padding: 0;
    }

    .toolbar {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
      justify-content: space-between;
      padding-bottom: var(--base-size-10);
    }

    .outline-tools {
      display: flex;
      min-width: 0;
      flex: 1 1 auto;
      align-items: center;
      gap: var(--base-size-8);
    }

    .toolbar-spacer {
      min-width: 0;
      flex: 1 1 auto;
    }

    .search {
      min-width: 0;
      width: min(100%, 52rem);
      flex: 0 1 52rem;
      height: var(--control-medium-size, 32px);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      outline: 0;
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8);
      font: var(--lv-type-body-compact);
    }

    .search:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset, 2px);
    }

    .tools {
      display: flex;
      flex: 0 0 auto;
      align-items: center;
      gap: var(--base-size-4);
      margin-left: auto;
    }

    .tool {
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: var(--base-size-6) var(--base-size-8);
      white-space: nowrap;
      font: var(--lv-type-caption);
    }

    .tool:hover,
    .tool:focus-visible {
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .tool.icon-tool {
      display: inline-grid;
      width: var(--control-small-size, 28px);
      height: var(--control-small-size, 28px);
      padding: 0;
      place-items: center;
    }

    .mode-toggle {
      display: inline-flex;
      flex: 0 0 auto;
      align-items: center;
      gap: 1px;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: 2px;
    }

    .mode {
      border: 0;
      border-radius: var(--lv-radius-small, 4px);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: var(--base-size-4) var(--base-size-8);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .mode[data-active='true'] {
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-medium);
    }

    .mode:hover,
    .mode:focus-visible {
      color: var(--lv-fg-default);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset, 1px);
    }

    .tree {
      display: grid;
      gap: 1px;
      min-width: 0;
      padding-top: var(--base-size-8);
      font: var(--lv-type-code-inline);
    }

    .node { min-width: 0; }

    .row {
      display: flex;
      width: 100%;
      min-width: 0;
      align-items: baseline;
      gap: var(--base-size-4);
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      padding: var(--base-size-4) var(--base-size-6);
      text-align: left;
      white-space: nowrap;
    }

    button.row { cursor: pointer; }

    .row:hover,
    button.row:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .sql-row {
      align-items: flex-start;
      white-space: normal;
    }

    .sql-row:hover,
    .sql-row:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .sql-row lv-code-block {
      min-width: 0;
      width: min(100%, 52rem);
      max-width: 100%;
      flex: 0 1 52rem;
    }

    .group {
      min-width: 0;
      margin-left: var(--base-size-12);
    }

    .chevron {
      display: inline-grid;
      width: var(--base-size-12);
      flex: 0 0 var(--base-size-12);
      color: var(--lv-fg-muted);
      place-items: center;
    }

    .key { color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }
    .count { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .separator { color: var(--lv-fg-muted); }
    .string { color: var(--fgColor-attention, var(--lv-fg-warning, var(--lv-fg-default))); overflow: hidden; text-overflow: ellipsis; }
    .number { color: var(--lv-fg-accent, var(--lv-accent)); }
    .boolean { color: var(--lv-fg-accent, var(--lv-accent)); }
    .null { color: var(--lv-fg-muted, var(--lv-fg-default)); font-style: italic; }

    .message {
      color: var(--lv-fg-muted);
      padding: var(--base-size-8) 0;
      font: var(--lv-type-body-compact);
    }

    .raw-yaml {
      display: block;
      padding-top: var(--base-size-8);
    }

    .error { color: var(--lv-fg-danger); }

    @media (max-width: 560px) {
      .toolbar { flex-wrap: wrap; }
      .outline-tools { order: 2; flex-basis: 100%; }
      .mode-toggle { order: 1; margin-left: auto; }
      .search { width: 100%; max-width: none; flex-basis: 100%; }
      .tools { margin-left: auto; }
    }
  `

  updated(changed: Map<string, unknown>): void {
    if (!changed.has('configuration') || this.configuration === this.parsedConfiguration) return
    this.parsedConfiguration = this.configuration
    try {
      const value = parseYAML(this.configuration) as ConfigValue
      this.documentValue = value
      this.parseError = ''
      this.expandedPaths = defaultExpandedPaths(value)
    } catch (error) {
      this.documentValue = null
      this.expandedPaths = new Set()
      this.parseError = error instanceof Error ? error.message : 'Unable to parse configuration.'
    }
  }

  render() {
    return html`
      <div class="viewer" aria-label=${this.viewMode === 'raw' ? `Source ${languageLabel(this.language)} configuration` : 'Configuration outline'}>
        <div class="toolbar">
          ${this.viewMode === 'outline' ? html`
            <div class="outline-tools">
              <input class="search" type="search" placeholder="Filter keys or values…" aria-label="Filter configuration" .value=${this.query} @input=${this.handleQuery} />
              <div class="tools">
                <button class="tool icon-tool" type="button" aria-label="Expand all" title="Expand all" @click=${this.expandAll}>${lucideIcon(ChevronsDown, { size: 16 })}</button>
                <button class="tool icon-tool" type="button" aria-label="Collapse all" title="Collapse all" @click=${this.collapseAll}>${lucideIcon(ChevronsUp, { size: 16 })}</button>
              </div>
            </div>
          ` : html`<span class="toolbar-spacer" aria-hidden="true"></span>`}
          <div class="mode-toggle" role="group" aria-label="Configuration view">
            <button class="mode" data-mode="outline" data-active=${this.viewMode === 'outline'} type="button" aria-pressed=${this.viewMode === 'outline'} @click=${() => this.setViewMode('outline')}>Outline</button>
            <button class="mode" data-mode="raw" data-active=${this.viewMode === 'raw'} type="button" aria-pressed=${this.viewMode === 'raw'} @click=${() => this.setViewMode('raw')}>Source</button>
          </div>
        </div>
        ${this.viewMode === 'raw'
          ? html`<lv-code-block class="raw-yaml" toolbar copy .language=${this.language} .code=${this.configuration}></lv-code-block>`
          : this.renderOutline()}
      </div>
    `
  }

  private renderOutline(): TemplateResult {
    return this.parseError
      ? html`<div class="message error" role="alert">${this.parseError}</div>`
      : this.documentValue === null
        ? html`<div class="message">Loading configuration…</div>`
        : html`<div class="tree" role="tree">${this.renderRootValue(this.documentValue)}</div>`
  }

  private renderRootValue(value: ConfigValue): TemplateResult | TemplateResult[] {
    if (!isBranch(value)) return html`<div class="row">${valueMarkup(value)}</div>`
    return entries(value).flatMap(([key, child]) => this.renderNode(key, child, key, 0))
  }

  private renderNode(key: string, value: ConfigValue, path: string, depth: number): TemplateResult[] {
    if (!matches(value, path, this.query)) return []
    const branch = isBranch(value)
    const open = this.expandedPaths.has(path) || Boolean(this.query && branch)
    if (!branch && typeof value === 'string' && path.endsWith('.sql')) {
      return [html`
        <div class="node">
          <div class="row sql-row" style=${`padding-left:${depth * 15 + 6}px`} role="treeitem">
            <span class="key">${displayKey(key, path)}</span><span class="separator">:</span>
            <lv-code-block inline compact dense format copy language="sql" .code=${value}></lv-code-block>
          </div>
        </div>
      `]
    }
    const row = branch
      ? html`
        <button class="row" type="button" style=${`padding-left:${depth * 15 + 6}px`} aria-expanded=${open} @click=${() => this.toggle(path)}>
          <span class="chevron">${open ? '⌄' : '›'}</span>
          <span class="key" data-kind=${Array.isArray(value) ? 'array' : 'object'}>${displayKey(key, path)}</span>
          <span class="count">${Array.isArray(value) ? `[${entries(value).length}]` : `{${entries(value).length}}`}</span>
        </button>
      `
      : html`
        <div class="row" style=${`padding-left:${depth * 15 + 6}px`}>
          <span class="key">${displayKey(key, path)}</span><span class="separator">:</span>${valueMarkup(value)}
        </div>
      `
    const childRows = branch && open
      ? html`<div class="group" role="group">${entries(value).flatMap(([childKey, child]) => this.renderNode(childKey, child, joinPath(path, childKey), depth + 1))}</div>`
      : nothing
    return [html`<div class="node">${row}${childRows}</div>`]
  }

  private handleQuery = (event: Event): void => {
    this.query = (event.currentTarget as HTMLInputElement).value.trim().toLowerCase()
  }

  private toggle(path: string): void {
    const next = new Set(this.expandedPaths)
    if (next.has(path)) next.delete(path)
    else next.add(path)
    this.expandedPaths = next
  }

  private setViewMode(mode: ViewMode): void {
    this.viewMode = mode
  }

  private expandAll = (): void => {
    this.expandedPaths = allBranchPaths(this.documentValue)
  }

  private collapseAll = (): void => {
    this.expandedPaths = new Set()
  }
}

function isBranch(value: ConfigValue): value is ConfigValue[] | ConfigObject {
  return value !== null && typeof value === 'object'
}

function entries(value: ConfigValue): Array<[string, ConfigValue]> {
  if (Array.isArray(value)) return value.map((item, index) => [String(index), item])
  if (value !== null && typeof value === 'object') return Object.entries(value)
  return []
}

function joinPath(path: string, key: string): string {
  return path ? `${path}.${key}` : key
}

function defaultExpandedPaths(value: ConfigValue): Set<string> {
  return allBranchPaths(value)
}

function allBranchPaths(value: ConfigValue, path = ''): Set<string> {
  const paths = new Set<string>()
  if (!isBranch(value)) return paths
  for (const [key, child] of entries(value)) {
    if (!isBranch(child)) continue
    const childPath = joinPath(path, key)
    paths.add(childPath)
    for (const nested of allBranchPaths(child, childPath)) paths.add(nested)
  }
  return paths
}

function matches(value: ConfigValue, path: string, query: string): boolean {
  if (!query) return true
  const text = `${path} ${isBranch(value) ? '' : String(value)}`.toLowerCase()
  if (text.includes(query)) return true
  return isBranch(value) && entries(value).some(([key, child]) => matches(child, joinPath(path, key), query))
}

function displayKey(key: string, path: string): string {
  if (key === 'definition' || path.endsWith('.definition')) return 'Transform'
  return key
}

function languageLabel(language: string): string {
  return language.trim().toLowerCase() === 'json' ? 'JSON' : 'YAML'
}

function valueMarkup(value: ConfigValue): TemplateResult {
  if (value === null) return html`<span class="null">null</span>`
  if (typeof value === 'boolean') return html`<span class="boolean">${String(value)}</span>`
  if (typeof value === 'number') return html`<span class="number">${value}</span>`
  if (typeof value === 'string') {
    return html`<span class="string" title=${value}>${value.replaceAll('\n', ' ↵ ')}</span>`
  }
  return html`<span>${String(value)}</span>`
}

customElements.define('lv-config-viewer', ConfigViewer)
