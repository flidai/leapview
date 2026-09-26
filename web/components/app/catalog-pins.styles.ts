import { css } from 'lit'

export const catalogPinnedStyles = css`
    .pinned-dashboards { display: grid; gap: var(--base-size-8); }
    .pinned-dashboards h2 { margin: 0; font: var(--lv-type-section-title); }
    .pinned-dashboards-list { display: flex; flex-wrap: wrap; gap: var(--base-size-8); margin: 0; padding: 0; list-style: none; }
    .pinned-dashboard { display: flex; min-width: 0; align-items: center; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); }
    .pinned-dashboard a { display: inline-flex; min-width: 0; align-items: center; gap: var(--base-size-8); color: var(--lv-fg-default); padding: var(--base-size-8) var(--base-size-12); font: var(--lv-type-body-compact); text-decoration: none; }
    .pinned-dashboard a span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .pinned-dashboard button { display: inline-grid; width: var(--control-medium-size); height: var(--control-medium-size); flex: 0 0 auto; place-items: center; margin-right: var(--base-size-4); border: 0; border-radius: var(--lv-radius-default); color: var(--lv-fg-muted); background: transparent; cursor: pointer; }
    .pinned-dashboard a:hover, .pinned-dashboard button:hover { background: var(--lv-bg-control-hover); }
    .pinned-dashboard a:focus-visible, .pinned-dashboard button:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }

`
