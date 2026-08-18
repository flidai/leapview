import { expect, test } from 'bun:test'
import type { VisualizationEnvelope } from '../../../generated/visualization'
import { projectVisualizationHighlights } from './highlight'

test('cross-highlight matches governed source fields without changing the comparison frame', () => {
  const envelope = {
    highlights: [{
      sourceVisualID: 'source',
      interactionID: 'point_selection',
      label: 'Delivered',
      entries: [{ label: 'Delivered', mappings: [{ targetFieldID: 'orders.status', value: 'delivered' }] }],
    }],
    spec: {
      datasets: [{ id: 'primary', fields: [
        { id: 'status', sourceRef: 'orders.status', role: 'dimension', dataType: 'string', nullable: false, label: 'Status' },
        { id: 'value', sourceRef: 'revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
      ] }],
    },
  } as VisualizationEnvelope
  const rows = [['delivered', 100], ['canceled', 80]]

  const projection = projectVisualizationHighlights(envelope, 'primary', ['status', 'value'], rows)

  expect([...projection.matchedRows]).toEqual([0])
  expect(rows).toEqual([['delivered', 100], ['canceled', 80]])
  expect(projection.announcement).toContain('Comparison totals are unchanged')
})

test('spatial cross-highlight evaluates box, lasso, and radius geometry against governed coordinates', () => {
  const envelope = {
    highlights: [],
    spec: {
      datasets: [{ id: 'primary', fields: [
        { id: 'latitude', sourceRef: 'customers.latitude', role: 'identity', dataType: 'decimal', nullable: false, label: 'Latitude' },
        { id: 'longitude', sourceRef: 'customers.longitude', role: 'identity', dataType: 'decimal', nullable: false, label: 'Longitude' },
      ] }],
    },
  } as unknown as VisualizationEnvelope
  const rows = [[-23.55, -46.63], [-22.91, -43.17], [-15.79, -47.88]]
  const highlight = (spatialGeometry: unknown) => {
    envelope.highlights = [{
      sourceVisualID: 'source-map',
      interactionID: 'spatial_selection',
      entries: [],
      spatialGeometry,
      spatialLatitudeFieldID: 'customers.latitude',
      spatialLongitudeFieldID: 'customers.longitude',
      label: 'Spatial selection',
    }] as VisualizationEnvelope['highlights']
    return [...projectVisualizationHighlights(envelope, 'primary', ['latitude', 'longitude'], rows).matchedRows]
  }

  expect(highlight({ kind: 'box', bounds: { west: -48, south: -24, east: -45, north: -22 } })).toEqual([0])
  expect(highlight({ kind: 'lasso', points: [
    { longitude: -48, latitude: -24 }, { longitude: -45, latitude: -24 },
    { longitude: -45, latitude: -22 }, { longitude: -48, latitude: -22 },
  ] })).toEqual([0])
  expect(highlight({ kind: 'radius', center: { longitude: -43.17, latitude: -22.91 }, radiusMeters: 20_000 })).toEqual([1])
})
