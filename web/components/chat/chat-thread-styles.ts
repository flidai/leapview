import { css } from 'lit'

export const chatThreadStyles = css`
  .tool-actions { display: flex; flex-wrap: wrap; gap: 8px; margin: 8px 0 12px; }
  .tool-actions a { display: inline-flex; align-items: center; gap: 6px; padding: 6px 10px; border: var(--lv-border-default); border-radius: 6px; color: var(--lv-accent); font: var(--lv-type-caption); text-decoration: none; }
  .tool-actions a:hover { background: var(--lv-bg-control-hover); text-decoration: underline; }
  .tool-actions a:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 2px; }
  .artifact-actions { display:flex; gap:8px; margin:6px 0 12px; }
  .artifact-actions button { display:inline-flex; align-items:center; gap:6px; padding:5px 8px; font:inherit; font-size:12px; color:var(--lv-fg-muted); background:transparent; border:var(--lv-border-default); border-radius:6px; cursor:pointer; }
  .artifact-actions button:hover { background:var(--lv-bg-control-hover); color:var(--lv-fg-default); }
  .artifact-actions button:disabled { opacity:.6; cursor:default; }
  .artifact-actions svg { width:14px; height:14px; }

  .message-attachment { margin-top: 8px; text-align: left; font: var(--lv-type-caption); }
  .message-attachment summary { cursor: pointer; }
  .message-attachment summary svg { width: 14px; height: 14px; vertical-align: middle; }
  .message-attachment pre { max-height: 240px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; }
    :host {
      box-sizing: border-box;
      display: block;
      height: 100%;
      min-height: 0;
      overflow: hidden;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    *,
    *::before,
    *::after {
      box-sizing: inherit;
    }

    .thread {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      height: 100%;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr);
      overflow: hidden;
	      background: var(--lv-bg-app);
    }

    .scroll {
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: auto;
      overscroll-behavior: contain;
      padding: var(--lv-chat-thread-padding);
    }

    .stack {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      min-width: 0;
      width: min(100%, var(--lv-chat-stack-width));
      margin-inline: auto;
      gap: var(--lv-chat-stack-gap);
    }

    .alert {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      border-color: var(--lv-line-danger-muted);
      background: var(--lv-bg-danger-muted);
      padding: var(--lv-chat-thread-padding);
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      text-align: left;
    }

    .stack.is-empty {
      min-height: 100%;
      place-content: center;
    }

    .empty-state {
      display: grid;
      justify-items: center;
      gap: var(--lv-space-sm);
      padding: var(--lv-space-lg);
      color: var(--lv-fg-muted);
      text-align: center;
    }

    .empty-icon {
      display: grid;
      width: var(--base-size-40);
      height: var(--base-size-40);
      place-items: center;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
    }

    .empty-icon svg {
      width: var(--base-size-20);
      height: var(--base-size-20);
    }

    .empty-title {
      color: var(--lv-fg-default);
      font: var(--lv-type-section-title);
    }

    .empty-detail {
      max-width: 24rem;
      font: var(--lv-type-secondary);
    }

    .working {
      display: inline-flex;
      width: fit-content;
      align-items: center;
      gap: var(--lv-space-sm);
      color: var(--lv-fg-muted);
      font: var(--lv-type-secondary);
    }

    .working-dots {
      display: inline-flex;
      gap: var(--lv-space-2xs);
    }

    .working-dots i {
      width: var(--base-size-4);
      height: var(--base-size-4);
      border-radius: 50%;
      background: currentColor;
      animation: working-pulse 1.2s ease-in-out infinite;
    }

    .working-dots i:nth-child(2) { animation-delay: 120ms; }
    .working-dots i:nth-child(3) { animation-delay: 240ms; }

    .message {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr);
      max-width: min(var(--lv-chat-message-width), 100%);
    }

    .message.user {
      justify-self: end;
    }

    .agent-turn,
    .message.error {
      justify-self: start;
    }

    .label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .bubble {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--lv-chat-bubble-padding-block) var(--lv-chat-bubble-padding-inline);
      font: var(--lv-type-body);
      line-height: var(--base-text-lineHeight-relaxed);
      overflow-wrap: anywhere;
    }

    .bubble.plain {
      white-space: pre-wrap;
    }

    .agent-turn {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      width: 100%;
      min-width: 0;
      max-width: min(var(--lv-chat-message-width), 100%);
    }

    .edited-label { display: block; margin-top: 4px; text-align: right; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .message-actions { display: flex; align-items: center; gap: 4px; min-height: 28px; margin-top: 6px; color: var(--lv-fg-muted); }
    .user .message-actions { justify-content: flex-end; opacity: 0; }
    .user:hover .message-actions, .user:focus-within .message-actions { opacity: 1; }
    .message-actions button { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: var(--lv-radius-default); background: transparent; color: inherit; cursor: pointer; }
    .message-actions button:hover { background: var(--lv-bg-panel-muted); color: var(--lv-fg-default); }
    .message-actions button:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
    .message-actions button:disabled { opacity: .4; cursor: default; }
    .copy-confirmation { font: var(--lv-type-caption); }
    .copy-error { color: var(--lv-fg-danger); font: var(--lv-type-caption); padding: 8px; }
    @media (hover: none) { .user .message-actions { opacity: 1; } .message-actions button { width: 36px; height: 36px; } }

    .run-activity { color: var(--lv-fg-muted); font-size: var(--lv-chat-tool-font-size, 12px); }
    .run-activity > summary { display: flex; align-items: center; gap: 6px; width: fit-content; cursor: pointer; list-style: none; padding: 4px 0; }
    .run-activity > summary::-webkit-details-marker { display: none; }
    .run-activity[open] > summary svg { transform: rotate(90deg); }
    .run-activity > summary:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; border-radius: 4px; }
    .run-activity-steps { display: grid; gap: 8px; padding: 8px 0; }

    .agent-stack {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr);
      gap: var(--lv-chat-agent-item-gap);
    }

    .agent-markdown {
      display: block;
      min-width: 0;
    }

    .user .bubble {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-panel-muted);
    }

		.user-turn-bubble {
			display: grid;
			min-width: 0;
			grid-template-columns: minmax(0, 1fr);
			gap: var(--lv-space-sm);
		}

		.turn-references {
			display: flex;
			flex-wrap: wrap;
			gap: var(--lv-space-2xs);
		}

		.turn-reference {
			display: inline-flex;
			width: fit-content;
			max-width: 100%;
			min-width: 0;
			align-items: center;
			gap: var(--lv-space-xs);
			border-radius: var(--lv-radius-default);
			background: var(--lv-bg-control);
			color: var(--lv-fg-default);
			padding: var(--lv-space-xs) var(--lv-space-sm);
			text-decoration: none;
			white-space: nowrap;
		}

		.turn-reference:hover,
		.turn-reference:focus-visible {
			background: var(--lv-bg-control-hover);
			outline: 0;
		}

		.turn-reference-icon,
		.turn-reference-icon svg {
			width: 14px;
			height: 14px;
		}

		.turn-reference-name {
			min-width: 0;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.turn-message-text {
			white-space: pre-wrap;
		}

    :host([surface='drawer']) .thread {
      background: var(--lv-bg-app);
    }

    :host([surface='drawer']) .scroll {
      padding: var(--lv-space-lg) calc(var(--lv-space-lg) + var(--lv-space-xs)) var(--lv-space-sm);
    }

    :host([surface='drawer']) .user .bubble {
      border-color: transparent;
      border-radius: var(--lv-radius-large);
      padding: var(--lv-space-sm) var(--lv-space-md);
    }

    .message.error .bubble {
      border-color: var(--lv-line-danger-muted);
      background: var(--lv-bg-danger-muted);
    }

    .tool-call {
      display: grid;
      width: fit-content;
      max-width: 100%;
      margin-block: var(--lv-chat-agent-tool-gap);
      gap: var(--lv-space-sm);
    }

    .tool-call.has-artifact {
      width: min(100%, 48rem);
    }

    .tool-trigger {
      display: inline-flex;
      width: fit-content;
      max-width: 100%;
      align-items: center;
      gap: var(--lv-chat-activity-gap, var(--lv-space-sm));
      border: 0;
      border-radius: var(--lv-radius-tight, var(--lv-radius-default));
      background: transparent;
      padding: var(--lv-chat-activity-padding-block) 0;
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      line-height: var(--base-text-lineHeight-snug);
      text-align: left;
      transition: color var(--lv-transition-fast);
    }

    .tool-icon {
      display: inline-flex;
      width: var(--lv-chat-activity-icon-size);
      height: var(--lv-chat-activity-icon-size);
      flex: 0 0 var(--lv-chat-activity-icon-size);
      color: currentColor;
    }

    .tool-icon svg,
    .tool-chevron svg {
      display: block;
      width: 100%;
      height: 100%;
      fill: none;
      stroke: currentColor;
      stroke-width: 1.8;
      stroke-linecap: round;
      stroke-linejoin: round;
    }

    .tool-call.running .tool-trigger {
      color: var(--lv-fg-warning);
    }

    .tool-call.running .tool-icon {
      animation: pulse 1.1s ease-in-out infinite;
    }

    .tool-call.error .tool-trigger {
      color: var(--lv-fg-danger);
    }

    .tool-trigger:hover,
    .tool-trigger:focus-visible {
      color: var(--lv-fg-default);
    }

    .tool-call.error .tool-trigger:hover,
    .tool-call.error .tool-trigger:focus-visible {
      color: var(--lv-fg-danger);
    }

    .tool-trigger:focus-visible {
      outline: var(--lv-border-width-focus) solid var(--lv-line-emphasis, var(--lv-line-accent));
      outline-offset: var(--lv-space-xs);
    }

    .activity-text {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .tool-status {
      color: var(--lv-fg-muted);
      font-weight: var(--base-text-weight-normal);
      white-space: nowrap;
    }

    .tool-chevron {
      display: inline-flex;
      width: var(--lv-chat-activity-icon-size);
      height: var(--lv-chat-activity-icon-size);
      flex: 0 0 var(--lv-chat-activity-icon-size);
      opacity: 0;
      transform: translateX(calc(-1 * var(--lv-space-xs)));
      transition: opacity var(--lv-transition-fast), transform var(--lv-transition-fast);
    }

    .tool-trigger:hover .tool-chevron,
    .tool-trigger:focus-visible .tool-chevron,
    .tool-trigger[aria-expanded='true'] .tool-chevron {
      opacity: 1;
      transform: translateX(0);
    }

    .tool-trigger[aria-expanded='true'] .tool-chevron {
      transform: rotate(90deg);
    }

    .tool-details {
      display: grid;
      max-width: min(42rem, 100%);
      gap: var(--lv-space-md);
      border-left: var(--lv-border-width-focus) solid var(--lv-line-muted);
      padding-left: var(--lv-space-lg);
      color: var(--lv-fg-muted);
      font: var(--lv-type-secondary);
      animation: tool-details-open var(--lv-transition-normal);
      transform-origin: top left;
    }

    .tool-detail-block {
      display: grid;
      gap: var(--lv-space-xs);
    }

    .tool-detail-label {
      color: var(--lv-fg-muted);
      font-weight: var(--base-text-weight-medium);
    }

    .tool-detail-block lv-code-block {
      max-width: 100%;
    }

    .tool-error {
      color: var(--lv-fg-danger);
      white-space: pre-wrap;
    }

    .tool-empty {
      color: var(--lv-fg-muted);
    }

    .page-visuals { display: grid; gap: 8px; min-width: 0; }
    .page-visuals h3 { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .visual-reference {
      display: flex; align-items: center; gap: 10px; width: 100%; min-width: 0;
      padding: 12px; border: var(--lv-border-default); border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel); color: var(--lv-fg-default); text-align: left; cursor: pointer;
      font: var(--lv-type-body-compact);
    }
    .visual-reference:hover { background: var(--lv-bg-control-hover); }
    .visual-reference:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 2px; }
    .visual-reference[aria-pressed='true'] { border-color: var(--lv-accent); }
    .visual-reference-icon { display: grid; place-items: center; width: 32px; height: 32px; flex: 0 0 32px; border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); color: var(--lv-accent); }
    .visual-reference-icon svg, .visual-reference-chevron svg { width: 16px; height: 16px; }
    .visual-reference-copy { display: grid; min-width: 0; gap: 3px; flex: 1; }
    .visual-reference-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .visual-reference-hint { font: var(--lv-type-caption); color: var(--lv-fg-muted); }
    .visual-reference-chevron { color: var(--lv-fg-muted); }

    .dashboard-preview-link {
      display: inline-flex; align-items: center; gap: 8px; max-width: 100%;
      margin-top: 8px; padding: 8px 10px; border: var(--lv-border-default);
      border-radius: var(--lv-radius-default); background: var(--lv-bg-panel);
      color: var(--lv-fg-default); font: var(--lv-type-body-compact); text-align: left; cursor: pointer;
    }
    .dashboard-preview-link svg { flex-shrink: 0; width: 16px; height: 16px; color: var(--lv-accent); }
    .dashboard-preview-link:hover { background: var(--lv-bg-control-hover); }
    .dashboard-preview-link:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 2px; }

    lv-visual-artifact {
      display: block;
      width: 100%;
      min-width: 0;
      overflow: hidden;
    }

    lv-visual-artifact:not([type='table']):not([type='matrix']):not([type='pivot']) {
      height: 18rem;
    }

    lv-visual-artifact:is([type='table'], [type='matrix'], [type='pivot']) {
      height: 22rem;
    }

    @keyframes working-pulse {
      0%, 60%, 100% { opacity: 0.35; transform: translateY(0); }
      30% { opacity: 1; transform: translateY(-2px); }
    }

    @keyframes tool-details-open {
      from {
        opacity: 0;
        transform: translateY(calc(-1 * var(--lv-chat-tool-disclosure-offset)));
      }
      to {
        opacity: 1;
        transform: translateY(0);
      }
    }

    @keyframes pulse {
      0%, 100% { opacity: 0.45; }
      50% { opacity: 1; }
    }

    @media (max-width: 720px) {
      .scroll {
        padding: var(--lv-chat-thread-padding-compact);
      }
    }
  `
