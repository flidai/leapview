import type { LitElement } from 'lit'

/** Public interface of playground examples, never of production components. */
export interface StatefulExample extends LitElement {
  getExampleState(): Record<string, unknown>
  restoreExampleState(state: Record<string, unknown>): void | Promise<void>
  getExampleCode?(): string
}

export function stateRecord(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

/** Restore only explicitly named controls. Unknown keys cannot become instance properties. */
export function readState<T extends object>(value: unknown, defaults: T, choices: Partial<Record<keyof T, readonly string[]>> = {}): T {
  const input = stateRecord(value)
  const result = { ...defaults }
  for (const key of Object.keys(defaults) as Array<keyof T & string>) {
    const candidate = input[key]
    const fallback = defaults[key]
    if (typeof fallback === 'boolean' && typeof candidate === 'boolean') result[key] = candidate as T[typeof key]
    if (typeof fallback === 'string' && typeof candidate === 'string' && candidate.length <= 20000 && (!choices[key] || choices[key]!.includes(candidate))) result[key] = candidate as T[typeof key]
    if (typeof fallback === 'number' && typeof candidate === 'number' && Number.isFinite(candidate) && candidate >= 0 && candidate <= 100000) result[key] = candidate as T[typeof key]
    if (Array.isArray(fallback) && Array.isArray(candidate) && candidate.length <= 100 && candidate.every(item => typeof item === 'string' && item.length <= 200 && (!choices[key] || choices[key]!.includes(item)))) result[key] = [...candidate] as T[typeof key]
  }
  return result
}

export const themeModes = ['system', 'light', 'dark', 'dark_dimmed', 'light_colorblind', 'dark_colorblind', 'light_tritanopia', 'dark_tritanopia'] as const

export const snapshotKey = 'leapview-playground:reload:v1'
export interface ExampleSnapshot {
  version: 1
  route: string
  width: string
  height: string
  theme: string
  preview: boolean
  themeAttributes?: { colorMode: string; lightTheme: string; darkTheme: string; colorScheme: string }
  example: Record<string, unknown>
}

export function decodeSnapshot(raw: string | null): ExampleSnapshot | undefined {
  if (!raw || raw.length > 100000) return
  try {
    const value = stateRecord(JSON.parse(raw))
    if (value.version !== 1 || typeof value.route !== 'string' || !/^[a-z-]+\/[a-z-]+$/.test(value.route)) return
    return {
      version: 1, route: value.route,
      ...readState(value, { width: 'responsive', height: '420', theme: 'light', preview: false }, {
        width: ['responsive', '360', '768', '1200'], height: ['260', '420', '640'],
        theme: themeModes,
      }),
      example: stateRecord(value.example),
      themeAttributes: readState(value.themeAttributes, { colorMode: 'light', lightTheme: 'light', darkTheme: 'dark', colorScheme: 'light' }, {
        colorMode: ['light', 'dark', 'auto'], lightTheme: themeModes, darkTheme: themeModes, colorScheme: ['light', 'dark'],
      }),
    }
  } catch { return }
}
