import { css, html } from 'lit'
import { CalendarDays, X } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'

const grains = ['second', 'minute', 'hour', 'day', 'week', 'month', 'quarter', 'year'] as const
export type TimeGroupingGrain = typeof grains[number]

export const timeGroupingStyles = css`
  .time-grouping {
    display: inline-flex; align-items: center; flex-wrap: wrap; gap: 8px;
    box-sizing: border-box; min-width: 0; max-width: 100%;
    padding: 6px 8px 6px 10px;
    border: var(--lv-border-muted); border-radius: var(--lv-radius-default, 6px);
    background: var(--lv-bg-panel); color: var(--lv-fg-default);
    font: var(--lv-type-caption);
  }
  .time-grouping-field { display: inline-flex; align-items: center; gap: 6px; min-width: 0; max-width: 100%; }
  .time-grouping-field svg { flex: none; color: var(--lv-fg-muted); }
  .time-grouping-field span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .time-grouping label { display: inline-flex; align-items: center; gap: 8px; margin: 0; color: var(--lv-fg-muted); font: inherit; white-space: nowrap; }
  .time-grouping select {
    box-sizing: border-box; width: auto; min-width: 92px; height: 28px; margin: 0;
    padding: 0 8px; border: var(--lv-border-default); border-radius: var(--lv-radius-default, 6px);
    background: var(--lv-bg-control); color: var(--lv-fg-default); font: inherit; cursor: pointer;
  }
  .time-grouping-remove {
    display: inline-grid; flex: none; place-items: center; width: 28px; height: 28px;
    padding: 0; border: 0; border-radius: var(--lv-radius-default, 6px);
    background: transparent; color: var(--lv-fg-muted); cursor: pointer;
  }
  .time-grouping-remove:hover { background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
  .time-grouping select:focus-visible, .time-grouping-remove:focus-visible { outline: 2px solid var(--lv-line-accent); outline-offset: 2px; }
`

/** Date grouping lives alongside filters, while remaining distinct from a date range. */
export function renderTimeGrouping(fieldLabel: string, grain: string, change: (grain: TimeGroupingGrain) => void, remove: () => void) {
  return html`<div class="time-grouping" role="group" aria-label="Date grouping">
    <span class="time-grouping-field" title=${fieldLabel}>${lucideIcon(CalendarDays, { size: 14 })}<span>${fieldLabel}</span></span>
    <label>Group by<select aria-label="Time grain" .value=${grain} @change=${(event: Event) => change((event.target as HTMLSelectElement).value as TimeGroupingGrain)}>
      ${grains.map(value => html`<option value=${value} .selected=${value === grain}>${value[0].toUpperCase() + value.slice(1)}</option>`)}
    </select></label>
    <button class="time-grouping-remove" type="button" aria-label="Remove time grouping" title="Remove time grouping" @click=${remove}>${lucideIcon(X, { size: 14 })}</button>
  </div>`
}
