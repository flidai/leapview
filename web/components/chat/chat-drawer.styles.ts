import { css } from 'lit'

export const chatDrawerStyles = css`
    :host {
      display: block;
      box-sizing: border-box;
      width: 0;
      min-width: 0;
      height: 100svh;
      overflow: hidden;
      border-left: 0 solid var(--lv-line-muted);
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      --lv-chat-stack-width: 100%;
    }

    :host([open]) {
			width: 100%;
      border-left-width: 1px;
    }

    :host([embedded]) {
      height: 100%;
      border-left: 0;
    }

    :host([open][expanded]) {
      position: fixed;
      inset: 0;
      z-index: var(--zIndex-modal, 200);
      width: 100%;
      height: 100svh;
      border: 0;
      --lv-chat-stack-width: 760px;
    }

    :host([expanded]) .title { display: flex; }
    :host([expanded]) lv-chat-composer { --lv-chat-composer-width: 760px; }

    .drawer {
      display: grid;
			width: 100%;
      height: 100%;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr) auto;
      background: var(--lv-bg-app);
    }

    .drawer.welcome-mode { grid-template-rows: auto minmax(0, 1fr); }

    .header {
      display: grid;
			gap: var(--lv-space-sm);
			padding: var(--lv-space-md) var(--lv-space-lg) var(--lv-space-sm);
    }

    .toolbar {
      display: flex;
      min-width: 0;
      align-items: center;
    }

    .title {
      display: flex;
      min-width: 0;
      flex: 1;
      align-items: center;
			gap: var(--lv-space-sm);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .toolbar-actions {
      display: flex;
      align-items: center;
      gap: var(--lv-space-2xs);
    }

    .title svg,
    button svg,
    a svg {
      width: 16px;
      height: 16px;
    }

    button,
    a {
      display: inline-grid;
			width: var(--control-medium-size);
			height: var(--control-medium-size);
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
      text-decoration: none;
    }

    button:hover,
    button:focus-visible,
    a:hover,
    a:focus-visible {
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    a[aria-disabled="true"] { opacity: 0.5; cursor: wait; }

    button:disabled {
      color: var(--lv-fg-muted);
      cursor: not-allowed;
      opacity: 0.5;
    }

    button:disabled:hover {
      background: transparent;
    }

    .text-action { font: var(--lv-type-caption); width: auto; display: inline-flex; gap: var(--lv-space-xs); padding-inline: var(--lv-space-sm); }
    .welcome { box-sizing: border-box; min-width: 0; min-height: 0; overflow: auto; padding: var(--lv-space-lg) var(--lv-space-sm); display: flex; flex-direction: column; align-items: center; justify-content: safe center; gap: var(--lv-space-md); }
    .welcome-heading { display: flex; align-items: center; justify-content: center; gap: var(--lv-space-sm); text-align: center; }
    .welcome-heading .agent-mark { display: grid; place-items: center; color: var(--lv-accent); }
    .welcome-heading .agent-mark svg { width: var(--base-size-20); height: var(--base-size-20); }
    .welcome h2 { margin: 0; font: var(--lv-type-section-title); }
    .welcome lv-chat-composer { width: 100%; flex: 0 0 auto; }
    .prompts { display: flex; flex-wrap: wrap; justify-content: center; gap: var(--lv-space-sm); padding-inline: var(--lv-space-sm); }
    .prompt { display: inline-flex; width: auto; height: auto; min-height: var(--lv-control-medium); align-items: center; gap: var(--lv-space-xs); padding: 0 var(--lv-space-md); border: var(--lv-border-muted); border-radius: var(--lv-radius-full); background: var(--lv-bg-panel); color: var(--lv-fg-default); font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-medium); white-space: nowrap; }
    .prompt:hover { border-color: var(--lv-line-accent-muted); }
    .prompt svg { width: var(--base-size-16); height: var(--base-size-16); color: var(--lv-accent); }
    .welcome-hint { margin: 0; padding-inline: var(--lv-space-md); color: var(--lv-fg-muted); text-align: center; font: var(--lv-type-caption); }
    .welcome-hint kbd { display: inline-grid; min-width: 20px; height: 20px; place-items: center; border: var(--lv-border-muted); border-radius: var(--lv-radius-tight); background: var(--lv-bg-control); color: var(--lv-fg-default); font: inherit; }
    button:focus-visible, a:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-line-accent); outline-offset: var(--lv-space-2xs); }

    .close-action {
      margin-left: var(--lv-space-xs);
    }

    :host([embedded]:not([expanded])) .title,
    :host([embedded]) .close-action {
      display: none;
    }

    :host([embedded]) .toolbar {
      justify-content: flex-end;
    }

    :host([embedded]) .header {
      padding-block-start: var(--lv-space-sm);
    }

    :host([embedded]) .text-action {
      width: var(--control-medium-size);
      padding-inline: 0;
    }

    :host([embedded]) .text-action span {
      display: none;
    }

    .context {
      display: grid;
			gap: var(--lv-space-sm);
      border: 0;
			padding: 0;
      background: var(--lv-bg-app);
      font: var(--lv-type-caption);
    }

    .context-line {
      display: flex;
      min-width: 0;
      align-items: center;
			gap: var(--lv-space-xs);
    }

    .page-context {
      overflow: hidden;
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-medium);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .context-separator {
      color: var(--lv-fg-muted);
    }

    .filter-context {
      color: var(--lv-fg-muted);
      white-space: nowrap;
    }

    .reference-limit-status {
      color: var(--lv-fg-muted);
    }

    lv-chat-thread {
      display: block;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    lv-chat-thread[hidden] { display: none; }

    lv-chat-visual-panel {
      grid-row: 2 / 4;
      min-width: 0;
      min-height: 0;
      z-index: 1;
      background: var(--lv-bg-app);
    }

    lv-chat-composer {
      display: block;
      border-top: 0;
      background: transparent;
    }

    @media (max-width: 720px) {
      :host([open]) {
        position: fixed;
        inset: 0;
        z-index: var(--zIndex-modal, 200);
        width: 100vw;
        border-left: 0;
      }

      :host([open][embedded]:not([expanded])) {
        position: static;
        width: 100%;
        height: 100%;
      }

      .drawer {
        width: 100vw;
      }

      :host([embedded]) .drawer {
        width: 100%;
      }
    }

    @media (prefers-reduced-motion: reduce) {
      :host { transition: none; }
    }
`
