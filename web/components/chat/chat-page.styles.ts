import { css } from 'lit'

export const chatPageStyles = css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      background: var(--lv-bg-app);
    }

    .route {
      display: block;
      min-height: 100svh;
      background: var(--lv-bg-app);
    }

    .route.visual-open {
      display: grid;
      height: 100svh;
      grid-template-columns: minmax(0, 1fr) minmax(22rem, 55%);
      overflow: hidden;
    }
    lv-chat-visual-panel { min-width: 0; min-height: 0; }
    .route.dashboard-open {
      display: grid;
      height: 100svh;
      grid-template-columns: minmax(0, 1fr) minmax(22rem, 55%);
      overflow: hidden;
    }
    .route.dashboard-workspace {
      display: grid;
      height: 100svh;
      grid-template-columns: minmax(0, 1fr) clamp(20rem, 28vw, 26rem);
      overflow: hidden;
    }
    lv-chat-dashboard-draft { min-width: 0; min-height: 0; }
    .dashboard-workspace > lv-chat-dashboard-draft { grid-column: 1; grid-row: 1; }
    .dashboard-workspace > .main { grid-column: 2; grid-row: 1; border-left: var(--lv-border-muted); }
    .dashboard-workspace .thread-stack { --lv-chat-stack-width: 100%; }
    .return-chat { display: inline-flex; align-items: center; justify-content: center; width: var(--lv-control-medium); height: var(--lv-control-medium); border-radius: var(--lv-radius-default); color: var(--lv-fg-muted); text-decoration: none; }
    .return-chat:hover { background: var(--lv-bg-control-hover); }
    .return-chat:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-line-accent); outline-offset: var(--base-size-2); }
    .main {
      display: grid;
      min-width: 0;
      height: 100svh;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
      background: var(--lv-bg-app);
    }

    .main.list-main {
      height: auto;
      min-height: 100svh;
      grid-template-rows: minmax(0, 1fr);
      overflow: visible;
    }

    .main.new-main {
      grid-template-rows: minmax(0, 1fr);
    }

    .main.new-main.with-return { grid-template-rows: auto minmax(0, 1fr); }

    .loading-state {
      display: grid;
      place-items: center;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .conversation-titlebar {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      padding: 14px var(--base-size-16) var(--base-size-8);
    }

    .mobile-dashboard-toggle {
      display: inline-flex;
      min-height: var(--lv-control-medium);
      align-items: center;
      justify-self: start;
      gap: var(--base-size-8);
      border: var(--lv-border-muted);
      border-radius: 999px;
      background: var(--lv-bg-panel);
      padding: 0 var(--base-size-12);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-secondary);
    }

    .mobile-dashboard-count {
      display: inline-grid;
      min-width: 1.25rem;
      min-height: 1.25rem;
      place-items: center;
      border-radius: 999px;
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
      font: var(--lv-type-caption);
    }

    h1 {
      margin: 0;
    }

    h1 {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
    }

    .body {
      display: grid;
      min-width: 0;
      min-height: 0;
      overflow: auto;
      background: var(--lv-bg-app);
    }

    .list-main .body {
      min-height: auto;
      overflow: visible;
    }

    .thread-stack {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr) auto;
      overflow: hidden;
      background: var(--lv-bg-app);
    }

    .dashboard-destination { display: flex; align-items: center; flex-wrap: wrap; gap: var(--base-size-8); margin: 0 var(--base-size-16); font: var(--lv-type-secondary); color: var(--lv-fg-muted); }
    .dashboard-destination a { color: var(--lv-fg-accent); }
    .dashboard-destination button { margin-left: auto; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); padding: var(--base-size-4) var(--base-size-8); color: var(--lv-fg-default); background: var(--lv-bg-control); cursor: pointer; }

    .new-chat-stage {
      box-sizing: border-box;
      display: flex;
      min-width: 0;
      min-height: 100%;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: var(--lv-space-md);
      overflow-y: auto;
      padding: calc(var(--lv-space-lg) * 3) 0 var(--lv-space-lg);
      background: var(--lv-bg-app);
    }

    .new-chat-stage > * {
      animation: new-chat-enter var(--lv-transition-medium) both;
    }

    .new-chat-stage lv-chat-composer {
      width: 100%;
      animation-delay: 70ms;
    }

    .new-chat-intro {
      box-sizing: border-box;
      width: min(100%, var(--lv-chat-stack-width));
      padding-inline: var(--lv-space-lg);
    }

    .new-chat-heading {
      display: flex;
      align-items: center;
      justify-content: center;
      gap: var(--lv-space-sm);
      text-align: center;
    }

    .agent-mark {
      display: grid;
      width: var(--base-size-24);
      height: var(--base-size-24);
      flex: 0 0 var(--base-size-24);
      place-items: center;
      color: var(--lv-accent);
    }

    .agent-mark svg {
      width: var(--base-size-20);
      height: var(--base-size-20);
    }

    .new-chat-title {
      max-width: 100%;
      font: var(--lv-type-page-title);
    }

    .prompt-starters {
      box-sizing: border-box;
      display: flex;
      width: min(100%, var(--lv-chat-stack-width));
      flex-wrap: wrap;
      justify-content: center;
      gap: var(--lv-space-sm);
      padding-inline: var(--lv-space-lg);
    }

    .prompt-starter {
      display: inline-flex;
      min-height: var(--lv-control-medium);
      align-items: center;
      gap: var(--lv-space-xs);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      padding: 0 var(--lv-space-md);
      cursor: pointer;
      transition:
        background var(--lv-transition-fast),
        border-color var(--lv-transition-fast);
    }

    .prompt-starter:hover:not(:disabled) {
      border-color: var(--lv-line-accent-muted);
      background: var(--lv-bg-control-hover);
    }

    .prompt-starter:focus-visible {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent);
      outline-offset: var(--lv-space-2xs);
    }

    .prompt-starter:disabled {
      color: var(--lv-fg-muted);
      cursor: not-allowed;
      opacity: 0.65;
    }

    .prompt-starter-icon {
      display: grid;
      width: var(--base-size-16);
      height: var(--base-size-16);
      place-items: center;
      color: var(--lv-accent);
    }

    .prompt-starter-icon svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .prompt-starter-label {
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
    }

    .new-chat-context-hint {
      margin: 0;
      padding-inline: var(--lv-space-lg);
      color: var(--lv-fg-muted);
      text-align: center;
      font: var(--lv-type-caption);
    }

    .new-chat-context-hint kbd {
      display: inline-grid;
      min-width: 20px;
      height: 20px;
      place-items: center;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-tight);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      font: inherit;
    }

    @keyframes new-chat-enter {
      from {
        opacity: 0;
        transform: translateY(var(--lv-space-sm));
      }

      to {
        opacity: 1;
        transform: translateY(0);
      }
    }

    @media (prefers-reduced-motion: reduce) {
      .new-chat-stage > * {
        animation: none;
      }
    }

    lv-chat-thread {
      display: block;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    lv-chat-composer {
      display: block;
      background: var(--lv-bg-app);
    }

    @media (max-width: 768px) {
      .route {
        grid-template-columns: 1fr;
      }

      .route.visual-open { grid-template-columns: minmax(0, 1fr); }
      .route.visual-open lv-chat-visual-panel {
        position: fixed;
        z-index: 20;
        inset: 0;
        background: var(--lv-bg-panel);
      }
      .route.dashboard-open { grid-template-columns: minmax(0, 1fr); }
      .dashboard-workspace > .main { grid-column: 1; border-left: 0; }
      .route.dashboard-open lv-chat-dashboard-draft {
        position: fixed;
        z-index: 20;
        inset: 0;
        background: var(--lv-bg-panel);
      }
      .conversation-titlebar:has(.mobile-dashboard-toggle) { gap: var(--base-size-8); }
      .mobile-dashboard-toggle { display: inline-flex; }
      .main.new-main {
        height: 100svh;
      }

      .new-chat-stage {
        justify-content: flex-start;
        padding-top: calc(var(--lv-space-lg) * 2);
      }

      .prompt-starters { gap: var(--lv-space-xs); }
    }
`
