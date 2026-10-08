import { css } from 'lit'

// Keep the controls legible and their targets usable while giving the report
// the majority of the screen. Advanced options remain in native disclosures.
export const dashboardBuilderDensityStyles = css`
  .toolbar { gap: 8px; padding: 6px 12px; }
  .toolbar-actions { gap: 4px; }
  .arrange-toolbar { gap: 6px; }
  .title { font-size: var(--text-body-size-medium); line-height: 20px; }
  .meta { font-size: var(--text-body-size-small); line-height: 16px; margin-top: 0; }
  .toolbar button, .toolbar summary, .back { font-size: var(--text-body-size-small); }
  .pane-header, .field-browser-header { padding: 8px 10px; }
  .pane-title, .inspector-heading .pane-title { font-size: var(--text-body-size-small); line-height: 20px; }
  .inspector-heading { flex-wrap: nowrap; gap: 4px; }
  .inspector-title { flex: 1; gap: 6px; }
  .inspector-panel, .field-results { padding: 8px 10px; }
  .inspector-panel, .inspector-panel > *, .field-wells, .field-well, .field-well-target, .field-token { min-width: 0; }
  .visual-picker { grid-template-columns: repeat(auto-fit, minmax(28px, 1fr)); }
  .property-label, .format-section h3, .field-entity summary { font-size: var(--text-body-size-small); }
  .pane-hint, .field-well-label, .format-text-field { font-size: var(--text-body-size-small); }
  .field-browser[hidden] { display: none; }
  .field-browser { flex: 1; overflow: auto; }
  .field-search-header { position: sticky; top: 0; z-index: 1; padding: 8px 10px; background: var(--lv-bg-panel); border-bottom: var(--lv-border-muted); }
  .saved-library-tabs { padding: 4px 8px; }
  .saved-library-tabs button { min-height: 28px; padding: 4px; }
  .field { min-height: 34px; padding: 4px 6px; }
  .field-well { gap: 4px; }
  .field-well-target { padding: 4px; min-height: 34px; }
  .field-token { font-size: var(--text-body-size-small); padding: 0 6px; }
  .visual-query-controls, .visual-format-disclosure { margin-top: 4px; padding-top: 4px; border-top: var(--lv-border-muted); }
  .visual-query-controls > summary, .visual-format-disclosure > summary {
    min-height: 32px; line-height: 32px; font-size: var(--text-body-size-small); font-weight: var(--base-text-weight-semibold);
    cursor: pointer; color: var(--lv-fg-default);
  }
  .visual-query-controls > summary:focus-visible, .visual-format-disclosure > summary:focus-visible {
    outline: 2px solid var(--lv-fg-accent); outline-offset: 2px;
  }
  .visual-format-controls { margin: 0; border-top: 0; padding-top: 4px; gap: 8px; }
  .format-section { gap: 6px; padding-bottom: 8px; }
  .format-text-field input, .format-text-field select { font-size: var(--text-body-size-small); min-height: 30px; }
  .query-control-list { gap: 6px; }
  .visual-type-disclosure > summary { min-height: 32px; line-height: 32px; font-size: var(--text-body-size-small); cursor: pointer; }
  .visual-type-disclosure > summary:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
  .visual-type-disclosure .visual-picker-catalog { padding-top: 4px; }
  @media (hover: hover) {
    .field-token-action:not([data-field-action='remove']) { opacity: 0; }
    .field-token:hover .field-token-action, .field-token:focus-within .field-token-action { opacity: 1; }
    .field-token:hover .field-token-action:disabled, .field-token:focus-within .field-token-action:disabled { opacity: .35; }
  }
  .page-bar { min-height: 36px; }
  .page-bar > button { width: 36px; min-height: 36px; }
  .page-tab { min-height: 36px; font-size: var(--text-body-size-small); }
  .right-dock[hidden] { display: none; }
  .body.tools-hidden, .chat-preview .body.tools-hidden { grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(0, 1fr) auto; }

  /* Chat uses the original side-by-side tools. Opening one section never
   * hides another; narrow screens retain the regular mobile flow. */
  @media (min-width: 641px) {
    .chat-preview .body { grid-template-columns: minmax(0, 1fr) max-content; grid-template-rows: minmax(0, 1fr) auto; }
    .chat-preview .right-dock {
      grid-column: 2; grid-row: 1 / span 2; max-height: none; border-top: 0;
      grid-template-columns: var(--dock-filters-width) var(--dock-visuals-width) var(--dock-data-width);
      grid-template-rows: minmax(0, 1fr);
    }
    .chat-preview .pane { border-left: var(--lv-border-muted); border-top: 0; }
    .chat-preview .pane[data-collapsed='true'] { overflow: hidden; }
    .chat-preview .pane[data-collapsed='true'] .pane-header {
      position: relative; height: 100%; box-sizing: border-box; padding: 8px 4px; border-bottom: 0;
    }
    .chat-preview .pane[data-collapsed='true'] .pane-heading-row,
    .chat-preview .pane[data-collapsed='true'] .inspector-heading {
      height: 100%; flex-direction: column; justify-content: flex-start; flex-wrap: nowrap;
    }
    .chat-preview .pane[data-collapsed='true'] .pane-title-group,
    .chat-preview .pane[data-collapsed='true'] .inspector-title { flex-direction: column; }
    .chat-preview .pane[data-collapsed='true'] .pane-title { writing-mode: vertical-rl; transform: rotate(180deg); overflow: visible; text-overflow: clip; }
    .chat-preview .pane[data-collapsed='true'] .pane-collapse {
      position: absolute; z-index: 1; inset: 0; width: 100%; height: 100%; min-height: 0;
      border: 0; border-radius: 0; background: transparent;
    }
    .chat-preview .pane[data-collapsed='true'] .pane-collapse svg { display: none; }
    .chat-preview .pane[data-collapsed='true']:hover .pane-header { background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted)); }
    .chat-preview .pane[data-collapsed='true'] .pane-collapse:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: -2px; }
  }

  @media (min-width: 961px) and (max-width: 1200px) {
    .body { grid-template-columns: minmax(0, 1fr) 288px; }
    .chat-preview .body { grid-template-columns: minmax(0, 1fr) max-content; }
  }

  @media (max-width: 640px) {
    .pane-header, .field-browser-header { padding: 8px 10px; }
    .toolbar { padding: 6px 8px; }
    .format-text-field input, .format-text-field select { min-height: 32px; }
  }
`
