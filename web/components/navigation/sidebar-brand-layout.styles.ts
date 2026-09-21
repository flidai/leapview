import { css } from 'lit'

export const sidebarBrandLayoutStyles = css`
  :host(:not([data-admin])) .brand-row {
    display: grid;
    grid-template-columns: var(--base-size-28) minmax(0, 1fr) auto;
    grid-template-areas: 'collapse identity switcher';
    column-gap: var(--base-size-4);
    align-items: center;
  }

  .brand-row > .collapse-button { grid-area: collapse; }
  .brand-row > .brand-identity { grid-area: identity; }
  .brand-row > .area-switcher { grid-area: switcher; }

  @media (min-width: 641px) {
    :host([data-collapsed][data-peeking]) .brand-row > .brand-identity {
      max-height: 0;
      overflow: hidden;
    }
  }
`
