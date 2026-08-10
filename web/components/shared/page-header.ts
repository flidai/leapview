import { css, html, nothing, type TemplateResult } from 'lit'

export const pageHeaderStyles = css`
  .page-header {
    display: grid;
    min-width: 0;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: start;
    gap: var(--base-size-8);
  }

  .page-title-block {
    min-width: 0;
  }

  .page-header h1,
  .page-header p {
    margin: 0;
  }

  .page-header h1 {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-page-title);
  }

  .page-header .page-eyebrow {
    margin-bottom: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  .page-header .page-detail {
    margin-top: var(--base-size-4);
    overflow: hidden;
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
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
