import { css, html, nothing, type TemplateResult } from 'lit'

export const pageHeaderStyles = css`
  .page-header {
    display: grid;
    min-width: 0;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--base-size-16);
    border-bottom: var(--lv-border-muted);
    padding-bottom: var(--base-size-16);
  }

  .page-title-block {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .page-header h1,
  .page-header p {
    margin: 0;
  }

  .page-header h1 {
    color: var(--lv-fg-default);
    overflow-wrap: anywhere;
    font: var(--lv-type-page-title);
  }

  .page-header .page-eyebrow {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  .page-header .page-detail {
    max-width: 60rem;
    color: var(--lv-fg-muted);
    overflow-wrap: anywhere;
    font: var(--lv-type-body-compact);
  }

  .page-actions {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    justify-content: flex-end;
    gap: var(--base-size-8);
  }

  .page-actions:empty {
    display: none;
  }

  @media (max-width: 720px) {
    .page-header {
      grid-template-columns: 1fr;
      align-items: start;
    }

    .page-actions {
      justify-content: flex-start;
    }
  }
`

export function renderPageHeader(title: string, detail = '', eyebrow = '', actions: unknown = nothing): TemplateResult {
  return html`
    <header class="page-header">
      <div class="page-title-block">
        ${eyebrow ? html`<p class="page-eyebrow">${eyebrow}</p>` : nothing}
        <h1>${title}</h1>
        ${detail ? html`<p class="page-detail">${detail}</p>` : nothing}
      </div>
      <div class="page-actions">${actions}</div>
    </header>
  `
}
