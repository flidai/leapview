import { css } from 'lit'

export const chatThreadStyles = css`
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
      height: 100%;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr);
      overflow: hidden;
	      background: var(--lv-bg-app);
    }

    .scroll {
      height: 100%;
      min-height: 0;
      overflow: auto;
      overscroll-behavior: contain;
      padding: var(--lv-chat-thread-padding);
    }

    .stack {
      display: grid;
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
      width: 100%;
      min-width: 0;
      max-width: min(var(--lv-chat-message-width), 100%);
    }

    .edited-label { display: block; margin-top: 4px; text-align: right; color: var(--lv-fg-muted); font-size: 12px; }
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

    .agent-stack {
      display: grid;
      gap: var(--lv-chat-agent-item-gap);
    }

    .agent-markdown {
      display: block;
    }

    .user .bubble {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-panel-muted);
    }

		.user-turn-bubble {
			display: grid;
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

    @media (max-width: 720px) {
      .scroll {
        padding: var(--lv-chat-thread-padding-compact);
      }
    }
  `
