import { css } from 'lit'

export const dataExplorerHandoffStyles = css`
  .header-title { display: flex; min-width: 0; align-items: center; gap: var(--base-size-8); }
  .return-link {
    display: inline-flex; flex: 0 0 auto; align-items: center; gap: var(--base-size-4);
    color: var(--lv-fg-muted); text-decoration: none; font: var(--lv-type-caption);
  }
  .return-link:hover, .return-link:focus-visible { color: var(--lv-fg-link); }
  .return-link svg { width: var(--base-size-16); height: var(--base-size-16); }
  .header-divider { flex: 0 0 auto; color: var(--lv-fg-muted); }
`
