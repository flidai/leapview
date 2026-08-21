import { LitElement, css, html } from 'lit'
import { BookOpen } from 'lucide'
import { DatastarLit } from '../../web/components/shared/datastar-lit'
import type { VisualPayload, VisualShowcaseDocument } from './site-types'
import { lucideIcon } from '../../web/components/shared/lucide-icons'

class SiteVisualShowcase extends DatastarLit(LitElement) {
  static styles = css`
    :host {
      display: block;
    }

    .showcase-section {
      display: grid;
      gap: var(--base-size-16);
    }

    .section-heading {
      display: grid;
      gap: var(--base-size-4);
    }

    h2,
    p {
      margin: 0;
    }

    h2 {
      color: var(--lv-fg-default);
      font-size: var(--text-title-size-large);
      font-weight: var(--base-text-weight-semibold);
      line-height: var(--base-text-lineHeight-tight);
    }

    p {
      color: var(--lv-fg-muted);
      font-size: var(--text-body-size-medium);
      line-height: var(--base-text-lineHeight-relaxed);
    }

    .chart-grid,
    .table-grid {
      display: grid;
      gap: var(--base-size-16);
    }

    .chart-grid {
      grid-template-columns: repeat(auto-fit, minmax(18rem, 1fr));
    }

    .table-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .chart {
      min-width: 0;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
      box-shadow: var(--shadow-resting-small);
      overflow: hidden;
    }

    .chart .visual-frame {
      height: 20rem;
    }

    .table-card {
      min-width: 0;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
      box-shadow: var(--shadow-resting-small);
      overflow: hidden;
    }

    .table-card .visual-frame {
      height: 26rem;
    }

    .table-card.featured {
      grid-column: 1 / -1;
    }

    .table-card.featured .visual-frame {
      height: 30rem;
    }

    .visual-frame {
      min-width: 0;
      overflow: hidden;
    }

    lv-visualization-host {
      display: block;
      height: 100%;
    }

    .visual-card-footer {
      display: flex;
      min-height: 3.25rem;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-12);
      padding: var(--base-size-8) var(--base-size-12);
      border-top: var(--lv-border-default);
      background: var(--lv-bg-panel-muted);
    }

    .visual-type {
      min-width: 0;
      overflow: hidden;
      color: var(--lv-fg-muted);
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .docs-link {
      display: inline-flex;
      min-height: 2rem;
      flex: none;
      align-items: center;
      gap: var(--base-size-4);
      padding: var(--base-size-4) var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-semibold);
      line-height: var(--base-text-lineHeight-normal);
      text-decoration: none;
    }

    .docs-link:hover,
    .docs-link:focus-visible {
      border-color: var(--lv-button-border-hover);
      background: var(--lv-button-bg-hover);
    }

    .docs-link:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    @media (width < 48rem) {
      .table-grid {
        grid-template-columns: minmax(0, 1fr);
      }

      .table-card.featured {
        grid-column: auto;
      }
    }
  `

  render() {
    const visuals = this.signal<VisualPayload[]>('visuals', [])
    const documents = this.signal<VisualShowcaseDocument[]>('visualDocuments', [])
    const entries = documents.flatMap((document) => {
      const visual = visuals.find((candidate) => candidate.visualID === document.visualID)
      return visual ? [{ document, visual }] : []
    })
    const charts = entries.filter(({ visual }) => !isTabularVisualType(visual.spec.kind))
    const tables = entries.filter(({ visual }) => isTabularVisualType(visual.spec.kind))
    return html`
      <section class="showcase-section" aria-labelledby="chart-showcase-heading">
        <div class="section-heading">
          <h2 id="chart-showcase-heading">Charts and KPIs</h2>
          <p>Renderer-neutral visual payloads adapted by the built-in ECharts and KPI renderers.</p>
        </div>
        <div class="chart-grid">${charts.map(({ document, visual }) => visualShowcaseCard(document, visual, 'chart'))}</div>
      </section>
      <section class="showcase-section" aria-labelledby="table-showcase-heading">
        <div class="section-heading">
          <h2 id="table-showcase-heading">Tables, matrices, and pivots</h2>
          <p>Virtualized table, matrix, and pivot payloads from the same generated visual catalog.</p>
        </div>
        <div class="table-grid">
          ${tables.map(({ document, visual }, index) => visualShowcaseCard(document, visual, `table-card ${index === 0 ? 'featured' : ''}`))}
        </div>
      </section>
    `
  }
}

function visualShowcaseCard(document: VisualShowcaseDocument, visual: VisualPayload, className: string) {
  const label = `Open ${document.title} documentation`
  return html`<article class=${className}>
    <div class="visual-frame"><lv-visualization-host .envelope=${visual}></lv-visualization-host></div>
    <footer class="visual-card-footer">
      <span class="visual-type">${document.title}</span>
      <a class="docs-link" href=${`/docs/${document.slug}`} aria-label=${label} title=${label}>
        ${lucideIcon(BookOpen, { size: 15, strokeWidth: 2 })}
        <span>View docs</span>
      </a>
    </footer>
  </article>`
}

if (!customElements.get('lv-site-visual-showcase')) {
  customElements.define('lv-site-visual-showcase', SiteVisualShowcase)
}

function isTabularVisualType(type: string): boolean {
  return type === 'table' || type === 'matrix' || type === 'pivot'
}
