import { css } from 'lit'

/** Browser pane layout, resource tree, and field selection controls. */
export const dataExplorerBrowserStyles = css`
  .explorer {
    display: grid;
    min-width: 0;
    min-height: 0;
    grid-template-columns: auto 4px minmax(0, 1fr);
    overflow: hidden;
  }

  .browser,
  .main {
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .browser {
    display: grid;
    grid-template-rows: auto minmax(0, 1fr);
    background: var(--lv-bg-app);
  }

  .browser-tools {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-6);
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-6) var(--base-size-8);
  }

  .sidebar-toggle {
    display: grid;
    width: var(--control-small-size);
    height: var(--control-small-size);
    flex: none;
    place-items: center;
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-muted);
    cursor: pointer;
  }

  .sidebar-toggle:hover,
  .sidebar-toggle:focus-visible {
    background: var(--lv-bg-control-hover);
    color: var(--lv-fg-default);
    outline: 0;
  }

  .browser-resizer {
    position: relative;
    min-width: 4px;
    border-right: var(--lv-border-muted);
    cursor: col-resize;
    touch-action: none;
  }

  .browser-resizer::after {
    position: absolute;
    inset-block: 0;
    left: 1px;
    width: 2px;
    background: transparent;
    content: '';
  }

  .browser-resizer:hover::after,
  .browser-resizer:focus-visible::after {
    background: var(--lv-fg-link);
  }

  .browser-resizer:focus-visible {
    outline: 0;
  }

  .browser-collapsed .browser {
    grid-template-rows: max-content;
    align-content: start;
  }

  .explorer.browser-collapsed {
    grid-template-columns: auto minmax(0, 1fr);
  }

  .browser-collapsed .browser-tools {
    justify-content: start;
    padding-inline: var(--base-size-8);
  }

  .browser-collapsed .browser-resizer {
    display: none;
  }

  .explore-browser {
    grid-template-rows: auto auto minmax(0, 1fr);
  }

  .selectors {
    display: grid;
    gap: var(--base-size-8);
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-12);
  }

  .selectors label,
  .filter-editor label {
    display: grid;
    gap: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
  }

  select,
  .filter-editor input {
    min-width: 0;
    height: var(--control-medium-size);
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-control);
    color: var(--lv-fg-default);
    padding: 0 var(--base-size-8);
    font: var(--lv-type-body);
  }

  .field-groups {
    min-height: 0;
    overflow: auto;
    padding: var(--base-size-8);
  }

  .field-group {
    margin-bottom: var(--base-size-8);
  }

  .field-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    border-radius: var(--lv-radius-default);
  }

  .field-row:hover,
  .field-row:focus-within {
    background: var(--lv-bg-control-hover);
  }

  .field-button {
    display: grid;
    min-width: 0;
    grid-template-columns: 1rem minmax(0, 1fr);
    gap: var(--base-size-8);
    align-items: center;
    padding: var(--base-size-8);
    text-align: left;
  }

  .field-button strong,
  .field-button small {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .field-button strong {
    font: var(--lv-type-body);
  }

  .field-button small {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .field-button.is-selected strong {
    color: var(--lv-fg-accent);
  }

  .field-action {
    display: grid;
    width: var(--control-small-size);
    height: var(--control-small-size);
    place-items: center;
    color: var(--lv-fg-muted);
  }

  .search {
    position: relative;
    min-width: 0;
    flex: 1;
  }

  .search input {
    width: 100%;
    min-width: 0;
    height: var(--control-small-size);
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-control);
    color: var(--lv-fg-default);
    padding: 0 var(--base-size-8) 0 var(--base-size-32);
    font: var(--lv-type-body);
  }

  .search-icon {
    position: absolute;
    left: var(--base-size-8);
    top: 50%;
    display: grid;
    color: var(--lv-fg-muted);
    transform: translateY(-50%);
  }

  .tree {
    min-height: 0;
    overflow: auto;
    padding: var(--base-size-6);
  }

  details {
    min-width: 0;
  }

  summary {
    display: grid;
    grid-template-columns: 1rem 1rem minmax(0, 1fr);
    gap: var(--base-size-6);
    align-items: center;
    border-radius: var(--lv-radius-default);
    min-height: var(--control-small-size);
    padding: var(--base-size-4) var(--base-size-6);
    color: var(--lv-fg-muted);
    cursor: pointer;
    list-style: none;
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
    text-transform: uppercase;
  }

  summary::-webkit-details-marker {
    display: none;
  }

  details[open] > summary .chevron {
    transform: rotate(90deg);
  }

  .object-list {
    display: grid;
    gap: var(--base-size-2);
    padding: var(--base-size-2) 0 var(--base-size-8) var(--base-size-16);
  }

  .object-node > summary {
    display: grid;
    grid-template-columns: 1rem 1rem minmax(0, 1fr);
    gap: var(--base-size-6);
    min-height: var(--control-small-size);
    padding: var(--base-size-4) var(--base-size-6);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
    text-transform: none;
  }

  .object-expand {
    display: grid;
    width: 1.5rem;
    height: 1.5rem;
    place-items: center;
    margin: calc((1.5rem - 1rem) / -2);
    border-radius: var(--lv-radius-default);
    cursor: pointer;
  }

  .object-expand:hover {
    background: var(--lv-bg-control-hover);
  }

  .object-node[open] > summary .chevron {
    transform: rotate(90deg);
  }

  .object-button {
    display: grid;
    min-width: 0;
    width: 100%;
    grid-template-columns: 1rem 1rem minmax(0, 1fr);
    gap: var(--base-size-6);
    align-items: center;
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-default);
    min-height: var(--control-small-size);
    padding: var(--base-size-4) var(--base-size-6);
    text-align: left;
    cursor: pointer;
    font: inherit;
  }

  .object-button:hover,
  .object-button:focus-visible {
    background: var(--lv-bg-control-hover);
    outline: 0;
  }

  .object-button.is-selected {
    background: var(--lv-bg-accent-muted);
    color: var(--lv-fg-accent);
  }

  .object-button strong {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .object-button strong {
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
  }

  .object-label {
    min-width: 0;
  }

  .object-label small {
    display: block;
    overflow: hidden;
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-normal);
  }

  .object-button.is-selected .object-label small {
    color: var(--lv-fg-accent);
  }

  .column-list {
    display: grid;
    gap: var(--base-size-2);
    padding: var(--base-size-2) 0 var(--base-size-8) var(--base-size-32);
  }

  .column-item {
    display: grid;
    min-width: 0;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    min-height: var(--control-small-size);
    border-radius: var(--lv-radius-default);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .column-item:hover,
  .column-item:focus-within {
    background: var(--lv-bg-control-hover);
  }

  .column-item.is-unavailable {
    opacity: 0.58;
  }

  .column-item.is-unavailable:hover,
  .column-item.is-unavailable:focus-within {
    background: transparent;
  }

  .column-item .field-button {
    display: grid;
    min-width: 0;
    grid-template-columns: 1rem 1rem minmax(0, 1fr) auto;
    gap: var(--base-size-6);
    align-items: center;
    padding: var(--base-size-4) var(--base-size-8);
    text-align: left;
  }

  .column-item .field-button > span:nth-child(3) {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .column-item .field-button.is-selected > span:nth-child(3) {
    color: var(--lv-fg-accent);
  }

  .column-item .field-button:disabled {
    cursor: not-allowed;
  }

  .field-check {
    display: grid;
    place-items: center;
    color: var(--lv-fg-muted);
  }

  .field-button.is-selected .field-check {
    color: var(--lv-fg-accent);
  }

  .metric-field code {
    color: var(--lv-fg-accent);
  }

  .column-item code {
    overflow: hidden;
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-caption);
  }

`
