import { css } from 'lit'

export const sidebarRailStyles = css`
    .rail-link {
      box-sizing: border-box;
      display: grid;
      width: var(--base-size-28);
      height: var(--base-size-28);
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
      text-decoration: none;
    }

    .rail-link:hover,
    .rail-link:focus-visible,
    .rail-link[aria-current='page'] {
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
    }

    .rail-link:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .rail-divider {
      width: var(--base-size-20);
      height: 1px;
      justify-self: center;
      background: var(--lv-line-muted);
    }

    .rail-search {
      margin-top: auto;
    }

`
