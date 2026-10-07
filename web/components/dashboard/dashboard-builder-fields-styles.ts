import { css } from 'lit'
import { dashboardBuilderFieldStyles } from './dashboard-builder-field-styles'

export const dashboardBuilderFieldsStyles = css`
    .component-drag-grip {
      position: absolute;
      z-index: 3;
      top: var(--base-size-4);
      left: 50%;
      display: grid;
      width: var(--control-small-size);
      min-height: 1.25rem;
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-full);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
      opacity: 0;
      pointer-events: none;
      transform: translateX(-50%);
      transition: opacity var(--lv-duration-fast) var(--motion-easing-move);
    }

    .component-drag-grip svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .visual:hover .component-drag-grip,
    .visual[data-selected='true'] .component-drag-grip,
    .filter-component:hover .component-drag-grip,
    .filter-component[data-selected='true'] .component-drag-grip {
      opacity: 1;
      pointer-events: auto;
    }

    .component-drag-handle:active,
    .grid-stack-item.ui-draggable-dragging .component-drag-handle {
      cursor: grabbing;
    }

    .visual-picker-catalog {
      display: block;
      max-height: none;
      overflow: visible;
    }

    .visual-picker {
      display: grid;
      grid-template-columns: repeat(7, minmax(0, 1fr));
      gap: var(--base-size-4);
    }

    .visual-picker-button {
      --visual-picker-color: var(--lv-fg-muted);
      --visual-picker-muted: var(--lv-bg-panel-muted);
      display: grid;
      min-width: 0;
      min-height: 2.25rem;
      place-items: center;
      border-color: transparent;
      padding: var(--base-size-2);
      color: var(--visual-picker-color);
      background: var(--lv-bg-panel-muted);
    }

    .visual-picker-button[data-visual-picker-type='bar'] {
      --visual-picker-color: var(--lv-data-1);
      --visual-picker-muted: var(--lv-data-1-muted);
    }

    .visual-picker-button[data-visual-picker-type='column'] {
      --visual-picker-color: var(--lv-data-2);
      --visual-picker-muted: var(--lv-data-2-muted);
    }

    .visual-picker-button[data-visual-picker-type='line'] {
      --visual-picker-color: var(--lv-data-3);
      --visual-picker-muted: var(--lv-data-3-muted);
    }

    .visual-picker-button[data-visual-picker-type='area'] {
      --visual-picker-color: var(--lv-data-4);
      --visual-picker-muted: var(--lv-data-4-muted);
    }

    .visual-picker-button[data-visual-picker-type='table'] {
      --visual-picker-color: var(--lv-data-5);
      --visual-picker-muted: var(--lv-data-5-muted);
    }

    .visual-picker-button[data-visual-picker-type='kpi'] {
      --visual-picker-color: var(--lv-data-6);
      --visual-picker-muted: var(--lv-data-6-muted);
    }

    .visual-picker-button[data-visual-group='Part to whole'] {
      --visual-picker-color: var(--lv-data-4);
      --visual-picker-muted: var(--lv-data-4-muted);
    }

    .visual-picker-button[data-visual-group='Distribution'] {
      --visual-picker-color: var(--lv-data-3);
      --visual-picker-muted: var(--lv-data-3-muted);
    }

    .visual-picker-button[data-visual-group='Hierarchy & flow'] {
      --visual-picker-color: var(--lv-data-2);
      --visual-picker-muted: var(--lv-data-2-muted);
    }

    .visual-picker-button[data-visual-group='Specialized'] {
      --visual-picker-color: var(--lv-data-6);
      --visual-picker-muted: var(--lv-data-6-muted);
    }

    .visual-picker-button[data-visual-group='Tables'] {
      --visual-picker-color: var(--lv-data-5);
      --visual-picker-muted: var(--lv-data-5-muted);
    }

    .visual-picker-button[data-visual-group='Filters'] {
      --visual-picker-color: var(--lv-data-1);
      --visual-picker-muted: var(--lv-data-1-muted);
    }

    .visual-picker-button:hover {
      border-color: color-mix(in srgb, var(--visual-picker-color) 55%, var(--lv-line-default));
      background: color-mix(in srgb, var(--visual-picker-muted) 72%, var(--lv-bg-panel-muted));
    }

    .visual-picker-button[aria-pressed='true'] {
      border-color: var(--visual-picker-color);
      background: var(--visual-picker-muted);
      box-shadow: inset 0 0 0 var(--lv-border-width) var(--visual-picker-color);
    }

    .visual-picker-button svg {
      width: 1.25rem;
      height: 1.25rem;
      overflow: visible;
    }

    .visual-picker-button .visual-icon-primary,
    .visual-picker-button .visual-icon-secondary,
    .visual-picker-button .visual-icon-tertiary,
    .visual-picker-button .visual-icon-axis {
      fill: currentColor;
    }

    .visual-picker-button .visual-icon-primary {
      opacity: 1;
    }

    .visual-picker-button .visual-icon-secondary {
      opacity: 0.58;
    }

    .visual-picker-button .visual-icon-tertiary {
      opacity: 0.24;
    }

    .visual-picker-button .visual-icon-axis {
      opacity: 0.36;
    }

    .visual-picker-button .visual-icon-stroke,
    .visual-picker-button .visual-icon-band,
    .visual-picker-button .visual-icon-ring,
    .visual-picker-button .visual-icon-sunburst-outer,
    .visual-picker-button .visual-icon-gauge {
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
    }

    .visual-picker-button .visual-icon-stroke {
      stroke-width: 2.25;
    }

    .visual-picker-button .visual-icon-stroke-thin {
      stroke-width: 1.5;
    }

    .visual-picker-button .visual-icon-band {
      stroke-width: 4;
    }

    .visual-picker-button .visual-icon-ring,
    .visual-picker-button .visual-icon-sunburst-outer {
      stroke-width: 4.5;
    }

    .visual-picker-button .visual-icon-gauge {
      stroke-width: 3.5;
    }

    .visual-picker-button .visual-icon-cutout {
      fill: var(--lv-bg-panel-muted);
    }

    .visual-reference-link {
      margin-left: auto;
      color: var(--lv-fg-accent);
      font: var(--lv-type-caption);
      text-decoration: none;
    }

    .visual-reference-link:hover {
      text-decoration: underline;
    }

    .inspector-panel {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      gap: var(--base-size-8);
      padding: var(--base-size-12);
    }

    .inspector-panel > *,
    .inspector-panel .property-group,
    .inspector-panel .field-wells,
    .inspector-panel .field-well,
    .inspector-panel .field-well-target {
      min-width: 0;
      grid-template-columns: minmax(0, 1fr);
    }

    .field-wells {
      display: grid;
      gap: var(--base-size-8);
    }

    ${dashboardBuilderFieldStyles}

    .property-heading {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
    }

    .builder-disclosure {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-8);
    }

    .builder-disclosure summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      cursor: pointer;
    }

    .builder-disclosure summary:has(.disclosure-count) {
      display: list-item;
    }

    .disclosure-count {
      float: right;
      min-width: 1.25rem;
      border-radius: var(--lv-radius-full);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font-weight: var(--base-text-weight-normal);
      text-align: center;
    }

    .builder-disclosure-content {
      padding-top: var(--base-size-8);
    }

    .interaction-targets {
      display: grid;
      gap: var(--base-size-8);
      margin: 0;
      padding: 0;
      border: 0;
    }

    .interaction-target {
      display: grid;
      gap: var(--base-size-6);
    }

    .interaction-target-title {
      overflow: hidden;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .interaction-effects {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: var(--base-size-4);
    }

    .interaction-effect {
      display: grid;
      place-items: center;
      min-height: var(--control-small-size);
      box-sizing: border-box;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel);
      font: var(--lv-type-caption);
      cursor: pointer;
    }

    .interaction-effect:has(input:focus-visible) {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 1px;
    }

    .interaction-effect[data-effect='filter'][data-selected='true'] {
      border-color: var(--lv-data-2);
      color: var(--lv-data-2);
      background: var(--lv-data-2-muted);
    }

    .interaction-effect[data-effect='highlight'][data-selected='true'] {
      border-color: var(--lv-data-3);
      color: var(--lv-data-3);
      background: var(--lv-data-3-muted);
    }

    .interaction-effect[data-effect='none'][data-selected='true'] {
      border-color: var(--lv-line-emphasis);
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel-muted);
    }

    .interaction-effect input {
      position: absolute;
      width: 1px;
      height: 1px;
      opacity: 0;
      pointer-events: none;
    }

    .field-well {
      display: grid;
      gap: var(--base-size-6);
    }

    .field-well-label {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
    }

    .field-well-label span:last-child {
      font-weight: var(--base-text-weight-normal);
    }

    .field-well-target {
      display: grid;
      min-height: var(--control-large-size);
      gap: var(--base-size-4);
      align-content: center;
      box-sizing: border-box;
      border: 1px dashed var(--lv-line-default);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-6);
      background: var(--lv-bg-panel-muted);
    }

    .field-well-target:hover,
    .field-well-target:focus-within {
      border-color: var(--lv-data-2);
      background: var(--lv-data-2-muted);
    }

    .field-well-target[data-field-drop='compatible'] {
      border-color: var(--lv-data-2);
      background: var(--lv-data-2-muted);
    }

    .field-well-target[data-field-drop='incompatible'] {
      opacity: 0.55;
    }

    .field-pill,
    .field-token {
      display: flex;
      min-height: var(--control-small-size);
      align-items: center;
      gap: var(--base-size-6);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: 0 var(--base-size-8);
      background: var(--lv-bg-panel);
      font: var(--lv-type-body-compact);
    }

    .field-token-label {
      min-width: 0;
      flex: 1 1 auto;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-token-actions {
      display: flex;
      flex: 0 0 auto;
      align-items: center;
      gap: var(--base-size-2);
    }

    .field-token-action {
      display: grid;
      width: 1.5rem;
      min-width: 1.5rem;
      min-height: 1.5rem;
      place-items: center;
      border-color: transparent;
      padding: 0;
      color: var(--lv-fg-muted);
      background: transparent;
      font: var(--lv-type-caption);
    }

    .field-token-action:hover:not(:disabled) {
      color: var(--lv-fg-default);
      background: var(--lv-bg-control-hover);
    }

    .field-token-action[data-field-action='remove']:hover:not(:disabled) {
      color: var(--lv-fg-danger, var(--lv-fg-default));
    }

    .field-token-action:disabled {
      opacity: 0.35;
    }

    .field-pill-kind {
      margin-left: auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .empty-well {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

`
