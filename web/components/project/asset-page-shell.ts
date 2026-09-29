import { html, nothing, type TemplateResult } from 'lit'

interface AssetTab {
  label: string
  href: string
  active?: boolean
}

interface AssetPageShellOptions {
  label: string
  className?: string
  breadcrumb: unknown
  actions?: unknown
  tabs: AssetTab[]
  bodyClass?: string
  body: unknown
  onRecordTableAction?: (event: Event) => void
}

export function renderAssetTabs(tabs: AssetTab[], label = 'Asset sections'): TemplateResult | typeof nothing {
  if (!tabs.length) return nothing
  return html`<nav class="tabs" aria-label=${label}>
    ${tabs.map((tab) => html`<a class=${tab.active ? 'active' : ''} href=${tab.href} aria-current=${tab.active ? 'page' : nothing}><span>${tab.label}</span></a>`)}
  </nav>`
}

export function renderAssetPageShell(options: AssetPageShellOptions): TemplateResult {
  return html`<section class=${options.className || 'asset-page'} aria-label=${options.label} @lv-record-table-action=${options.onRecordTableAction ?? nothing}>
    <header class="breadcrumb-header">
      ${options.breadcrumb}
      <div class="actions">${options.actions ?? nothing}</div>
    </header>
    <div class="asset-body">
      ${renderAssetTabs(options.tabs)}
      <div class=${options.bodyClass || 'section-body'}>${options.body}</div>
    </div>
  </section>`
}
