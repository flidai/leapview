const MIN_CALENDAR_YEAR = 1
const MAX_CALENDAR_YEAR = 9999
const MINUTES_PER_HOUR = 60
const MILLISECONDS_PER_SECOND = 1_000
const MILLISECONDS_PER_MINUTE = 60 * MILLISECONDS_PER_SECOND

export type TimestampDateBound = {
  value: { kind: 'timestamp'; value: string }
  inclusive: boolean
}

type CalendarDate = { year: number; month: number; day: number }

/**
 * Converts a calendar date into its first valid instant in the supplied IANA
 * timezone. An upper date bound is the following day's start and is exclusive.
 */
export function timestampDateBound(
  date: string,
  timezone = 'UTC',
  upper = false,
): TimestampDateBound {
  const calendarDate = parseCalendarDate(date)
  if (!calendarDate) throw new RangeError(`invalid calendar date: ${date}`)
  if (upper && date === '9999-12-31') {
    throw new RangeError('upper bound for 9999-12-31 is outside the supported calendar range')
  }

  const target = createUTCDate(calendarDate.year, calendarDate.month, calendarDate.day)
  if (upper) target.setUTCDate(target.getUTCDate() + 1)

  const instant = localMidnight(target, timezone)
  const value = formatUTCTimestamp(instant)
  return {
    value: { kind: 'timestamp', value },
    inclusive: !upper,
  }
}

/** Returns the calendar date represented by a timestamp in an IANA timezone. */
export function timestampCalendarDate(timestamp: string, timezone = 'UTC'): string {
  const instant = parseTimestamp(timestamp)
  if (instant === undefined) return ''

  const parts = new Intl.DateTimeFormat('en-US', {
    calendar: 'gregory',
    numberingSystem: 'latn',
    timeZone: timezone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(instant)
  const year = Number(partValue(parts, 'year'))
  const month = Number(partValue(parts, 'month'))
  const day = Number(partValue(parts, 'day'))
  if (!Number.isInteger(year) || !Number.isInteger(month) || !Number.isInteger(day)) return ''
  if (year < MIN_CALENDAR_YEAR || year > MAX_CALENDAR_YEAR) return ''
  return `${String(year).padStart(4, '0')}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`
}

function parseCalendarDate(value: string): CalendarDate | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return undefined
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  if (year < MIN_CALENDAR_YEAR || year > MAX_CALENDAR_YEAR || month < 1 || month > 12) return undefined
  const candidate = createUTCDate(year, month, day)
  return candidate.getUTCFullYear() === year && candidate.getUTCMonth() === month - 1 && candidate.getUTCDate() === day
    ? { year, month, day }
    : undefined
}

function createUTCDate(year: number, month: number, day: number): Date {
  const date = new Date(0)
  date.setUTCFullYear(year, month - 1, day)
  date.setUTCHours(0, 0, 0, 0)
  return date
}

function localMidnight(calendarMidnight: Date, timezone: string): Date {
  const calendarTime = calendarMidnight.getTime()
  const day = 24 * 60 * MILLISECONDS_PER_MINUTE
  // Inspect both sides of a transition. If midnight repeats, the earliest
  // matching instant includes both occurrences in the calendar day.
  const candidates = [...new Set([-day, 0, day].map(delta =>
    calendarTime - timezoneOffsetMilliseconds(new Date(calendarTime + delta), timezone),
  ))].sort((left, right) => left - right)
  const localTime = (instant: number) => instant + timezoneOffsetMilliseconds(new Date(instant), timezone)
  const exact = candidates.find(instant => localTime(instant) === calendarTime)
  if (exact !== undefined) return new Date(exact)

  // Midnight lies in a clock-forward gap. Locate the transition itself,
  // which is the first valid instant of the day, rather than oscillating
  // between the offsets or rejecting the selected calendar date.
  let before = candidates[0]!
  let after = candidates[candidates.length - 1]!
  if (localTime(before) < calendarTime && localTime(after) > calendarTime) {
    while (after - before > 1) {
      const middle = before + Math.floor((after - before) / 2)
      if (localTime(middle) < calendarTime) before = middle
      else after = middle
    }
    return new Date(after)
  }
  throw new RangeError('The selected date boundary is not supported in this timezone.')
}

function timezoneOffsetMilliseconds(instant: Date, timezone: string): number {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: timezone,
    timeZoneName: 'longOffset',
  }).formatToParts(instant)
  const value = partValue(parts, 'timeZoneName')
  const match = /^GMT(?:(?<sign>[+-])(\d{2}):(\d{2})(?::(\d{2}))?)?$/.exec(value)
  if (!match) throw new RangeError(`timezone offset is not supported: ${value}`)
  if (!match.groups?.sign) return 0
  const hours = Number(match[2])
  const minutes = Number(match[3])
  const seconds = Number(match[4] ?? 0)
  if (hours > 23 || minutes > 59 || seconds > 59) throw new RangeError(`invalid timezone offset: ${value}`)
  const magnitude = ((hours * MINUTES_PER_HOUR + minutes) * MILLISECONDS_PER_MINUTE) + seconds * MILLISECONDS_PER_SECOND
  return match.groups.sign === '+' ? magnitude : -magnitude
}

function formatUTCTimestamp(instant: Date): string {
  const year = instant.getUTCFullYear()
  if (year < 0 || year > MAX_CALENDAR_YEAR) {
    throw new RangeError('timestamp is outside the supported RFC3339 year range')
  }
  return `${String(year).padStart(4, '0')}-${String(instant.getUTCMonth() + 1).padStart(2, '0')}-${String(instant.getUTCDate()).padStart(2, '0')}T${String(instant.getUTCHours()).padStart(2, '0')}:${String(instant.getUTCMinutes()).padStart(2, '0')}:${String(instant.getUTCSeconds()).padStart(2, '0')}Z`
}

function parseTimestamp(value: string): Date | undefined {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value)) return undefined
  const time = Date.parse(value)
  return Number.isFinite(time) ? new Date(time) : undefined
}

function partValue(parts: Intl.DateTimeFormatPart[], type: Intl.DateTimeFormatPartTypes): string {
  return parts.find(part => part.type === type)?.value ?? ''
}
