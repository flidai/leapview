import { css } from 'lit'
import { builderCanvasDesktopWidth } from './dashboard-builder-canvas-size'
import { builderCanvasMinimumHeight } from './dashboard-builder-canvas-size'

export const dashboardBuilderCanvasStyles = css`
    .field-browser-header {
      position: sticky;
      z-index: 1;
      top: 0;
      padding: var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    @media (min-width: 1201px), (min-width: 641px) and (max-width: 960px) {
      .pane[data-collapsed='true'] {
        overflow: hidden;
      }

      .pane[data-collapsed='true'] .pane-header,
      .pane[data-collapsed='true'] .field-browser-header {
        position: relative;
        height: 100%;
        box-sizing: border-box;
        padding: var(--base-size-8) var(--base-size-4);
        border-bottom: 0;
      }

      .pane[data-collapsed='true'] .pane-heading-row,
      .pane[data-collapsed='true'] .inspector-heading {
        height: 100%;
        flex-direction: column;
        justify-content: flex-start;
        flex-wrap: nowrap;
      }

      .pane[data-collapsed='true'] .pane-title-group,
      .pane[data-collapsed='true'] .inspector-title {
        flex-direction: column;
      }

      .pane[data-collapsed='true'] .pane-title {
        writing-mode: vertical-rl;
        transform: rotate(180deg);
        overflow: visible;
        text-overflow: clip;
      }

      .pane[data-collapsed='true'] .pane-collapse {
        position: absolute;
        z-index: 1;
        inset: 0;
        width: 100%;
        height: 100%;
        min-height: 0;
        border: 0;
        border-radius: 0;
        background: transparent;
      }

      .pane[data-collapsed='true'] .pane-collapse svg {
        display: none;
      }

      .pane[data-collapsed='true']:hover .pane-header,
      .pane[data-collapsed='true']:hover .field-browser-header {
        background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
      }

      .pane[data-collapsed='true'] .pane-collapse:focus-visible {
        outline: 2px solid var(--lv-fg-accent);
        outline-offset: -2px;
      }
    }

    .canvas-pane {
      display: grid;
      grid-column: 1;
      grid-row: 1;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr);
      background: var(--lv-report-canvas-bg, var(--lv-bg-app));
    }

    .page-tabs {
      display: flex;
      flex: 0 1 auto;
      align-self: stretch;
      min-width: 0;
      gap: 0;
      overflow-x: auto;
      overscroll-behavior-x: contain;
      padding-left: var(--base-size-8);
      scrollbar-width: thin;
    }

    .page-tab {
      display: flex;
      flex: 0 0 auto;
      min-width: 4.5rem;
      max-width: 14rem;
      min-height: 100%;
      align-items: center;
      justify-content: center;
      overflow: hidden;
      border: 0;
      border-left: var(--lv-border-width) solid var(--lv-line-muted);
      border-right: var(--lv-border-width) solid var(--lv-line-muted);
      border-radius: 0;
      padding: 0 var(--base-size-16);
      color: inherit;
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-body-compact);
      text-align: left;
      text-decoration: none;
      text-overflow: ellipsis;
      white-space: nowrap;
      cursor: pointer;
    }

    .page-tab:hover {
      background: var(--lv-bg-control-hover);
    }

    .page-tab:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: -2px;
    }

    .page-tab[aria-selected='true'],
    .page-tab[aria-current='page'] {
      border-color: var(--lv-line-muted);
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel);
      font-weight: var(--base-text-weight-semibold);
      box-shadow: inset 0 -3px 0 var(--lv-fg-accent);
    }

    .page-tab[data-page-dragging='true'] {
      opacity: 0.55;
    }

    .page-tab[data-page-drop='true'] {
      box-shadow: inset 3px 0 0 var(--lv-line-emphasis);
    }

    .canvas-scroll {
      position: relative;
      overflow: auto;
      min-width: 0;
      padding: 0;
      background: var(--lv-report-canvas-bg, var(--lv-bg-app));
    }

    .canvas-fit {
      position: relative;
      width: var(--builder-canvas-fitted-width, 100%);
      height: var(--builder-canvas-fitted-height, 0);
      margin-inline: auto;
    }

    .canvas {
      --builder-grid-offset: 0px;
      position: absolute;
      inset: var(--builder-grid-offset) auto auto var(--builder-grid-offset);
      box-sizing: border-box;
      width: var(--builder-grid-width, ${builderCanvasDesktopWidth}px);
      min-height: ${builderCanvasMinimumHeight}px;
      border: 0;
      border-radius: 0;
      background-color: var(--lv-report-page-bg, var(--lv-bg-panel));
      background-image: none;
      background-size: calc(100% / var(--builder-grid-columns)) var(--builder-grid-row-pitch);
      box-shadow: none;
      transform: scale(var(--builder-canvas-scale, 1));
      transform-origin: top left;
    }

    .canvas[data-field-dragging='true'] {
      outline: 2px dashed var(--lv-data-2);
      outline-offset: -4px;
    }

    .canvas[data-grid-guides='true'],
    .canvas:has(.ui-draggable-dragging, .ui-resizable-resizing) {
      background-image: linear-gradient(to right, color-mix(in srgb, var(--lv-line-muted) 55%, transparent) 1px, transparent 1px), linear-gradient(to bottom, color-mix(in srgb, var(--lv-line-muted) 55%, transparent) 1px, transparent 1px);
    }

    .canvas-field-drop-hint {
      position: sticky;
      z-index: 5;
      top: var(--base-size-12);
      display: none;
      width: fit-content;
      max-width: calc(100% - 2rem);
      align-items: center;
      margin: var(--base-size-12) auto 0;
      border: 1px solid var(--lv-data-2);
      border-radius: var(--lv-radius-full);
      padding: var(--base-size-6) var(--base-size-12);
      color: var(--lv-fg-default);
      background: var(--lv-data-2-muted);
      box-shadow: var(--lv-shadow-floating-sm);
      font: var(--lv-type-body-compact);
      pointer-events: none;
    }

    .canvas[data-field-dragging='true'] .canvas-field-drop-hint {
      display: flex;
    }

    /* GridStack's package stylesheet cannot cross this component's shadow
     * boundary, so keep the small set of layout rules it needs local. The
     * library supplies the --gs-* values and writes the gs-* attributes. */
    .grid-stack {
      position: relative;
    }

    .grid-stack > .grid-stack-item {
      position: absolute;
      top: 0;
      width: var(--gs-column-width);
      height: var(--gs-cell-height);
      padding: 0;
    }

    .grid-stack > .grid-stack-item > .grid-stack-item-content {
      position: absolute;
      top: var(--gs-item-margin-top);
      right: var(--gs-item-margin-right);
      bottom: var(--gs-item-margin-bottom);
      left: var(--gs-item-margin-left);
      display: grid;
      width: auto;
      height: auto;
      margin: 0;
      overflow: hidden;
    }

    .grid-stack:not(.grid-stack-rtl) > .grid-stack-item {
      left: 0;
    }

    .grid-stack > .grid-stack-item > .grid-stack-item-content {
      top: var(--gs-item-margin-top);
      right: var(--gs-item-margin-right);
      bottom: var(--gs-item-margin-bottom);
      left: var(--gs-item-margin-left);
    }

    .grid-stack-item > .ui-resizable-handle {
      position: absolute;
      z-index: 2;
      display: block;
      box-sizing: border-box;
      touch-action: none;
      user-select: none;
    }

    .grid-stack-item > .ui-resizable-n,
    .grid-stack-item > .ui-resizable-s {
      right: calc(var(--gs-item-margin-right) + 18px);
      left: calc(var(--gs-item-margin-left) + 18px);
      height: 12px;
    }

    .grid-stack-item > .ui-resizable-n {
      top: var(--gs-item-margin-top);
      cursor: n-resize;
    }

    .grid-stack-item > .ui-resizable-s {
      bottom: var(--gs-item-margin-bottom);
      cursor: s-resize;
    }

    .grid-stack-item > .ui-resizable-e,
    .grid-stack-item > .ui-resizable-w {
      top: calc(var(--gs-item-margin-top) + 18px);
      bottom: calc(var(--gs-item-margin-bottom) + 18px);
      width: 12px;
    }

    .grid-stack-item > .ui-resizable-e {
      right: var(--gs-item-margin-right);
      cursor: e-resize;
    }

    .grid-stack-item > .ui-resizable-w {
      left: var(--gs-item-margin-left);
      cursor: w-resize;
    }

    .grid-stack-item > .ui-resizable-ne,
    .grid-stack-item > .ui-resizable-nw,
    .grid-stack-item > .ui-resizable-se,
    .grid-stack-item > .ui-resizable-sw {
      width: 20px;
      height: 20px;
    }

    .grid-stack-item > .ui-resizable-ne,
    .grid-stack-item > .ui-resizable-nw { top: var(--gs-item-margin-top); }
    .grid-stack-item > .ui-resizable-se,
    .grid-stack-item > .ui-resizable-sw { bottom: var(--gs-item-margin-bottom); }
    .grid-stack-item > .ui-resizable-ne,
    .grid-stack-item > .ui-resizable-se { right: var(--gs-item-margin-right); }
    .grid-stack-item > .ui-resizable-nw,
    .grid-stack-item > .ui-resizable-sw { left: var(--gs-item-margin-left); }

    .grid-stack-item > .ui-resizable-ne { cursor: ne-resize; }
    .grid-stack-item > .ui-resizable-nw { cursor: nw-resize; }
    .grid-stack-item > .ui-resizable-se { cursor: se-resize; }
    .grid-stack-item > .ui-resizable-sw { cursor: sw-resize; }

    .grid-stack-item > .ui-resizable-handle::after {
      position: absolute;
      border: var(--lv-border-default);
      background: var(--lv-bg-panel);
      content: '';
      pointer-events: none;
    }

    .grid-stack-item > .ui-resizable-n::after,
    .grid-stack-item > .ui-resizable-s::after {
      left: 50%;
      width: 18px;
      height: 4px;
      border-radius: var(--lv-radius-full);
      transform: translateX(-50%);
    }

    .grid-stack-item > .ui-resizable-n::after { top: -2px; }
    .grid-stack-item > .ui-resizable-s::after { bottom: -2px; }

    .grid-stack-item > .ui-resizable-e::after,
    .grid-stack-item > .ui-resizable-w::after {
      top: 50%;
      width: 4px;
      height: 18px;
      border-radius: var(--lv-radius-full);
      transform: translateY(-50%);
    }

    .grid-stack-item > .ui-resizable-e::after { right: -2px; }
    .grid-stack-item > .ui-resizable-w::after { left: -2px; }

    .grid-stack-item > .ui-resizable-ne::after,
    .grid-stack-item > .ui-resizable-nw::after,
    .grid-stack-item > .ui-resizable-se::after,
    .grid-stack-item > .ui-resizable-sw::after {
      top: 50%;
      left: 50%;
      width: 8px;
      height: 8px;
      border-radius: var(--lv-radius-small);
      transform: translate(-50%, -50%);
    }

    .grid-stack-item.ui-resizable-disabled > .ui-resizable-handle {
      display: none;
    }

    .grid-stack-item:not([data-selected='true']) > .ui-resizable-handle {
      display: none !important;
    }

    .visual > .grid-stack-item-content,
    .filter-component > .grid-stack-item-content,
    .header-component > .grid-stack-item-content,
    .builder-placeholder > .grid-stack-item-content {
      isolation: isolate;
      grid-template-rows: auto minmax(0, 1fr) auto;
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-8);
      color: inherit;
      background: color-mix(in srgb, var(--lv-bg-panel) 96%, transparent);
      text-align: left;
    }

    .visual.has-preview > .grid-stack-item-content {
      grid-template-rows: minmax(0, 1fr);
      padding: 0;
    }

    .visual:hover > .grid-stack-item-content {
      border-color: var(--lv-line-emphasis);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-bg-control-hover);
    }

    .visual[data-selected='true'] > .grid-stack-item-content {
      border-color: var(--lv-data-3);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-data-3-muted);
    }

    .filter-component[data-selected='true'] > .grid-stack-item-content {
      border-color: var(--lv-data-2);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-data-2-muted);
    }

    .header-component > .grid-stack-item-content,
    .builder-placeholder > .grid-stack-item-content {
      grid-template-rows: auto minmax(0, 1fr);
      background: var(--lv-bg-panel);
    }

    .header-component[data-selected='true'] > .grid-stack-item-content {
      border-color: var(--lv-data-4);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-data-4-muted);
    }

    .header-copy {
      display: grid;
      align-content: center;
      gap: var(--base-size-4);
      min-width: 0;
      padding: var(--base-size-8);
    }

    .header-copy span,
    .builder-placeholder span {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .builder-placeholder > .grid-stack-item-content {
      align-content: center;
      gap: var(--base-size-4);
      border-style: dashed;
      color: var(--lv-fg-muted);
      text-align: center;
    }

    .component-drag-handle {
      display: flex;
      width: 100%;
      min-width: 0;
      min-height: var(--control-small-size);
      align-items: center;
      overflow: hidden;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
      cursor: grab;
      touch-action: none;
      user-select: none;
    }

    .component-drag-handle:hover {
      color: var(--lv-data-4);
    }

`
