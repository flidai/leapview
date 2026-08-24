/**
 * Type definitions for Datastar Inspector
 */

/**
 * Persisted inspector state (stored in sessionStorage)
 */
export interface InspectorState {
  /** Whether the panel is expanded */
  expanded: boolean
  /** Current filter text */
  filter: string
  /** Expanded tree paths */
  expandedPaths?: string[]
  /** User-positioned collapsed launcher coordinates */
  togglePosition?: InspectorPosition
  /** User-positioned expanded panel coordinates */
  panelPosition?: InspectorPosition
}

export interface InspectorPosition {
  x: number
  y: number
}

/**
 * Signal data structure (recursive key-value)
 */
export type SignalValue = string | number | boolean | null | SignalValue[] | SignalObject

export interface SignalObject {
  [key: string]: SignalValue
}
