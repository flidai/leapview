import { LitElement, css, html, nothing } from 'lit'
import { keyed } from 'lit/directives/keyed.js'
import { property, state } from 'lit/decorators.js'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import type { ExampleSnapshot, StatefulExample } from './example-state'
import { fixtureCoverage, type FixtureControl } from './fixture-coverage'

export function snapshotURL(snapshot: ExampleSnapshot): URL {
  const url = new URL(location.href)
  url.search = ''
  url.searchParams.set('state', JSON.stringify(snapshot))
  url.hash = snapshot.route
  return url
}

export async function copyText(value: string): Promise<boolean> {
  try { await navigator.clipboard.writeText(value); return true } catch { return false }
}

class PlaygroundReviewTools extends LitElement {
  @property({ attribute: false }) getSnapshot?: () => ExampleSnapshot
  @property({ attribute: false }) getCode?: () => string
  @property({ attribute: false }) getExample?: () => StatefulExample | null
  @property() route = ''
  @property({ attribute: false }) exampleReady = false
  @state() private code = ''
  @state() private message = ''
  @state() private pinned = ''
  @state() private pinnedLabel = ''
  @state() private pinRevision = 0
  @state() private scanning = false
  @state() private findings: Array<{ id: string; help: string; helpUrl: string; nodes: number; targets: string[] }> = []
  @state() private scanSummary = ''
  @state() private scannedRoute = ''
  @state() private coverage: FixtureControl[] = []

  protected willUpdate(changed: Map<PropertyKey, unknown>) {
    if (changed.has('route')) { this.code = ''; this.message = ''; this.coverage = [] }
  }

  protected updated(changed: Map<PropertyKey, unknown>) {
    if ((changed.has('route') || changed.has('exampleReady')) && this.exampleReady && this.renderRoot.querySelector<HTMLDetailsElement>('.coverage')?.open) void this.refreshCoverage()
  }

  private refreshCoverage = async () => {
    if (!this.exampleReady) return
    const route = this.route
    const example = this.getExample?.()
    if (!example) return
    await example?.updateComplete
    if (this.exampleReady && route === this.route && example === this.getExample?.()) this.coverage = fixtureCoverage(example.shadowRoot)
  }

  static styles = [settingsLayoutStyles, css`
    :host { display: block; margin-top: var(--base-size-16); font: var(--lv-type-body-compact); color: var(--lv-fg-default); }
    * { box-sizing: border-box; }
    details { border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); }
    summary { cursor: pointer; padding: var(--base-size-12) var(--base-size-16); color: var(--lv-fg-muted); }
    summary:focus-visible { outline: var(--borderWidth-thick) solid var(--focus-outlineColor); }
    .body { padding: 0 var(--base-size-16) var(--base-size-16); display: grid; gap: var(--base-size-16); }
    .actions { display: flex; flex-wrap: wrap; gap: var(--base-size-8); }
    .note { color: var(--lv-fg-muted); margin: 0; }
    pre, textarea { display: block; max-height: 18rem; width: 100%; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font: var(--lv-type-mono); padding: var(--base-size-12); background: var(--lv-bg-panel-muted); border: var(--lv-border-muted); color: inherit; }
    pre { margin: 0; }
    a { color: var(--lv-fg-accent); }
    iframe { display: block; width: 100%; height: 480px; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); }
    .reference { display: grid; gap: var(--base-size-8); }
    .checks { display: grid; gap: var(--base-size-8); }
    label { display: flex; gap: var(--base-size-8); align-items: start; }
    input { accent-color: var(--lv-bg-accent); }
    ul { margin: 0; padding-left: var(--base-size-20); }
    dl { margin: 0; display: grid; gap: var(--base-size-12); }
    dt { font-weight: var(--base-text-weight-semibold); }
    dd { margin: var(--base-size-4) 0 0; color: var(--lv-fg-muted); overflow-wrap: anywhere; }
  `]

  private showCode = async () => {
    if (!this.exampleReady) return
    this.code = this.getCode?.() || '// See Usage & events for this component.'
    this.message = await copyText(this.code) ? 'Code copied.' : 'Clipboard unavailable. Select and copy the code below.'
  }

  private pin = () => {
    if (!this.exampleReady) return
    const state = this.getSnapshot?.()
    if (!state) return
    const url = snapshotURL({ ...state, preview: true })
    url.searchParams.set('embedded', '1')
    if (url.href.length > 8000) { this.message = 'This example is too large to pin. Reduce the edited content first.'; return }
    this.pinned = url.href
    this.pinRevision++
    this.pinnedLabel = `${state.route} · ${state.theme} · ${state.width === 'responsive' ? 'responsive width' : state.width + ' px'}`
  }

