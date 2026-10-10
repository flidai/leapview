import { css } from 'lit'

export const chatPagePreviewStyles = css`
    .chat-layout {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    .chat-layout { grid-template-areas: 'chat'; }
    .body { grid-area: chat; }
    .chat-layout.preview-open {
      grid-template-columns: minmax(300px, .9fr) minmax(0, 1.1fr);
      grid-template-areas: 'chat visuals';
    }
    .preview-open .thread-stack { --lv-chat-stack-width: 100%; }
    .preview-panel {
      grid-area: visuals; display: grid; grid-template-rows: auto minmax(0, 1fr);
      min-width: 0; min-height: 0; overflow: hidden; border-left: var(--lv-border-default);
      background: var(--lv-bg-panel);
    }
    .preview-scroll { overflow: auto; min-height: 0; padding: 16px; overscroll-behavior: contain; }
    .preview-panel:has(.dashboard-destination) { grid-template-rows: auto auto minmax(0, 1fr); }
    .preview-panel[hidden], .builder-stage[hidden], .builder-frame[hidden] { display: none; }
    .builder-stage { grid-area: builder; min-width: 0; min-height: 0; position: relative; }
    .builder-frame { display: block; width: 100%; height: 100%; border: 0; }
    .preview-actions { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; flex-shrink: 0; }
    .save-error { padding: 12px; color: var(--lv-fg-danger); font: var(--lv-type-body-compact); }
    .preview-action:disabled { opacity: .6; cursor: default; }
    .chat-layout.builder-open { grid-template-columns: minmax(0, 1fr) clamp(280px, 24vw, 320px); grid-template-areas: 'builder chat'; }
    .chat-layout.builder-open .body {
      --lv-type-body: 400 14px/1.5 var(--fontStack-system);
      --lv-chat-stack-gap: 24px;
    }
    .builder-open .body { border-left: var(--lv-border-default); }
    .preview-heading { flex-wrap: wrap; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 16px; border-bottom: var(--lv-border-default); }
    .preview-heading h2 { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin: 0; font: var(--lv-type-section-title); }
    .preview-empty { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .preview-grid { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
    .preview-card[hidden] { display: none; }
    .preview-card { min-width: 0; border-radius: var(--lv-radius-default); outline: none; scroll-margin: 6px; }
    .preview-card:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 3px; }
    .preview-card lv-visual-artifact { height: clamp(300px, 44svh, 480px); }
    .preview-card.kpi lv-visual-artifact { height: 180px; }
    .preview-card.wide lv-visual-artifact {
      height: auto;
      --lv-visual-height: auto;
      --lv-table-max-body-height: min(52svh, 560px);
    }
    .preview-action {
      display: inline-flex; align-items: center; justify-content: center; gap: 8px;
      padding: 7px 12px; min-height: 34px; border: var(--lv-border-default);
      border-radius: var(--lv-radius-default); background: var(--lv-bg-panel);
      color: var(--lv-fg-default); font: var(--lv-type-body-compact); cursor: pointer; text-decoration: none;
    }
    .preview-action:hover { background: var(--lv-bg-control-hover); }
    .preview-action:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 2px; }
    .preview-action svg { width: 16px; height: 16px; }
    .close-visuals { padding: 7px; }
    .titlebar-start { display: flex; min-width: 0; align-items: center; gap: 16px; }
    .titlebar-start h1 { min-width: 0; }
    .conversation-titlebar { grid-template-columns: minmax(0, 1fr) auto; }

    @media (max-width: 900px) {
      .chat-layout.preview-open { grid-template-columns: minmax(0, 1fr); grid-template-rows: minmax(220px, 42%) minmax(0, 1fr); grid-template-areas: 'chat' 'visuals'; }
      .chat-layout.builder-open { grid-template-rows: minmax(0, 1fr) minmax(220px, 38%); grid-template-areas: 'builder' 'chat'; }
      .preview-panel, .builder-open .body { border-left: 0; border-top: var(--lv-border-default); }
      .preview-scroll { padding: 12px; }
      .titlebar-start { flex-wrap: wrap; gap: 8px; }
      .preview-heading { padding: 10px 12px; }
    }

    .main.builder-main { grid-template-rows: minmax(0, 1fr); }
    .body.with-chat-header { grid-template-rows: auto minmax(0, 1fr); }
    .chat-pane-header {
      display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center;
      gap: 6px; min-height: 50px; padding: 0 12px; border-bottom: var(--lv-border-default);
    }
    .chat-pane-header[hidden] { display: none; }
    .chat-pane-heading { display: flex; min-width: 0; align-items: center; gap: 6px; white-space: nowrap; font: var(--lv-type-body-compact); }
    .chat-page-name { overflow: hidden; text-overflow: ellipsis; }
    .chat-pane-heading svg { flex-shrink: 0; }
    .dashboard-destination { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-bottom: var(--lv-border-default); font: var(--lv-type-body-compact); }
    .dashboard-destination select { min-width: 0; max-width: 240px; padding: 5px 8px; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); }
    .chat-pane-heading svg { width: 16px; height: 16px; }
    .titlebar-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 8px; }
    @media (max-width: 640px) {
      .conversation-titlebar { grid-template-columns: minmax(0, 1fr); gap: 8px; }
      .conversation-titlebar .titlebar-actions { justify-content: flex-start; }
    }
    .chat-size-toggle {
      display: inline-flex; align-items: center; justify-content: center;
      width: 32px; height: 32px; flex-shrink: 0; padding: 6px;
      border: 0; border-radius: var(--lv-radius-default); background: transparent;
      color: var(--lv-fg-muted); cursor: pointer;
    }
    .chat-size-toggle svg { width: 16px; height: 16px; }
    .chat-size-toggle:hover { background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
    .chat-size-toggle:focus-visible { outline: 2px solid var(--lv-accent); outline-offset: 2px; }
    .chat-size-toggle:disabled { opacity: .6; cursor: default; }

`
