import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronLeft, ChevronRight, CalendarDays } from 'lucide'
import { lucideIcon } from '../../shared/lucide-icons'
import { toggleAnchoredPopover } from '../../shared/anchored-popover'

export type DatePickerInputDetail = {
  value: string
  displayValue: string
}

const MONTHS = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
]
const WEEKDAYS = ['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa']

export class DashboardDatePicker extends LitElement {
  @property() value = ''
  @property() label = 'Date'
  @property() placeholder = 'No date'
  @property() weekStart = 'monday'
  @property() error = ''
  @property({ type: Boolean }) invalid = false
  @property({ type: Boolean }) disabled = false

  @state() private open = false
  @state() private viewYear = new Date().getFullYear()
  @state() private viewMonth = new Date().getMonth()

  static styles = css`
    :host { display: block; min-width: 0; color: inherit; font: inherit; }
    .visually-hidden {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0 0 0 0);
      clip-path: inset(50%);
      white-space: nowrap;
    }
    .control { display: flex; min-width: 0; }
    .date-trigger {
      display: flex;
      width: 100%;
      min-width: 0;
      min-height: var(--control-medium-size);
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: inherit;
      cursor: pointer;
      padding: 0 var(--base-size-8);
      text-align: left;
      font: var(--lv-type-body-compact);
    }
    .date-trigger:hover:not(:disabled) { background: var(--lv-bg-control-hover); }
    .date-trigger:disabled { cursor: default; opacity: .55; }
    .date-trigger[data-invalid='true'] { border-color: var(--lv-fg-danger, var(--fgColor-danger)); }
    .date-value { min-width: 0; overflow: hidden; color: inherit; text-overflow: ellipsis; white-space: nowrap; }
    .date-placeholder { color: var(--lv-fg-muted); }
    .date-trigger svg { width: var(--base-size-16); height: var(--base-size-16); flex: 0 0 auto; color: var(--lv-fg-muted); }
    .date-popover {
      position: fixed;
      inset: auto;
      z-index: var(--zIndex-popover, 300);
      top: 0;
      left: 0;
      display: none;
      width: 272px;
      max-width: calc(100vw - var(--base-size-16));
      box-sizing: border-box;
      overflow: hidden;
      margin: 0;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-overlay, var(--lv-bg-panel));
      color: var(--lv-fg-default);
      box-shadow: var(--lv-shadow-floating-lg, var(--shadow-floating-small));
      padding: var(--base-size-8);
    }
    .date-popover:popover-open { display: grid; gap: var(--base-size-8); }
    .calendar-header {
      display: grid;
      grid-template-columns: var(--lv-control-compact, var(--control-medium-size)) minmax(0, 1fr) 4.5rem var(--lv-control-compact, var(--control-medium-size));
      align-items: center;
      gap: var(--base-size-4);
    }
    .month-label {
      min-width: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      text-align: center;
    }
    .calendar-header button,
    .calendar-footer button {
      min-height: var(--lv-control-compact, var(--control-medium-size));
      border: 0;
      border-radius: var(--lv-radius-tight, var(--lv-radius-default));
      background: transparent;
      color: inherit;
      cursor: pointer;
      font: var(--lv-type-body-compact);
    }
    .calendar-header button:hover:not(:disabled),
    .calendar-footer button:hover:not(:disabled) { background: var(--lv-bg-control-hover); }
    .calendar-header button:disabled,
    .calendar-footer button:disabled { cursor: default; opacity: .45; }
    .calendar-header svg { width: var(--base-size-16); height: var(--base-size-16); }
    .year-control {
      width: 4.5rem;
      min-height: var(--lv-control-compact, var(--control-medium-size));
      justify-self: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-tight, var(--lv-radius-default));
      background: var(--lv-bg-panel);
      color: inherit;
      box-sizing: border-box;
      padding: 0 var(--base-size-4);
      text-align: center;
      font: var(--lv-type-body-compact);
    }
    .year-label { display: block; }
    .calendar-weekdays,
    .calendar-grid {
      display: grid;
      grid-template-columns: repeat(7, minmax(0, 1fr));
      gap: 2px;
    }
    .calendar-weekday {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }
    .calendar-day {
      display: grid;
      min-width: 0;
      min-height: var(--lv-control-compact, var(--control-medium-size));
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-tight, var(--lv-radius-default));
      background: transparent;
      color: inherit;
      cursor: pointer;
      padding: 0;
      font: var(--lv-type-body-compact);
    }
    .calendar-day:hover { background: var(--lv-bg-control-hover); }
    .calendar-day[data-outside-month='true'] { color: var(--lv-fg-muted); }
    .calendar-day[data-selected='true'] {
      background: var(--lv-button-accent-bg-rest);
      color: var(--lv-button-accent-fg-rest);
      font-weight: var(--base-text-weight-semibold);
    }
    .calendar-day[data-today='true']:not([data-selected='true']) { box-shadow: inset 0 0 0 var(--borderWidth-default) var(--lv-line-accent); }
    .calendar-footer { display: flex; justify-content: flex-end; }
    .calendar-footer button { color: var(--lv-fg-muted); padding-inline: var(--base-size-6); }
    button:focus-visible, input:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-accent); outline-offset: var(--base-size-2); }
    @media (max-width: 320px) {
      .date-popover { padding: var(--base-size-6); }
      .calendar-day { min-height: var(--control-small-size); }
    }
  `

