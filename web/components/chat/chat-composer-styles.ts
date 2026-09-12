import { css } from 'lit'

export const chatComposerStyles = css`
    :host {
      position: relative;
      display: block;
      background: linear-gradient(to bottom, transparent, var(--lv-bg-app) var(--lv-space-lg));
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    form {
			position: relative;
      width: min(calc(100% - var(--lv-space-lg) - var(--lv-space-lg)), var(--lv-chat-stack-width));
      margin-inline: auto;
      padding: calc(var(--lv-space-lg) + var(--lv-space-sm)) var(--lv-space-lg) var(--lv-space-lg);
    }

		.edit-banner {
			display: flex;
			align-items: center;
			justify-content: space-between;
			gap: var(--lv-space-sm);
			margin-bottom: var(--lv-space-sm);
			color: var(--lv-fg-muted);
			font: var(--lv-type-caption);
		}

		.cancel-edit {
			border: 0;
			background: transparent;
			color: var(--lv-fg-accent);
			font: inherit;
			cursor: pointer;
			padding: var(--lv-space-2xs) var(--lv-space-xs);
		}

		.cancel-edit:hover:not(:disabled),
		.cancel-edit:focus-visible {
			color: var(--lv-fg-default);
			text-decoration: underline;
		}

		.cancel-edit:disabled {
			color: var(--lv-fg-muted);
			cursor: not-allowed;
			opacity: var(--opacity-disabled);
		}

    .composer-surface {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: end;
      gap: var(--lv-space-sm);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      padding: var(--lv-space-sm);
      box-shadow: none;
      cursor: text;
      transition:
        background var(--lv-transition-fast),
        border-color var(--lv-transition-fast),
        box-shadow var(--lv-transition-fast);
    }

    .composer-surface:hover:not(.is-disabled) {
      border-color: var(--lv-line-muted);
      box-shadow: none;
    }

    .composer-surface:focus-within {
      border-color: var(--lv-line-accent-muted);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-bg-accent-muted);
    }

    .composer-surface.is-disabled {
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      box-shadow: none;
      cursor: not-allowed;
    }

    textarea {
      box-sizing: border-box;
      min-height: calc(var(--lv-control-large) + var(--lv-space-sm));
      max-height: 160px;
      width: 100%;
      grid-column: 1;
      grid-row: 1;
      resize: none;
      overflow-y: auto;
      border: 0;
      border-radius: calc(var(--lv-radius-default) - var(--lv-space-2xs));
      background: transparent;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      padding: var(--lv-space-xs) var(--lv-space-sm);
      outline: 0;
    }

    textarea:focus {
      outline: 0;
    }

    textarea::placeholder {
      color: var(--lv-fg-muted);
    }

    .actions {
      display: flex;
      grid-column: 2;
      grid-row: 1;
      min-height: var(--lv-control-medium);
      align-items: center;
      gap: var(--lv-space-xs);
      justify-content: flex-end;
    }

    .context-button {
      display: inline-grid;
      width: var(--lv-button-height, var(--lv-control-medium));
      height: var(--lv-button-height, var(--lv-control-medium));
      min-width: var(--lv-button-height, var(--lv-control-medium));
      place-items: center;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
    }

    .context-button svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .context-button:hover:not(:disabled),
    .context-button:focus-visible {
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
    }

    .context-button:focus-visible {
      outline: var(--focus-outline, var(--lv-border-default));
      outline-offset: var(--focus-outline-offset, var(--lv-space-xs));
    }

    .context-button:disabled {
      color: var(--lv-fg-muted);
      cursor: not-allowed;
      opacity: var(--opacity-disabled);
    }

		.send-button.is-editing {
			width: auto;
			min-width: var(--lv-button-height, var(--lv-control-medium));
			padding-inline: var(--lv-space-sm);
			gap: var(--lv-space-xs);
		}

		.stop-button {
			border-color: var(--lv-line-danger, var(--lv-fg-danger));
			background: var(--lv-fg-danger);
			color: var(--lv-fg-on-emphasis);
		}

		.stop-button:hover:not(:disabled) {
			border-color: var(--lv-line-danger);
			background: var(--lv-fg-danger);
		}

		.continuation-action {
			display: flex;
			justify-content: flex-end;
			margin-top: var(--lv-space-xs);
		}

		.continue-button {
			border: 0;
			border-radius: var(--lv-radius-default);
			background: transparent;
			color: var(--lv-fg-accent);
			cursor: pointer;
			font: var(--lv-type-caption);
			padding: var(--lv-space-xs) var(--lv-space-sm);
		}

		.continue-button:hover:not(:disabled),
		.continue-button:focus-visible {
			background: var(--lv-bg-control-hover);
			color: var(--lv-fg-default);
		}

		.continue-button:focus-visible {
			outline: var(--focus-outline, var(--lv-border-default));
			outline-offset: var(--focus-outline-offset, var(--lv-space-2xs));
		}

		.continue-button:disabled {
			color: var(--lv-fg-muted);
			cursor: not-allowed;
			opacity: var(--opacity-disabled);
		}

    :host([hide-context-action]) .context-button {
      display: none;
    }

		.mention-picker {
			display: grid;
			position: absolute;
			inset: auto var(--lv-space-lg) calc(100% - var(--lv-space-lg) - var(--lv-space-sm));
			z-index: var(--zIndex-dropdown);
			max-height: 180px;
			overflow: auto;
			border: var(--lv-border-muted);
			border-radius: var(--lv-radius-large);
			background: var(--lv-bg-panel);
			padding: var(--lv-space-xs);
			box-shadow: var(--lv-shadow-floating-sm);
		}

		.mention-option {
			display: grid;
			width: 100%;
			height: auto;
			min-height: var(--lv-control-small);
			grid-template-columns: 16px minmax(0, 1fr);
			align-items: center;
			gap: var(--lv-space-xs);
			border: 0;
			border-radius: var(--lv-radius-default);
			background: transparent;
			color: var(--lv-fg-default);
			padding: var(--lv-space-2xs) var(--lv-space-sm);
			box-shadow: none;
			text-align: left;
		}

		.mention-group {
			display: grid;
		}

		.mention-section-label {
			min-width: 0;
			padding: var(--lv-space-xs) var(--lv-space-sm) var(--lv-space-2xs);
			color: var(--lv-fg-muted);
			font: var(--lv-type-caption);
		}

		.mention-icon {
			display: grid;
			width: 16px;
			height: 16px;
			place-items: center;
			color: var(--lv-fg-muted);
		}

		.mention-icon svg {
			width: 14px;
			height: 14px;
		}

		.mention-copy {
			display: grid;
			min-width: 0;
			align-items: baseline;
			grid-template-columns: minmax(0, 2fr) minmax(0, 3fr) 88px;
			gap: var(--lv-space-sm);
		}

		.mention-title,
		.mention-hierarchy,
		.mention-type {
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.mention-hierarchy,
		.mention-type {
			min-width: 0;
			color: var(--lv-fg-muted);
			font: var(--lv-type-caption);
		}

		.mention-type {
			text-align: right;
		}

		.mention-status {
			display: flex;
			min-height: var(--lv-control-small);
			align-items: center;
			gap: var(--lv-space-sm);
			padding: var(--lv-space-2xs) var(--lv-space-sm);
			color: var(--lv-fg-muted);
			font: var(--lv-type-caption);
		}

		.mention-status svg {
			width: 14px;
			height: 14px;
		}

		.selected-references {
			display: flex;
			grid-column: 1 / -1;
			grid-row: 1;
			flex-wrap: wrap;
			gap: var(--lv-space-xs);
			padding: var(--lv-space-xs) var(--lv-space-sm) 0;
		}

		.reference-chip {
			display: inline-flex;
			width: auto;
			height: 24px;
			max-width: 100%;
			align-items: center;
			gap: var(--lv-space-xs);
			border: 0;
			border-radius: var(--lv-radius-full);
			background: var(--lv-bg-control);
			color: var(--lv-fg-default);
			padding: 0 var(--lv-space-sm);
			font: var(--lv-type-caption);
			cursor: pointer;
		}

		.reference-chip svg {
			width: 12px;
			height: 12px;
		}

		.composer-surface:has(.selected-references) textarea,
		.composer-surface:has(.selected-references) .actions {
			grid-row: 2;
		}

		.mention-option[data-active='true'],
		.mention-option:hover {
			background: var(--lv-bg-control-hover);
			transform: none;
		}

    .send-button {
      display: inline-flex;
      width: var(--lv-button-height, var(--lv-control-medium));
      height: var(--lv-button-height, var(--lv-control-medium));
      min-width: var(--lv-button-height, var(--lv-control-medium));
      align-items: center;
      justify-content: center;
      border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-accent-border-rest, var(--lv-accent));
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      background: var(--lv-button-accent-bg-rest, var(--lv-accent));
      color: var(--lv-button-accent-fg-rest, var(--lv-accent-fg));
      cursor: pointer;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      padding: 0;
      box-shadow: var(--lv-button-shadow-resting, var(--shadow-resting-small));
      transition:
        background var(--duration-fast) var(--ease-lv),
        border-color var(--duration-fast) var(--ease-lv),
        color var(--duration-fast) var(--ease-lv),
        transform var(--duration-fast) var(--ease-lv);
    }

    .send-button svg {
      width: var(--lv-button-icon-size, var(--base-size-16));
      height: var(--lv-button-icon-size, var(--base-size-16));
    }

    .send-button:hover:not(:disabled) {
      border-color: var(--lv-button-accent-border-hover, var(--lv-accent));
      background: var(--lv-button-accent-bg-hover, var(--lv-accent));
      transform: translateY(-1px);
    }

    .send-button:focus-visible {
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: var(--focus-outline-offset, var(--lv-space-xs));
    }

    .send-button:disabled {
      border-color: var(--lv-button-accent-border-disabled, var(--lv-line-default));
      background: var(--lv-button-accent-bg-disabled, var(--lv-bg-control));
      color: var(--lv-button-accent-fg-disabled, var(--lv-fg-muted));
      cursor: not-allowed;
      opacity: 1;
      box-shadow: none;
    }

    textarea:disabled {
      cursor: not-allowed;
      color: var(--lv-fg-muted);
      opacity: 1;
    }
    @media (max-width: 560px) {
      form {
        width: min(calc(100% - var(--lv-space-md) - var(--lv-space-md)), var(--lv-chat-stack-width));
        padding: calc(var(--lv-space-lg) + var(--lv-space-sm)) var(--lv-space-md) var(--lv-space-md);
      }
    }

    @media (pointer: coarse) {
      .actions {
        min-height: calc(var(--lv-control-large) + var(--lv-space-xs));
      }

      .context-button,
      .send-button {
        --lv-button-height: calc(var(--lv-control-large) + var(--lv-space-xs));
      }
    }
  `
