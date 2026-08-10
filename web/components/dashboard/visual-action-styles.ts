import { css } from 'lit'

export const visualActionStyles = css`
  .visual-actions {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
    gap: var(--base-size-4);
  }

  .icon-action {
    display: grid;
    width: var(--lv-button-height-xs, var(--control-xsmall-size));
    height: var(--lv-button-height-xs, var(--control-xsmall-size));
    min-height: var(--lv-button-height-xs, var(--control-xsmall-size));
    place-items: center;
    border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
    border-radius: var(--lv-radius-tight);
    background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
    color: var(--lv-button-invisible-icon-rest, var(--lv-icon-muted, var(--lv-fg-muted)));
    cursor: pointer;
    padding: 0;
    font: inherit;
    line-height: 1;
  }

  .icon-action svg {
    width: var(--base-size-16);
    height: var(--base-size-16);
  }

  .icon-action:hover,
  .icon-action:focus-visible {
    border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-default)));
    background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover, var(--lv-bg-panel-muted)));
    color: var(--lv-icon-default, var(--lv-fg-default));
    outline: var(--focus-outline, var(--lv-border-default));
    outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
    outline-offset: var(--focus-outline-offset, var(--base-size-2));
  }
`