  protected updated(changed: Map<PropertyKey, unknown>): void {
    if (!changed.has('value')) return
    const parsed = parseCanonicalDate(this.value)
    if (!parsed) return
    if (!this.open) {
      this.viewYear = parsed.year
      this.viewMonth = parsed.month
    }
  }

  render() {
    const displayValue = formatDisplayDate(this.value)
    const triggerLabel = displayValue ? `${this.label}, ${displayValue}` : this.label
    return html`
      <div class="control">
        <button
          class="date-trigger"
          type="button"
          data-invalid=${String(this.invalid)}
          aria-label=${triggerLabel}
          aria-invalid=${String(this.invalid)}
          aria-haspopup="dialog"
          aria-expanded=${String(this.open)}
          aria-describedby=${this.error ? 'date-picker-error' : nothing}
          ?disabled=${this.disabled}
          @click=${this.toggleDatePopover}
        >
          <span class=${displayValue ? 'date-value' : 'date-value date-placeholder'}>${displayValue || this.placeholder}</span>
          ${lucideIcon(CalendarDays)}
        </button>
        ${this.error ? html`<span id="date-picker-error" class="visually-hidden">${this.error}</span>` : nothing}
      </div>
      <div
        class="date-popover"
        popover="auto"
        role="dialog"
        aria-label=${`${this.label} calendar`}
        @toggle=${this.onPopoverToggle}
        @keydown=${this.onKeyDown}
      >
        <div class="calendar-header">
          <button type="button" aria-label="Previous month" ?disabled=${this.viewYear === 1 && this.viewMonth === 0} @click=${this.previousMonth}>${lucideIcon(ChevronLeft)}</button>
          <div class="month-label" aria-live="polite">${MONTHS[this.viewMonth]}</div>
          <label class="year-label"><span class="visually-hidden">Year</span><input class="year-control" aria-label="Year" type="number" min="1" max="9999" .value=${String(this.viewYear)} @change=${this.changeYear}></label>
          <button type="button" aria-label="Next month" ?disabled=${this.viewYear === 9999 && this.viewMonth === 11} @click=${this.nextMonth}>${lucideIcon(ChevronRight)}</button>
        </div>
        <div class="calendar-weekdays" aria-hidden="true">
          ${this.weekdayLabels().map(day => html`<span class="calendar-weekday">${day}</span>`)}
        </div>
        <div class="calendar-grid" role="grid" aria-label=${`${MONTHS[this.viewMonth]} ${this.viewYear}`}>
          ${this.calendarDays().map((day, index) => {
            const selected = day.value === this.value
            const today = day.value === todayValue()
            return html`
              <button
                class="calendar-day"
                type="button"
                role="gridcell"
                data-date=${day.value}
                data-outside-month=${String(day.month !== this.viewMonth)}
                data-selected=${String(selected)}
                data-today=${String(today)}
                tabindex=${this.calendarTabIndex(day, index)}
                aria-label=${`Select ${formatDisplayDate(day.value)}`}
                aria-selected=${String(selected)}
                @click=${() => this.selectDate(day.value)}
              >${day.day}</button>
            `
          })}
        </div>
        ${this.value ? html`<div class="calendar-footer"><button type="button" @click=${this.clearDate}>Clear date</button></div>` : nothing}
      </div>
    `
  }

  private toggleDatePopover = (): void => {
    const popover = this.renderRoot.querySelector<HTMLElement>('.date-popover')
    const trigger = this.renderRoot.querySelector<HTMLElement>('.date-trigger')
    if (!popover || !trigger || this.disabled) return
    if (this.open) {
      popover.hidePopover()
      this.open = false
      return
    }
    const selected = parseCanonicalDate(this.value)
    const now = selected ?? parseCanonicalDate(todayValue())!
    this.viewYear = now.year
    this.viewMonth = now.month
    this.open = toggleAnchoredPopover(trigger, popover, { minWidth: 272, maxWidth: 320, maxHeight: 360 })
    if (this.open) queueMicrotask(() => this.renderRoot.querySelector<HTMLInputElement>('.year-control')?.focus())
  }

  private onPopoverToggle = (event: Event): void => {
    this.open = (event as Event & { newState?: string }).newState === 'open'
  }