  private scan = async () => {
    if (!this.exampleReady || this.scanning) return
    this.scanning = true
    this.scanSummary = 'Inspecting the rendered preview…'
    this.findings = []
    const route = this.route
    const example = this.getExample?.()
    try {
      // Existing axe-core dependency; loaded only on an explicit reviewer action.
      const { default: axe } = await import('axe-core')
      if (!this.exampleReady || route !== this.route || example !== this.getExample?.()) { this.scanSummary = ''; return }
      const result = await axe.run({ include: [{ fromShadowDom: ['playground-app', '.viewport'] }] }, {
        runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'] },
      })
      if (!this.exampleReady || route !== this.route || example !== this.getExample?.()) { this.scanSummary = ''; return }
      this.findings = result.violations.map(item => ({ id: item.id, help: item.help, helpUrl: item.helpUrl, nodes: item.nodes.length, targets: item.nodes.map(node => JSON.stringify(node.target)) }))
      this.scannedRoute = route
      this.scanSummary = `${result.violations.length} rule violations; ${result.incomplete.length} rules need manual review. Snapshot of ${route} at ${new Date().toLocaleTimeString()}.`
    } catch (error) {
      this.scanSummary = `Inspection could not complete: ${error instanceof Error ? error.message : String(error)}`
    } finally { this.scanning = false }
  }

  render() {
    const chart = this.route.startsWith('charts/') ? this.route.split('/')[1] : ''
    return html`<details><summary>Code & review</summary><div class="body">
      <div class="actions">
        <button class="settings-button" ?disabled=${!this.exampleReady} @click=${this.showCode}>Copy component code</button>
        <button class="settings-button" ?disabled=${!this.exampleReady} @click=${this.pin}>${this.pinned ? 'Replace comparison' : 'Pin comparison'}</button>
        ${this.pinned ? html`<button class="settings-button" @click=${() => { this.pinned = ''; this.pinnedLabel = '' }}>Clear comparison</button>` : nothing}
        <button class="settings-button" ?disabled=${!this.exampleReady || this.scanning} @click=${this.scan}>${this.scanning ? 'Inspecting…' : 'Check accessibility'}</button>
      </div>
      <p class="note">Compare a fixed fixture with changes above. Accessibility checks cover the rendered preview; keyboard, canvas charts, and screen-reader behavior still need manual review.</p>
      <details class="coverage" @toggle=${(event: Event) => { if ((event.target as HTMLDetailsElement).open) void this.refreshCoverage() }}>
        <summary>Fixtures & states</summary><div class="body">
          <p class="note">Available options for this configuration, not test results. Refresh after changing options.</p>
          ${this.coverage.length ? html`<dl>${this.coverage.map(control => html`<div><dt>${control.label}</dt><dd>${control.choices.join(' · ')}</dd></div>`)}</dl>` : html`<p class="note">This example has no selectable fixture variants. See Usage & events for its interactions and limitations.</p>`}
          <div><button class="settings-button" ?disabled=${!this.exampleReady} @click=${this.refreshCoverage}>Refresh fixture summary</button></div>
        </div>
      </details>
      ${chart ? html`<a href=${`https://github.com/flidai/leapview/blob/main/docs/visuals/${encodeURIComponent(chart)}.md`}>Authored ${chart} YAML examples</a>` : nothing}
      ${this.message ? html`<span role="status">${this.message}</span>` : nothing}
      ${this.code ? html`<pre tabindex="0" aria-label="Current component code">${this.code}</pre>` : nothing}
      ${this.pinned ? html`<div class="reference"><strong>Pinned reference · ${this.pinnedLabel}</strong>${keyed(this.pinRevision, html`<iframe title="Pinned example comparison" src=${this.pinned}></iframe>`)}</div>` : nothing}
      ${keyed(this.route, html`<div class="checks" role="group" aria-label="Manual review checklist">
        <label><input type="checkbox">Keyboard: Tab order, visible focus, Enter, arrows, and Escape.</label>
        <label><input type="checkbox">Layout: narrow and wide containers, long labels, and both themes.</label>
        <label><input type="checkbox">States: empty, loading, errors, and selection without misleading totals.</label>
      </div>`)}
      <p class="note" role="status" ?hidden=${!this.scanSummary}>${this.scanSummary}${this.scannedRoute && this.scannedRoute !== this.route ? ' This result is from a different example; run the check again.' : ''}</p>
      ${this.findings.length ? html`<ul>${this.findings.map(item => html`<li><a href=${item.helpUrl}>${item.help}</a> · ${item.nodes} elements<pre>${item.targets.join('\n')}</pre></li>`)}</ul>` : nothing}
    </div></details>`
  }
}
customElements.define('playground-review-tools', PlaygroundReviewTools)
