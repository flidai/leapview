import { css, html, nothing, type TemplateResult } from 'lit'

export const emptyStateStyles = css`
  .empty-state {
    display: grid;
    min-height: 10rem;
    box-sizing: border-box;
    place-items: center;
    align-content: center;
    gap: var(--base-size-8);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-large, var(--lv-radius-default));
    padding: var(--base-size-24);
    color: var(--lv-fg-muted);
    background: var(--lv-bg-panel);
    text-align: center;
  }
  .empty-state-icon { display: inline-flex; color: var(--lv-fg-muted); }
  .empty-state-copy { display: grid; max-width: 32rem; gap: var(--base-size-4); }
  .empty-state-title { color: var(--lv-fg-default); font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
  .empty-state-description { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .empty-state-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: var(--base-size-8); margin-top: var(--base-size-4); }
`

export interface EmptyStateOptions {
  title: string
  description?: string
  icon?: unknown
  actions?: unknown
  role?: 'status' | 'alert'
}

export function renderEmptyState(options: EmptyStateOptions): TemplateResult {
  return html`<div class="empty-state" role=${options.role ?? 'status'}>
    ${options.icon ? html`<span class="empty-state-icon" aria-hidden="true">${options.icon}</span>` : nothing}
    <div class="empty-state-copy">
      <span class="empty-state-title">${options.title}</span>
      ${options.description ? html`<span class="empty-state-description">${options.description}</span>` : nothing}
    </div>
    ${options.actions ? html`<div class="empty-state-actions">${options.actions}</div>` : nothing}
  </div>`
}