  private onKeyDown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      this.renderRoot.querySelector<HTMLElement>('.date-popover')?.hidePopover()
      this.renderRoot.querySelector<HTMLElement>('.date-trigger')?.focus()
      this.dispatchEvent(new CustomEvent('lv-date-escape', { bubbles: true, composed: true }))
      return
    }
    const target = event.composedPath()[0]
    if (event.key === 'Enter' && target instanceof HTMLInputElement && target.classList.contains('year-control')) {
      event.preventDefault()
      event.stopPropagation()
      return
    }
    if (event.key === 'Enter' && target instanceof HTMLButtonElement && target.classList.contains('calendar-day')) {
      event.preventDefault()
      event.stopPropagation()
      target.click()
      return
    }
    if (target instanceof HTMLButtonElement && target.classList.contains('calendar-day')) {
      const current = [...this.renderRoot.querySelectorAll<HTMLButtonElement>('.calendar-day')].indexOf(target)
      if (current < 0) return
      const delta = event.key === 'ArrowLeft' ? -1 : event.key === 'ArrowRight' ? 1 : event.key === 'ArrowUp' ? -7 : event.key === 'ArrowDown' ? 7 : 0
      if (delta === 0) return
      event.preventDefault()
      this.renderRoot.querySelectorAll<HTMLButtonElement>('.calendar-day')[Math.max(0, Math.min(41, current + delta))]?.focus()
    }
  }

  private previousMonth = (): void => {
    if (this.viewMonth === 0) {
      this.viewMonth = 11
      this.viewYear = Math.max(1, this.viewYear - 1)
    } else this.viewMonth -= 1
  }

  private nextMonth = (): void => {
    if (this.viewMonth === 11) {
      if (this.viewYear === 9999) return
      this.viewYear += 1
      this.viewMonth = 0
    } else this.viewMonth += 1
  }

  private changeYear = (event: Event): void => {
    const input = event.currentTarget as HTMLInputElement
    const year = Number.parseInt(input.value, 10)
    if (!Number.isInteger(year) || year < 1 || year > 9999) {
      input.value = String(this.viewYear)
      return
    }
    this.viewYear = year
  }

  private selectDate = (value: string): void => {
    this.value = value
    this.dispatchInput(value)
    this.renderRoot.querySelector<HTMLElement>('.date-popover')?.hidePopover()
    this.open = false
    queueMicrotask(() => this.renderRoot.querySelector<HTMLElement>('.date-trigger')?.focus())
  }

  private clearDate = (): void => {
    this.value = ''
    this.dispatchInput('')
    this.renderRoot.querySelector<HTMLElement>('.date-popover')?.hidePopover()
    this.open = false
    queueMicrotask(() => this.renderRoot.querySelector<HTMLElement>('.date-trigger')?.focus())
  }

  private dispatchInput(value: string): void {
    this.dispatchEvent(new CustomEvent<DatePickerInputDetail>('lv-date-input', {
      bubbles: true,
      composed: true,
      detail: { value, displayValue: formatDisplayDate(value) },
    }))
  }

  private calendarDays(): CalendarDay[] {
    const first = new Date(Date.UTC(this.viewYear, this.viewMonth, 1))
    const firstDayOffset = this.weekStart.toLowerCase() === 'sunday'
      ? first.getUTCDay()
      : (first.getUTCDay() + 6) % 7
    const start = new Date(Date.UTC(this.viewYear, this.viewMonth, 1 - firstDayOffset))
    return Array.from({ length: 42 }, (_, index) => {
      const date = new Date(start.getTime() + index * 86_400_000)
      const value = formatCanonicalDate(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate())
      return { value, day: date.getUTCDate(), month: date.getUTCMonth() }
    })
  }

  private weekdayLabels(): string[] {
    return this.weekStart.toLowerCase() === 'sunday' ? WEEKDAYS : [...WEEKDAYS.slice(1), WEEKDAYS[0]]
  }

  private calendarTabIndex(day: CalendarDay, index: number): number {
    const selectedIndex = this.calendarDays().findIndex(candidate => candidate.value === this.value)
    return (selectedIndex >= 0 ? index === selectedIndex : index === 0) ? 0 : -1
  }
}

type CalendarDay = { value: string; day: number; month: number }
type ParsedDate = { year: number; month: number; day: number }

export function parseCanonicalDate(value: string): ParsedDate | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return undefined
  const year = Number(match[1])
  const month = Number(match[2]) - 1
  const day = Number(match[3])
  const date = new Date(Date.UTC(year, month, day))
  return date.getUTCFullYear() === year && date.getUTCMonth() === month && date.getUTCDate() === day
    ? { year, month, day }
    : undefined
}

export function formatCanonicalDate(year: number, month: number, day: number): string {
  return `${String(year).padStart(4, '0')}-${String(month + 1).padStart(2, '0')}-${String(day).padStart(2, '0')}`
}

export function formatDisplayDate(value: string): string {
  const date = parseCanonicalDate(value)
  return date ? `${String(date.day).padStart(2, '0')}-${String(date.month + 1).padStart(2, '0')}-${String(date.year).padStart(4, '0')}` : ''
}

function todayValue(): string {
  const today = new Date()
  return formatCanonicalDate(today.getFullYear(), today.getMonth(), today.getDate())
}

if (!customElements.get('lv-date-picker')) customElements.define('lv-date-picker', DashboardDatePicker)
