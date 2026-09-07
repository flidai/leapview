import { css } from 'lit'

export const dashboardPageInteractionStyles = css`
    .actions {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: flex-end;
      gap: var(--base-size-8);
    }

    .mobile-page-menu,
    .mobile-page-label,
    .icon-button.mobile-filter-toggle {
      display: none;
    }

    .mobile-page-label {
      max-width: 9rem;
      overflow: hidden;
      color: var(--lv-fg-muted);
      padding-inline: var(--base-size-4);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
    }

    .mobile-page-menu {
      position: relative;
      flex: 0 1 auto;
      min-width: 0;
    }

    .mobile-page-menu summary {
      display: flex;
      width: auto;
      max-width: 9rem;
      height: var(--control-medium-size);
      box-sizing: border-box;
      align-items: center;
      gap: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0 var(--base-size-8);
      list-style: none;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
    }

    .mobile-page-menu summary::-webkit-details-marker {
      display: none;
    }

    .mobile-page-menu summary:hover,
    .mobile-page-menu summary:focus-visible,
    .mobile-page-menu[open] summary {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .mobile-page-menu summary:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .mobile-page-current {
      overflow: hidden;
      min-width: 0;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .mobile-page-menu summary svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
      flex: 0 0 auto;
    }

    .mobile-page-popover {
      position: absolute;
      z-index: var(--zIndex-popover, 300);
      top: calc(100% + var(--base-size-6));
      right: 0;
      display: grid;
      width: min(18rem, calc(100vw - var(--base-size-24)));
      max-height: min(60svh, 24rem);
      gap: var(--base-size-2);
      overflow-y: auto;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-6);
      box-shadow: var(--shadow-floating-small);
    }

    .mobile-page-option {
      display: flex;
      min-height: var(--control-large-size);
      align-items: center;
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      padding: 0 var(--lv-space-control);
      text-decoration: none;
      font: var(--lv-type-body);
    }

    .mobile-page-option:hover,
    .mobile-page-option:focus-visible,
    .mobile-page-option[aria-current='page'] {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .mobile-page-option[aria-current='page'] {
      color: var(--lv-fg-link);
      font-weight: var(--base-text-weight-semibold);
    }

    .mobile-filter-toggle {
      position: relative;
      width: auto;
      min-width: var(--control-medium-size);
      grid-auto-flow: column;
      gap: var(--base-size-4);
      padding-inline: var(--base-size-8);
    }

    .mobile-filter-toggle svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .mobile-filter-count {
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

    button {
      font: inherit;
    }

    .icon-button {
      display: inline-grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      min-height: var(--control-medium-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0;
    }

    .icon-button:hover {
      background: var(--lv-bg-control-hover);
    }

    .icon-button:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

		.agent-toggle {
			display: inline-flex;
			width: auto;
			align-items: center;
			justify-content: center;
			gap: var(--base-size-6);
			border-color: var(--lv-line-muted);
			background: var(--lv-bg-control, var(--lv-bg-panel-muted));
			padding-inline: var(--base-size-12);
			font: var(--lv-type-body);
			font-weight: var(--base-text-weight-medium);
		}

		.agent-toggle[aria-expanded='true'] {
			width: var(--control-medium-size);
			padding-inline: 0;
			background: var(--lv-bg-control-hover);
		}

		.agent-toggle[aria-expanded='true'] span {
			display: none;
		}

		.agent-toggle svg,
		.ask-visual svg {
			width: var(--base-size-16);
			height: var(--base-size-16);
		}

		.ask-visual {
			display: inline-flex;
			flex: 0 0 auto;
			min-width: max-content;
			height: var(--lv-button-height-xs, var(--control-xsmall-size));
			min-height: var(--lv-button-height-xs, var(--control-xsmall-size));
			align-items: center;
			gap: var(--base-size-4);
			border: var(--borderWidth-default, var(--lv-border-width)) solid transparent;
			border-radius: var(--lv-radius-tight);
			background: transparent;
			color: var(--lv-button-invisible-icon-rest, var(--lv-icon-muted));
			cursor: pointer;
			opacity: 0;
			pointer-events: none;
			padding: 0 var(--base-size-6);
			font: var(--lv-type-caption);
			font-weight: var(--base-text-weight-medium);
			line-height: 1;
			transition: opacity var(--lv-transition-fast), background-color var(--lv-transition-fast), color var(--lv-transition-fast);
		}

		lv-dashboard-visual-frame:hover .ask-visual,
		lv-dashboard-visual-frame:focus-within .ask-visual,
		.ask-visual:focus-visible,
		lv-dashboard-visual-frame[data-agent-referenced] .ask-visual {
			opacity: 1;
			pointer-events: auto;
		}

		.ask-visual:hover,
		.ask-visual:focus-visible,
		.ask-visual[aria-pressed='true'] {
			border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-default)));
			background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover, var(--lv-bg-panel-muted)));
			color: var(--lv-icon-default, var(--lv-fg-default));
			outline: 0;
		}

		.ask-visual:focus-visible {
			outline: var(--focus-outline, var(--lv-border-default));
			outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
			outline-offset: var(--focus-outline-offset, var(--base-size-2));
		}

    .icon-button[disabled] {
      cursor: not-allowed;
      color: var(--lv-fg-muted);
      opacity: 0.64;
    }

    .body {
      position: relative;
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: stretch;
      overflow: hidden;
    }

    lv-filter-dock {
      grid-column: 2;
      grid-row: 1;
    }

    .dashboard-refresh-progress {
      position: absolute;
      inset: 0 0 auto;
      z-index: var(--zIndex-sticky, 50);
      height: 2px;
      overflow: hidden;
      background: var(--lv-line-muted);
      opacity: 0;
      pointer-events: none;
      transition: opacity var(--motion-transition-stateChange);
      transition-delay: 0s;
    }

    .dashboard-refresh-progress[data-active='true'] {
      opacity: 1;
      transition-delay: 0s;
    }

    .dashboard-refresh-progress[data-active='false'][data-complete='true'] {
      transition-delay: 180ms;
    }

    .dashboard-refresh-progress-value {
      width: 0;
      height: 100%;
      background: var(--lv-line-accent);
      transition: width var(--motion-transition-stateChange);
    }

    .filter-validation {
      position: absolute;
      z-index: var(--zIndex-sticky, 50);
      top: var(--base-size-8);
      left: 50%;
      max-width: min(36rem, calc(100% - var(--base-size-24)));
      border: var(--lv-border-danger);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-danger);
      padding: var(--base-size-8) var(--base-size-12);
      box-shadow: var(--shadow-floating-small);
      font: var(--lv-type-body);
      transform: translateX(-50%);
    }

    .canvas-wrap {
      display: grid;
      grid-column: 1;
      grid-row: 1;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      background: transparent;
      padding: 0;
    }

    .heading-visual {
      display: grid;
      height: 100%;
      min-height: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--base-size-12);
      padding: var(--base-size-8);
    }

    .eyebrow {
      margin-bottom: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
    }

    .heading-visual h2 {
      color: var(--lv-fg-default);
      font: var(--lv-type-title-large);
    }

    .badges {
      display: flex;
      flex-wrap: wrap;
      justify-content: flex-end;
      gap: var(--base-size-8);
    }

    .badge {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      padding: var(--base-size-2) var(--base-size-8);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .unsupported {
      display: grid;
      height: 100%;
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      padding: var(--base-size-16);
      text-align: center;
      font: var(--lv-type-body);
    }


`
