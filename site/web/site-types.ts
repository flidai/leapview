import type { VisualizationEnvelope } from '../../web/generated/visualization'

export type VisualPayload = VisualizationEnvelope

export type VisualShowcaseDocument = {
  slug: string
  title: string
  visualID: string
}
