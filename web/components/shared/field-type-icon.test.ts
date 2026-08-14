import { expect, test } from 'bun:test'
import { Binary, Braces, CalendarDays, CircleHelp, Clock3, Hash, ToggleLeft, Type } from 'lucide'
import { fieldTypeIcon } from './field-type-icon'

test('field type icons distinguish common data families', () => {
  expect(fieldTypeIcon('VARCHAR')).toBe(Type)
  expect(fieldTypeIcon('DOUBLE')).toBe(Hash)
  expect(fieldTypeIcon('DATE')).toBe(CalendarDays)
  expect(fieldTypeIcon('TIMESTAMP')).toBe(Clock3)
  expect(fieldTypeIcon('BOOLEAN')).toBe(ToggleLeft)
  expect(fieldTypeIcon('JSON')).toBe(Braces)
  expect(fieldTypeIcon('BLOB')).toBe(Binary)
  expect(fieldTypeIcon('GEOMETRY')).toBe(CircleHelp)
})
