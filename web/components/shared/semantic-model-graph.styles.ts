export const NODE_WIDTH = 280
export const HEADER_HEIGHT = 40
export const FIELD_HEIGHT = 28

export const semanticModelGraphStyles = `
  lv-semantic-model-graph,
  lv-semantic-model-graph .semantic-model-graph-root,
  lv-semantic-model-graph .semantic-model-graph-layout {
    display: block;
    height: 100%;
    min-width: 0;
    min-height: 0;
  }

  lv-semantic-model-graph .semantic-model-graph-layout {
    background:
      linear-gradient(var(--lv-bg-page, var(--lv-bg-app)), var(--lv-bg-page, var(--lv-bg-app))),
      radial-gradient(circle at 1px 1px, color-mix(in srgb, var(--lv-fg-muted), transparent 88%) 1px, transparent 0);
    background-size: auto, 20px 20px;
    outline: 0;
  }

  lv-semantic-model-graph .react-flow {
    position: relative;
    overflow: hidden;
    width: 100%;
    height: 100%;
    color: var(--lv-fg-default);
    background-color: transparent;
  }

  lv-semantic-model-graph .react-flow__container,
  lv-semantic-model-graph .react-flow__renderer,
  lv-semantic-model-graph .react-flow__viewport,
  lv-semantic-model-graph .react-flow__pane,
  lv-semantic-model-graph .react-flow__nodes,
  lv-semantic-model-graph .react-flow .react-flow__edges,
  lv-semantic-model-graph .react-flow .react-flow__edges svg {
    position: absolute;
  }

  lv-semantic-model-graph .react-flow__container,
  lv-semantic-model-graph .react-flow__viewport {
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    transform-origin: 0 0;
  }

  lv-semantic-model-graph .react-flow__node {
    position: absolute;
    box-sizing: border-box;
    pointer-events: all;
    transform-origin: 0 0;
    user-select: none;
  }

  lv-semantic-model-graph .react-flow__nodes {
    pointer-events: none;
  }

  lv-semantic-model-graph .react-flow .react-flow__edges svg {
    overflow: visible;
    pointer-events: none;
  }

  lv-semantic-model-graph .react-flow__edge-path,
  lv-semantic-model-graph .react-flow__connection-path {
    fill: none;
  }

  lv-semantic-model-graph .semantic-model-relationship-path {
    pointer-events: visibleStroke;
    cursor: pointer;
  }

  lv-semantic-model-graph .react-flow__edgelabel-renderer {
    position: absolute;
    width: 100%;
    height: 100%;
    pointer-events: none;
    user-select: none;
  }

  lv-semantic-model-graph .react-flow__background {
    pointer-events: none;
    z-index: -1;
  }

  lv-semantic-model-graph .react-flow__handle {
    position: absolute;
    width: 8px;
    height: 8px;
    min-width: 8px;
    min-height: 8px;
    border: 1px solid var(--lv-bg-panel);
    border-radius: 50%;
    background: var(--lv-fg-muted);
    pointer-events: none;
  }

  lv-semantic-model-graph .react-flow__handle-left {
    left: 0;
    transform: translate(-50%, -50%);
  }

  lv-semantic-model-graph .react-flow__handle-right {
    right: 0;
    transform: translate(50%, -50%);
  }

  lv-semantic-model-graph .react-flow__panel {
    position: absolute;
    z-index: 5;
    margin: var(--base-size-16);
  }

  lv-semantic-model-graph .react-flow__panel.left {
    left: 0;
  }

  lv-semantic-model-graph .react-flow__panel.bottom {
    bottom: 0;
  }

  lv-semantic-model-graph .react-flow__controls {
    display: flex;
    flex-direction: column;
    border: var(--lv-border-default);
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-resting-small, none);
  }

  lv-semantic-model-graph .react-flow__controls-button {
    display: flex;
    width: 26px;
    height: 26px;
    align-items: center;
    justify-content: center;
    border: 0;
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    padding: 4px;
    cursor: pointer;
  }

  lv-semantic-model-graph .react-flow__controls-button svg {
    width: 100%;
    max-width: 12px;
    max-height: 12px;
    fill: currentColor;
  }

  lv-semantic-model-graph .semantic-model-layout-actions {
    display: flex;
    align-items: center;
    gap: var(--base-size-6);
  }

  lv-semantic-model-graph .semantic-model-fields-control,
  lv-semantic-model-graph .semantic-model-reset-button {
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-tight);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    font: var(--lv-type-caption);
  }

  lv-semantic-model-graph .semantic-model-fields-control {
    display: inline-flex;
    min-height: 28px;
    align-items: center;
    overflow: hidden;
  }

  lv-semantic-model-graph .semantic-model-fields-label {
    color: var(--lv-fg-muted);
    padding: 0 var(--base-size-8);
  }

  lv-semantic-model-graph .semantic-model-fields-option {
    align-self: stretch;
    border: 0;
    border-left: var(--lv-border-muted);
    background: transparent;
    color: var(--lv-fg-muted);
    padding: 0 var(--base-size-8);
    font: inherit;
    cursor: pointer;
  }

  lv-semantic-model-graph .semantic-model-fields-option[aria-pressed='true'] {
    background: var(--lv-bg-control-active, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-semibold);
  }

  lv-semantic-model-graph .semantic-model-reset-button {
    display: inline-flex;
    width: 28px;
    height: 28px;
    align-items: center;
    justify-content: center;
    padding: 0;
  }

  lv-semantic-model-graph .semantic-model-fields-option:hover,
  lv-semantic-model-graph .semantic-model-fields-option:focus-visible,
  lv-semantic-model-graph .semantic-model-reset-button:hover,
  lv-semantic-model-graph .semantic-model-reset-button:focus-visible {
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    outline: 0;
  }

  lv-semantic-model-graph .semantic-model-relationship-inspector {
    display: grid;
    max-width: min(520px, calc(100% - 32px));
    gap: var(--base-size-4);
    border: var(--lv-border-default);
    border-radius: var(--borderRadius-default);
    background: color-mix(in srgb, var(--lv-bg-panel), transparent 4%);
    box-shadow: var(--shadow-resting-small, none);
    color: var(--lv-fg-muted);
    padding: var(--base-size-8) var(--base-size-10);
    font: var(--lv-type-caption);
    pointer-events: none;
  }

  lv-semantic-model-graph .semantic-model-relationship-inspector strong {
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-semibold);
  }

  lv-semantic-model-graph .semantic-model-relationship-fields {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-code-inline);
  }

  lv-semantic-model-graph .semantic-model-reset-icon {
    display: block;
    width: 14px;
    height: 14px;
  }

  lv-semantic-model-graph .react-flow__attribution {
    display: none;
  }

  lv-semantic-model-graph .semantic-model-edge-label,
  lv-semantic-model-graph .semantic-model-edge-endpoint {
    position: absolute;
    display: inline-grid;
    place-items: center;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-full);
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-resting-small, none);
    color: var(--lv-fg-default);
    font: var(--lv-type-caption);
    line-height: 1;
    pointer-events: none;
    z-index: 1;
  }

  lv-semantic-model-graph .semantic-model-edge-label {
    min-width: 30px;
    min-height: 20px;
    padding: 0 var(--base-size-6);
  }

  lv-semantic-model-graph .semantic-model-edge-label.selected {
    border-color: var(--lv-fg-muted);
    color: var(--lv-fg-default);
  }

  lv-semantic-model-graph .semantic-model-edge-endpoint {
    width: 20px;
    height: 20px;
    border-color: color-mix(in srgb, var(--lv-fg-muted), transparent 35%);
    color: var(--lv-fg-default);
  }

  lv-semantic-model-graph .semantic-model-edge-endpoint.source {
    margin-left: -16px;
  }

  lv-semantic-model-graph .semantic-model-edge-endpoint.target {
    margin-left: 16px;
  }

  lv-semantic-model-graph .semantic-model-node {
    width: ${NODE_WIDTH}px;
    overflow: hidden;
    border: var(--borderWidth-default) solid var(--lv-line-muted);
    border-radius: var(--borderRadius-default);
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-resting-small, none);
    color: var(--lv-fg-default);
    cursor: pointer;
  }

  lv-semantic-model-graph .semantic-model-node-selected {
    border-color: var(--lv-fg-muted);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--lv-fg-muted), transparent 35%), var(--shadow-resting-small, none);
  }

  lv-semantic-model-graph .semantic-model-node-dimmed {
    opacity: 0.42;
  }

  lv-semantic-model-graph .semantic-model-node:focus-visible {
    outline: 2px solid var(--lv-fg-muted);
    outline-offset: 2px;
  }

  lv-semantic-model-graph .semantic-model-node-header {
    display: flex;
    min-height: ${HEADER_HEIGHT}px;
    min-width: 0;
    gap: var(--base-size-8);
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
    padding: 0 var(--base-size-12);
    align-items: center;
    justify-content: space-between;
  }

  lv-semantic-model-graph .semantic-model-node-title {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-6);
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-semibold);
  }

  lv-semantic-model-graph .semantic-model-node-title span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  lv-semantic-model-graph .semantic-dataset-icon {
    flex: 0 0 auto;
    color: var(--lv-fg-muted);
  }

  lv-semantic-model-graph .semantic-model-node-fields {
    display: grid;
  }

  lv-semantic-model-graph .semantic-model-field {
    display: grid;
    min-height: ${FIELD_HEIGHT}px;
    grid-template-columns: 18px minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--base-size-6);
    border-bottom: var(--lv-border-muted);
    box-shadow: inset 0 0 0 0 transparent;
    padding: 0 var(--lv-space-control);
  }

  lv-semantic-model-graph .semantic-model-field:last-child {
    border-bottom: 0;
  }

  lv-semantic-model-graph .semantic-model-hidden-fields {
    display: flex;
    min-height: ${FIELD_HEIGHT}px;
    align-items: center;
    border-top: var(--lv-border-muted);
    color: var(--lv-fg-muted);
    padding: 0 var(--lv-space-control);
    font: var(--lv-type-caption);
  }

  lv-semantic-model-graph .semantic-model-field-join {
    background: color-mix(in srgb, var(--lv-fg-muted), transparent 90%);
    box-shadow: inset 2px 0 0 color-mix(in srgb, var(--lv-fg-muted), transparent 42%);
  }

  lv-semantic-model-graph .semantic-model-field-highlighted {
    background: color-mix(in srgb, var(--lv-line-accent), transparent 88%);
    box-shadow: inset 3px 0 0 var(--lv-line-accent);
  }

  lv-semantic-model-graph .semantic-model-field-name {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-code-inline);
  }

  lv-semantic-model-graph .semantic-model-field-grain .semantic-model-field-name {
    font-weight: var(--base-text-weight-semibold);
  }

  lv-semantic-model-graph .semantic-model-field-type-icon {
    display: inline-grid;
    width: 18px;
    height: 18px;
    place-items: center;
    color: var(--lv-fg-muted);
  }

  lv-semantic-model-graph .semantic-model-type-icon {
    display: block;
    width: 14px;
    height: 14px;
  }

  lv-semantic-model-graph .semantic-model-field-grain-marker {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    line-height: 1;
  }
`
