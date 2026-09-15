import type { DashboardCompiledFilterDefinition, DashboardFilterExpression, DashboardFilterValue } from '../../../generated/signals'
import { timestampCalendarDate } from './timestamp-date-range'

export type RangeDraft = {
  lower: string
  upper: string
  baseExpression: string
  dirty: boolean
}

export function rangeDraftFromExpression(expression: DashboardFilterExpression, timezone = 'UTC'): RangeDraft {
  const dateValue = (bound: { value: DashboardFilterValue; inclusive: boolean } | undefined, upper: boolean) => {
    if (!bound) return ''
    const value = String(bound.value.value)
    if (bound.value.kind !== 'timestamp') return value
    const instant = upper && !bound.inclusive ? new Date(Date.parse(value) - 1).toISOString() : value
    return timestampCalendarDate(instant, timezone)
  }
  return {
    lower: expression.kind === 'range' ? dateValue(expression.lower, false) : '',
    upper: expression.kind === 'range' ? dateValue(expression.upper, true) : '',
    baseExpression: JSON.stringify(expression),
    dirty: false,
  }
}

export function rangeValidationMessage(lower: string, upper: string, valueKind?: DashboardCompiledFilterDefinition['valueKind']): string {
  if (!lower || !upper) return ''
  if (valueKind === 'date' || valueKind === 'timestamp') {
    return lower > upper ? 'Start date must be on or before end date.' : ''
  }
  const lowerNumber = Number(lower)
  const upperNumber = Number(upper)
  return Number.isFinite(lowerNumber) && Number.isFinite(upperNumber) && lowerNumber > upperNumber
    ? 'Minimum must be less than or equal to maximum.'
    : ''
}
