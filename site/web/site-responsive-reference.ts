import { LitElement, css, html } from 'lit'
import { DatastarLit } from '../../web/components/shared/datastar-lit'
import type {
  DashboardCompiledFilterBinding,
  DashboardCompiledFilterDefinition,
  DashboardFilterExpression,
  DashboardFilterPresentation,
} from '../../web/generated/signals'
import type { VisualPayload } from './site-types'
import { kpiLayoutFeatures, resolveKPIWidgetLayout } from '../../web/components/dashboard/visualization/kpi-layout'
import {
  layoutRequirements,
  resolveWidgetLayout,
  widgetChrome,
  type WidgetContractID,
  type WidgetLayoutFeature,
  type WidgetLayoutResolution,
} from '../../web/components/dashboard/visualization/layout'

type KPIScenario = Readonly<{ id: string; label: string; description: string }>
type FilterScenario = Readonly<{
  id: string
  label: string
  description: string
  contract: WidgetContractID
  definition: DashboardCompiledFilterDefinition
  presentation: DashboardFilterPresentation
  expression: DashboardFilterExpression
}>

const kpiScenarios: readonly KPIScenario[] = [
  { id: 'total_orders', label: 'Basic value', description: 'Current value and an explicit note.' },
  { id: 'revenue_kpi_trend', label: 'Trend', description: 'Current value with an explicit sparkline.' },
  { id: 'revenue_kpi_unfavorable', label: 'Comparison', description: 'Baseline, delta, and authored unfavorable direction.' },
  { id: 'revenue_kpi_favorable', label: 'Comparison and trend', description: 'Baseline context and sparkline together.' },
  { id: 'revenue_kpi_bullet', label: 'Bullet', description: 'Goal, qualitative ranges, and measured value.' },
  { id: 'revenue_kpi_out_of_range', label: 'Progress', description: 'Goal progress with truthful out-of-range status.' },
  { id: 'revenue_kpi_status', label: 'Status', description: 'Qualitative status without implying a goal.' },
  { id: 'revenue_kpi_all_features', label: 'All features — stress test', description: 'Boundary coverage for subtitle, comparison, progress, goal, status, trend, and note.' },
  { id: 'revenue_kpi_missing_comparison', label: 'Missing comparison', description: 'Unavailable baseline remains visibly distinct from zero.' },
]

const filterBinding: DashboardCompiledFilterBinding = {
  key: 'qa-filter', id: 'qa-filter', filter: 'qa-filter', scope: 'page', pageID: 'responsive-widgets',
  default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
  readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
}

const filterScenarios: readonly FilterScenario[] = [
  {
    id: 'dropdown', label: 'Dropdown', description: 'One categorical selection with static options.', contract: 'slicer.dropdown',
    definition: filterDefinition('state', 'State', 'string', 'set', {
      kind: 'static', limit: 3, values: [
        { value: { kind: 'string', value: 'CA' }, label: 'California' },
        { value: { kind: 'string', value: 'NY' }, label: 'New York' },
        { value: { kind: 'string', value: 'TX' }, label: 'Texas' },
      ],
    }),
    presentation: filterPresentation('dropdown'),
    expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] },
  },
  {
    id: 'input', label: 'Comparison input', description: 'An explicit operator and numeric value.', contract: 'slicer.input',
    definition: filterDefinition('revenue', 'Revenue', 'decimal', 'comparison'),
    presentation: filterPresentation('input'),
    expression: { kind: 'comparison', operator: 'greater_than_or_equal', value: { kind: 'decimal', value: '1000' } },
  },
  {
    id: 'numeric-range', label: 'Numeric range', description: 'Minimum and maximum remain present in both layouts.', contract: 'slicer.numeric_range',
    definition: filterDefinition('order_value', 'Order value', 'decimal', 'range'),
    presentation: filterPresentation('numeric_range'),
    expression: {
      kind: 'range',
      lower: { value: { kind: 'decimal', value: '50' }, inclusive: true },
      upper: { value: { kind: 'decimal', value: '500' }, inclusive: true },
    },
  },
  {
    id: 'date-range', label: 'Date range', description: 'Start and end dates rearrange without overlap.', contract: 'slicer.date_range',
    definition: filterDefinition('purchase_date', 'Purchase date', 'date', 'range'),
    presentation: filterPresentation('date_range'),
    expression: {
      kind: 'range',
      lower: { value: { kind: 'date', value: '2026-01-01' }, inclusive: true },
      upper: { value: { kind: 'date', value: '2026-03-31' }, inclusive: true },
    },
  },
  {
    id: 'relative-period', label: 'Relative period', description: 'Direction, count, and unit remain explicit.', contract: 'slicer.relative_period',
    definition: filterDefinition('period', 'Relative period', 'timestamp', 'relative_period'),
    presentation: filterPresentation('relative_period'),
    expression: { kind: 'relative_period', direction: 'previous', count: 3, unit: 'month', includeCurrent: false, anchor: 'current_time' },
  },
]

