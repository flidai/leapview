import { css } from 'lit'

export const filterDockStyles = css`
    :host {
      position: relative;
      z-index: var(--zIndex-sticky, 50);
      display: block;
      width: var(--lv-page-rail-width-collapsed);
      min-width: 0;
      min-height: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      transition: width var(--lv-duration-fast) var(--motion-easing-move);
    }

    :host([data-open]) {
      width: var(--lv-dashboard-filter-open-width);
    }

    aside {
      display: grid;
      width: var(--lv-page-rail-width-collapsed);
      box-sizing: border-box;
      min-width: 0;
      min-height: 0;
      height: 100%;
      overflow: hidden;
      border-left: var(--lv-border-default);
      background: var(--lv-bg-app);
      transition:
        width var(--lv-duration-fast) var(--motion-easing-move),
        background-color var(--lv-duration-fast) var(--motion-easing-move);
    }

    aside[data-open] {
      position: relative;
      width: 100%;
      border-left: var(--lv-border-default);
      background: var(--lv-bg-app);
    }

    button {
      font: inherit;
    }

    .rail {
      display: flex;
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      box-sizing: border-box;
      align-items: center;
      align-content: start;
      flex-direction: column;
      justify-items: center;
      justify-content: flex-start;
      gap: var(--base-size-8);
      border: 0;
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: var(--base-size-16) 0;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .rail:hover {
      color: var(--lv-fg-default);
      background: var(--lv-bg-control-hover);
    }

    .rail:focus-visible,
    .icon-button:focus-visible,
    .footer-button:focus-visible {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent);
      outline-offset: calc(-1 * var(--base-size-2));
    }

    aside[data-open] .rail {
      display: none;
    }

    .rail span {
      writing-mode: vertical-rl;
      line-height: 1;
    }

    .rail-count {
      display: grid;
      min-width: 18px;
      min-height: 18px;
      place-items: center;
      border-radius: var(--lv-radius-full);
      background: var(--lv-line-accent);
      color: var(--lv-fg-on-emphasis);
      font: var(--lv-type-caption);
      line-height: 1;
    }

    .panel {
      display: none;
      position: static;
      width: auto;
      height: auto;
      max-width: none;
      max-height: none;
      box-sizing: border-box;
      min-width: 0;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr) auto;
      overflow: hidden;
      margin: 0;
      border: 0;
      background: var(--lv-bg-app);
      color: inherit;
      padding: 0;
    }

    aside[data-open] .panel[open] {
      display: grid;
    }

    .panel::backdrop {
      background: transparent;
    }

    .panel-header,
    .panel-footer {
      position: relative;
      z-index: 1;
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-app);
      padding: var(--base-size-12);
    }

    .panel-header {
      justify-content: space-between;
      border-bottom: var(--lv-border-muted);
    }

    .panel-heading {
      display: grid;
      min-width: 0;
      gap: var(--base-size-2);
    }

    .panel-heading strong {
      font: var(--lv-type-section-title);
    }

    .panel-summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .icon-button {
      display: inline-grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      flex: 0 0 auto;
      place-items: center;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
    }

    .icon-button:hover {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
    }

    .panel-scroll {
      min-width: 0;
      min-height: 0;
      overflow: auto;
      overscroll-behavior: contain;
      padding: var(--base-size-12);
    }

    .filter-group {
      display: grid;
      gap: var(--base-size-8);
    }

    .filter-group + .filter-group {
      margin-top: var(--base-size-16);
      padding-top: var(--base-size-16);
      border-top: var(--lv-border-muted);
    }

    .group-heading {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--base-size-8);
    }

    .group-title {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      letter-spacing: .02em;
      text-transform: uppercase;
    }

    .group-count {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .panel-footer {
      display: grid;
      border-top: var(--lv-border-muted);
    }

    .footer-row {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: flex-end;
      gap: var(--base-size-8);
    }

    .footer-row:first-child {
      justify-content: space-between;
    }

    .footer-button {
      min-height: var(--control-medium-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0 var(--lv-space-control);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .footer-button:hover:not(:disabled) {
      background: var(--lv-bg-control-hover);
    }

    .footer-button.primary {
      border-color: var(--lv-line-accent);
      background: var(--lv-line-accent);
      color: var(--lv-fg-on-emphasis);
    }

    .footer-button.reset {
      display: inline-flex;
      align-items: center;
      gap: var(--base-size-4);
      border-color: transparent;
      background: transparent;
      color: var(--lv-fg-muted);
    }

    .footer-button:disabled {
      cursor: default;
      opacity: .48;
    }

    @media (max-width: 640px) {
      :host,
      :host([data-open]) {
        z-index: var(--zIndex-modal, 200);
        width: 100%;
      }

      aside {
        width: 100%;
        border-left: 0;
        border-top: var(--lv-border-default);
      }

      aside[data-open] {
        position: fixed;
        inset: 0;
        width: 100%;
        height: 100dvh;
        border: 0;
        box-shadow: none;
      }

      .panel[open] {
        position: fixed;
        inset: 0;
        width: 100%;
        height: 100dvh;
      }

      .rail {
        min-height: 68px;
        height: auto;
        flex-direction: row;
        justify-content: center;
        padding: var(--base-size-12);
      }

      .rail[data-suppressed] {
        display: none;
      }

      .rail span,
      aside[data-open] .rail span {
        writing-mode: horizontal-tb;
      }

      .panel-header,
      .panel-footer {
        padding:
          max(var(--base-size-12), env(safe-area-inset-top))
          max(var(--base-size-12), env(safe-area-inset-right))
          var(--base-size-12)
          max(var(--base-size-12), env(safe-area-inset-left));
      }

      .panel-footer {
        padding-top: var(--base-size-12);
        padding-bottom: max(var(--base-size-12), env(safe-area-inset-bottom));
      }

      .panel-scroll {
        padding: var(--base-size-12);
      }
    }

    @media (prefers-reduced-motion: reduce) {
      :host, aside {
        transition: none;
      }
    }
`
