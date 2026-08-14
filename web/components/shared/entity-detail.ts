import { css, html, nothing } from 'lit'

export const entityDetailStyles = css`
  .detail-surface { display: grid; min-width: 0; gap: 0; }
  .back-link { width: fit-content; margin-bottom: var(--base-size-24); color: var(--lv-fg-muted); text-decoration: none; }
  .back-link:hover { color: var(--lv-fg-default); text-decoration: none; }
  .detail-header { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-24); padding: var(--base-size-8) 0 var(--base-size-32); border-bottom: var(--lv-border-muted); }
  .identity { display: flex; min-width: 0; align-items: center; gap: var(--base-size-12); }
  .identity-copy { display: grid; min-width: 0; gap: var(--base-size-4); }
  .identity-copy h1 { overflow: hidden; margin: 0; color: var(--lv-fg-default); font: var(--lv-type-page-title); text-overflow: ellipsis; white-space: nowrap; }
  .identity-subtitle { color: var(--lv-fg-muted); }
  .avatar { display: inline-flex; width: var(--base-size-64); height: var(--base-size-64); flex: 0 0 var(--base-size-64); align-items: center; justify-content: center; border-radius: var(--lv-radius-full); background: var(--lv-bg-selected); color: var(--lv-fg-accent); font-weight: var(--base-text-weight-semibold); }
  .avatar-plain { border-radius: 0; background: transparent; }
  .avatar-plain svg { width: var(--base-size-32); height: var(--base-size-32); }
  .identity-badges { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-6); }
  .badge { display: inline-flex; padding: var(--base-size-2) var(--base-size-6); border-radius: var(--lv-radius-full); background: var(--lv-bg-selected); font: var(--lv-type-caption); }
  .header-actions { display: flex; flex: 0 0 auto; align-items: center; gap: var(--base-size-8); }
  .detail-notice { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: var(--base-size-12); margin-top: var(--base-size-24); border-block: var(--lv-border-muted); border-left: var(--lv-border-width) solid var(--lv-fg-accent); padding: var(--base-size-16) var(--base-size-20); color: var(--lv-fg-muted); }
  .detail-notice strong { color: var(--lv-fg-default); }
  .detail-notice-icon { color: var(--lv-fg-accent); }
  .detail-sections { display: grid; }
  .detail-notice + .detail-sections .detail-section:first-child { border-top: 0; }
  .facts { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); column-gap: var(--base-size-48); row-gap: var(--base-size-16); margin: 0; }
  .fact { display: grid; min-width: 0; grid-template-columns: minmax(7rem, 0.8fr) minmax(0, 1.2fr); align-items: baseline; gap: var(--base-size-16); }
  .fact dt { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .fact dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }

  @media (max-width: 760px) {
    .back-link { margin-bottom: var(--base-size-16); }
    .detail-header { align-items: stretch; flex-direction: column; padding-bottom: var(--base-size-24); }
    .avatar { width: var(--base-size-48); height: var(--base-size-48); flex-basis: var(--base-size-48); }
    .avatar-plain svg { width: var(--base-size-24); height: var(--base-size-24); }
    .header-actions { justify-content: flex-start; }
    .detail-notice { padding-inline: var(--base-size-12); }
    .facts { grid-template-columns: minmax(0, 1fr); }
    .fact { grid-template-columns: minmax(6.5rem, 0.7fr) minmax(0, 1.3fr); }
  }

  @media (max-width: 480px) {
    .identity-copy h1 { white-space: normal; }
    .fact { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); }
  }
`

export interface EntityDetailOptions {
  label: string
  feedback?: unknown
  backHref: string
  backLabel: string
  avatar: unknown
  avatarTreatment?: 'framed' | 'plain'
  title: string
  subtitle?: string
  badges?: unknown
  actions?: unknown
  notice?: unknown
  sections: unknown
}

export function renderEntityDetail(options: EntityDetailOptions) {
  const avatarClass = options.avatarTreatment === 'plain' ? 'avatar avatar-plain' : 'avatar'
  return html`<section class="detail-surface" aria-label=${options.label}>
    ${options.feedback || nothing}
    <a class="back-link" href=${options.backHref}>← ${options.backLabel}</a>
    <header class="detail-header">
      <div class="identity">
        <span class=${avatarClass} aria-hidden="true">${options.avatar}</span>
        <div class="identity-copy">
          <h1>${options.title}</h1>
          ${options.subtitle ? html`<span class="identity-subtitle">${options.subtitle}</span>` : nothing}
          ${options.badges ? html`<div class="identity-badges">${options.badges}</div>` : nothing}
        </div>
      </div>
      ${options.actions ? html`<div class="header-actions">${options.actions}</div>` : nothing}
    </header>
    ${options.notice || nothing}
    <div class="detail-sections">${options.sections}</div>
  </section>`
}