class SiteResponsiveWidgetReference extends DatastarLit(LitElement) {
  private previewWidget: 'kpi' | 'date-range' = 'kpi'
  private previewWidth = 250
  private previewHeight = 130

  static styles = css`
    :host {
      display: grid;
      min-width: 0;
      gap: clamp(var(--base-size-40), 6vw, var(--base-size-64));
    }

    section,
    .section-heading,
    .scenario-copy,
    .playground-copy {
      display: grid;
    }

    section { gap: var(--base-size-20); }
    .section-heading { max-width: 48rem; gap: var(--base-size-6); }

    h2,
    h3,
    p { margin: 0; }

    h2,
    h3 { color: var(--lv-fg-default); }
    h2 { font-size: var(--text-title-size-large); line-height: var(--base-text-lineHeight-tight); }
    h3 { font-size: var(--text-title-size-medium); line-height: var(--base-text-lineHeight-tight); }
    p { color: var(--lv-fg-muted); line-height: var(--base-text-lineHeight-relaxed); }

    .scenario-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(min(100%, 33rem), 1fr));
      gap: var(--base-size-16);
    }

    .scenario {
      display: grid;
      min-width: 0;
      container-type: inline-size;
      align-content: start;
      gap: var(--base-size-16);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      box-shadow: var(--shadow-resting-small);
      padding: var(--base-size-16);
    }

    .scenario-copy { gap: var(--base-size-4); }

    .feature-list {
      display: flex;
      flex-wrap: wrap;
      gap: var(--base-size-4);
      margin-top: var(--base-size-4);
    }

    .feature {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      padding: var(--base-size-2) var(--base-size-8);
      font-size: var(--text-caption-size);
      line-height: var(--base-text-lineHeight-tight);
    }

    .frame-row {
      display: flex;
      min-width: 0;
      align-items: flex-start;
      gap: var(--base-size-16);
      overflow-x: auto;
      padding: var(--base-size-2) var(--base-size-2) var(--base-size-8);
    }

    figure {
      display: grid;
      flex: 0 0 auto;
      min-width: 0;
      gap: var(--base-size-8);
      margin: 0;
    }

    .layout-frame,
    .playground-frame {
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
    }

    .layout-frame lv-visualization-host,
    .layout-frame lv-slicer,
    .playground-frame lv-visualization-host,
    .playground-frame lv-slicer {
      display: block;
      width: 100%;
      height: 100%;
    }

    figcaption,
    .diagnostic {
      color: var(--lv-fg-muted);
      font-size: var(--text-caption-size);
      line-height: var(--base-text-lineHeight-tight);
    }

    figcaption { overflow-wrap: anywhere; }

    figcaption strong,
    .diagnostic strong { color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }

    .playground {
      display: grid;
      grid-template-columns: minmax(16rem, 22rem) minmax(0, 1fr);
      align-items: start;
      gap: var(--base-size-24);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      padding: var(--base-size-20);
    }

    .playground-copy { gap: var(--base-size-16); }
    .control { display: grid; gap: var(--base-size-6); }
    .control label { color: var(--lv-fg-default); font-size: var(--text-body-size-medium); font-weight: var(--base-text-weight-semibold); }
    .control-output { color: var(--lv-fg-muted); font-variant-numeric: tabular-nums; }
    select { min-height: var(--control-medium-size); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); padding-inline: var(--base-size-8); font: inherit; }
    input[type='range'] { width: 100%; accent-color: var(--lv-line-accent); }

    .playground-stage {
      min-width: 0;
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      padding: var(--base-size-16);
    }

    .playground-stage figure { width: max-content; }
    .playground-frame[data-fit='too-small'] { outline: 2px solid var(--lv-line-danger); outline-offset: -2px; }

    @container (width < 532px) {
      .frame-row {
        flex-direction: column;
        overflow-x: visible;
      }
    }

    @media (width < 48rem) {
      .playground { grid-template-columns: minmax(0, 1fr); padding: var(--base-size-16); }
      .scenario { padding: var(--base-size-12); }
      .frame-row {
        flex-direction: column;
        overflow-x: auto;
      }
    }

    @media (width < 25rem) {
      .scenario { padding-inline: var(--base-size-8); }
    }
  `

  render() {
    const visuals = this.signal<VisualPayload[]>('visuals', [])
    const indexed = new Map(visuals.map((visual) => [visual.visualID, visual]))
    const scenarios = kpiScenarios.flatMap((scenario) => {
      const visual = indexed.get(scenario.id)
      return visual ? [{ scenario, visual }] : []
    })
    const playgroundKPI = indexed.get('revenue_kpi_favorable')
    return html`
      <section aria-labelledby="responsive-kpi-heading">
        <div class="section-heading">
          <h2 id="responsive-kpi-heading">KPI feature combinations</h2>
          <p>Each configuration uses one compiled payload in every registered layout. Feature chips describe authored intent; every fixed preview is an exact-minimum boundary test.</p>
        </div>
        <div class="scenario-grid">
          ${scenarios.map(({ scenario, visual }) => this.renderKPIScenario(scenario, visual))}
        </div>
      </section>
      <section aria-labelledby="responsive-filter-heading">
        <div class="section-heading">
          <h2 id="responsive-filter-heading">Dashboard filter controls</h2>
          <p>These are production slicer controls, not chart renderings. Every explicit field stays present while the layout changes around it; every fixed preview is an exact-minimum boundary test.</p>
        </div>
        <div class="scenario-grid">
          ${filterScenarios.map((scenario) => this.renderFilterScenario(scenario))}
        </div>
      </section>
      <section aria-labelledby="responsive-playground-heading">
        <div class="section-heading">
          <h2 id="responsive-playground-heading">Intermediate-size playground</h2>
          <p>Inspect dimensions between the fixed frames. A red inset marks a size that violates the hard minimum; content remains present for diagnosis.</p>
        </div>
        ${playgroundKPI ? this.renderPlayground(playgroundKPI) : html`<p>Loading compiled examples…</p>`}
      </section>
    `
  }

  private renderKPIScenario(scenario: KPIScenario, visual: VisualPayload) {
    const features = kpiLayoutFeatures(visual)
    return html`<article class="scenario" data-kpi-scenario=${scenario.id}>
      <div class="scenario-copy">
        <h3>${scenario.label}</h3>
        <p>${scenario.description}</p>
        <div class="feature-list" aria-label="Explicit features">
          ${(features.length ? features : ['value']).map((feature) => html`<span class="feature">${feature}</span>`)}
        </div>
      </div>
      <div class="frame-row">
        ${layoutRequirements('kpi', features).map((requirement) => this.renderKPIFrame(scenario.label, visual, requirement.layout, requirement.minimum.width, requirement.minimum.height))}
      </div>
    </article>`
  }

  private renderKPIFrame(label: string, visual: VisualPayload, layout: string, width: number, height: number) {
    const ariaLabel = `${label}, ${layout} layout, ${width}×${height}`
    return html`<figure data-layout-frame=${layout} aria-label=${ariaLabel} style=${frameWidth(width)}>
      <div class="layout-frame" style=${frameSize(width, height)}>
        <lv-visualization-host .envelope=${visual}></lv-visualization-host>
      </div>
      <figcaption><strong>${layout}</strong> · ${width}×${height}</figcaption>
    </figure>`
  }

  private renderFilterScenario(scenario: FilterScenario) {
    const chrome = widgetChrome(scenario.contract)
    const features = slicerLayoutFeatures(scenario.presentation)
    return html`<article class="scenario" data-filter-scenario=${scenario.id}>
      <div class="scenario-copy">
        <h3>${scenario.label}</h3>
        <p>${scenario.description}</p>
      </div>
      <div class="frame-row">
        ${layoutRequirements(scenario.contract, features).map((requirement) => {
          const width = requirement.minimum.width + chrome.width
          const height = requirement.minimum.height + chrome.height
          const ariaLabel = `${scenario.label}, ${requirement.layout} layout, ${width}×${height}`
          return html`<figure data-layout-frame=${requirement.layout} aria-label=${ariaLabel} style=${frameWidth(width)}>
            <div class="layout-frame" style=${frameSize(width, height)}>
              ${filterSlicer(scenario)}
            </div>
            <figcaption><strong>${requirement.layout}</strong> · ${width}×${height}</figcaption>
          </figure>`
        })}
      </div>
    </article>`
  }

  private renderPlayground(kpi: VisualPayload) {
    const isKPI = this.previewWidget === 'kpi'
    const filter = filterScenarios.find((scenario) => scenario.id === 'date-range')!
    const resolution = isKPI
      ? resolveKPIWidgetLayout(kpi, { width: this.previewWidth, height: this.previewHeight })
      : filterOuterResolution(filter, this.previewWidth, this.previewHeight)
    const selected = selectedLayout(resolution)
    return html`<div class="playground">
      <div class="playground-copy">
        <div class="control">
          <label for="preview-widget">Preview widget</label>
          <select id="preview-widget" aria-label="Preview widget" @change=${this.changePreviewWidget}>
            <option value="kpi" ?selected=${isKPI}>KPI · comparison and trend</option>
            <option value="date-range" ?selected=${!isKPI}>Filter · date range</option>
          </select>
        </div>
        <div class="control">
          <label for="preview-width">Preview width</label>
          <input id="preview-width" type="range" min="160" max="520" step="1" .value=${String(this.previewWidth)} aria-label="Preview width" @input=${this.changePreviewWidth}>
          <output class="control-output" for="preview-width">${this.previewWidth}px</output>
        </div>
        <div class="control">
          <label for="preview-height">Preview height</label>
          <input id="preview-height" type="range" min="80" max="320" step="1" .value=${String(this.previewHeight)} aria-label="Preview height" @input=${this.changePreviewHeight}>
          <output class="control-output" for="preview-height">${this.previewHeight}px</output>
        </div>
        <p class="diagnostic"><strong>${selected.layout}</strong> · ${resolution.kind === 'fit' ? 'fits' : 'below minimum'} · requires ${selected.minimum.width}×${selected.minimum.height}</p>
      </div>
      <div class="playground-stage">
        <figure>
          <div
            class="playground-frame"
            data-playground-frame
            data-selected-layout=${selected.layout}
            data-fit=${resolution.kind === 'fit' ? 'fit' : 'too-small'}
            style=${frameSize(this.previewWidth, this.previewHeight)}
          >
            ${isKPI ? html`<lv-visualization-host .envelope=${kpi}></lv-visualization-host>` : filterSlicer(filter)}
          </div>
          <figcaption><strong>${this.previewWidth}×${this.previewHeight}</strong> · selected ${selected.layout}</figcaption>
        </figure>
      </div>
    </div>`
  }

  private changePreviewWidget = (event: Event) => {
    this.previewWidget = (event.currentTarget as HTMLSelectElement).value === 'date-range' ? 'date-range' : 'kpi'
    this.requestUpdate()
  }

  private changePreviewWidth = (event: Event) => {
    this.previewWidth = Number((event.currentTarget as HTMLInputElement).value)
    this.requestUpdate()
  }

  private changePreviewHeight = (event: Event) => {
    this.previewHeight = Number((event.currentTarget as HTMLInputElement).value)
    this.requestUpdate()
  }
}

if (!customElements.get('lv-site-responsive-widget-reference')) {
  customElements.define('lv-site-responsive-widget-reference', SiteResponsiveWidgetReference)
}

function filterDefinition(
  id: string,
  label: string,
  valueKind: DashboardCompiledFilterDefinition['valueKind'],
  predicate: 'set' | 'comparison' | 'range' | 'relative_period',
  options: DashboardCompiledFilterDefinition['options'] = { kind: 'none', limit: 0, values: [] },
): DashboardCompiledFilterDefinition {
  return {
    id, label, field: `orders.${id}`, valueKind,
    predicates: [{ kind: predicate, operators: predicate === 'comparison' ? ['greater_than_or_equal'] : [] }],
    options, timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday',
  }
}

function filterPresentation(style: DashboardFilterPresentation['style']): DashboardFilterPresentation {
  return { style, search: false, selectAll: false, showCounts: false, showSummary: false, compact: false }
}

function filterSlicer(scenario: FilterScenario) {
  return html`<lv-slicer
    .definition=${scenario.definition}
    .binding=${{ ...filterBinding, key: scenario.id, id: scenario.id, filter: scenario.definition.id }}
    .expression=${scenario.expression}
    .presentation=${scenario.presentation}
  ></lv-slicer>`
}

function slicerLayoutFeatures(presentation: DashboardFilterPresentation): WidgetLayoutFeature[] {
  return presentation.showSummary ? ['summary'] : []
}

function filterOuterResolution(scenario: FilterScenario, width: number, height: number): WidgetLayoutResolution {
  const chrome = widgetChrome(scenario.contract)
  return resolveWidgetLayout(scenario.contract, {
    width: Math.max(0, width - chrome.width),
    height: Math.max(0, height - chrome.height),
  }, slicerLayoutFeatures(scenario.presentation))
}

function selectedLayout(resolution: WidgetLayoutResolution) {
  return resolution.kind === 'fit' ? resolution : resolution.requirements.at(-1)!
}

function frameSize(width: number, height: number): string {
  return `width: ${width}px; height: ${height}px`
}

function frameWidth(width: number): string {
  return `width: ${width}px`
}
