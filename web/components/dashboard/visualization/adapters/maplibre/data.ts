import type { Feature, FeatureCollection, Geometry, Position } from 'geojson'
import type { VisualizationEnvelope, VisualizationGeographicLayer } from '../../../../../generated/visualization'
import { projectVisualizationHighlights } from '../../highlight'

export type GeographicDataset = { id: string; columns: string[]; rows: unknown[][] }

export function geographicDataset(envelope: VisualizationEnvelope, datasetID: string): GeographicDataset | undefined {
  if (envelope.dataState.kind === 'inline') return envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
  if (envelope.dataState.kind === 'spatial_windowed' && envelope.dataState.schema.id === datasetID && envelope.dataState.window) {
    return { id: datasetID, columns: envelope.dataState.schema.fields.map((field) => field.id), rows: envelope.dataState.window.rows }
  }
  return undefined
}

export function joinGeometry(envelope: VisualizationEnvelope, layer: VisualizationGeographicLayer, geometry: FeatureCollection): FeatureCollection {
  if (envelope.dataState.kind !== 'inline' || layer.kind !== 'choropleth') return geometry
  const join = layer.join
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === join.dataset)
  if (!dataset) return geometry
  const joinIndex = dataset.columns.indexOf(join.field)
  const valueIndex = layer.value ? dataset.columns.indexOf(layer.value.field) : -1
  const categoryIndex = layer.category ? dataset.columns.indexOf(layer.category.field) : -1
  const labelIndex = layer.label ? dataset.columns.indexOf(layer.label.field) : -1
  const highlight = projectVisualizationHighlights(envelope, dataset.id, dataset.columns, dataset.rows)
  const values = new Map(dataset.rows.map((row, rowIndex) => [String(row[joinIndex]), {
    value: valueIndex >= 0 ? row[valueIndex] : 1,
    category: categoryIndex >= 0 ? row[categoryIndex] : null,
    label: labelIndex >= 0 ? String(row[labelIndex] ?? '') : '',
    selected: rowIsSelected(envelope, dataset.id, dataset.columns, row),
    highlighted: highlight.matchedRows.has(rowIndex),
    rowIndex,
  }]))
  const features: Feature<Geometry>[] = geometry.features.map((feature) => {
    const matched = values.get(String(feature.id ?? feature.properties?.id))
    return { ...feature, properties: {
      ...feature.properties,
      __lv_value: matched?.value ?? null,
      __lv_category: matched?.category ?? null,
      __lv_label: matched?.label ?? '',
      __lv_selected: matched?.selected ?? false,
      __lv_has_selection: envelope.selection.length > 0,
      __lv_highlighted: matched?.highlighted ?? false,
      __lv_has_highlight: highlight.active,
      ...(matched ? rowLocator(dataset.id, matched.rowIndex, layer.id) : {}),
    } }
  })
  return { ...geometry, features }
}

export function coordinateGeometry(envelope: VisualizationEnvelope, layer: VisualizationGeographicLayer): FeatureCollection {
  if (!['point', 'heat', 'density'].includes(layer.kind)) return { type: 'FeatureCollection', features: [] }
  const coordinateLayer = layer as Extract<VisualizationGeographicLayer, { kind: 'point' | 'heat' | 'density' }>
  const dataset = geographicDataset(envelope, coordinateLayer.latitude.dataset)
  if (coordinateLayer.latitude.dataset !== coordinateLayer.longitude.dataset || !dataset) return { type: 'FeatureCollection', features: [] }
  const latitudeIndex = dataset.columns.indexOf(coordinateLayer.latitude.field)
  const longitudeIndex = dataset.columns.indexOf(coordinateLayer.longitude.field)
  const valueIndex = coordinateLayer.value ? dataset.columns.indexOf(coordinateLayer.value.field) : -1
  const categoryIndex = coordinateLayer.kind === 'point' && coordinateLayer.category ? dataset.columns.indexOf(coordinateLayer.category.field) : -1
  const labelIndex = coordinateLayer.label ? dataset.columns.indexOf(coordinateLayer.label.field) : -1
  const features: Feature<Geometry>[] = []
  const highlight = projectVisualizationHighlights(envelope, dataset.id, dataset.columns, dataset.rows)
  const selectableRows = envelope.dataState.kind !== 'spatial_windowed' || envelope.dataState.window?.precision === 'raw'
  for (let index = 0; index < dataset.rows.length; index++) {
    const row = dataset.rows[index]!
    const latitude = row[latitudeIndex], longitude = row[longitudeIndex]
    if (!validCoordinate(latitude, longitude)) continue
    features.push({ type: 'Feature', id: index, geometry: { type: 'Point', coordinates: [longitude as number, latitude as number] }, properties: {
      __lv_value: valueIndex >= 0 ? row[valueIndex] : 1,
      __lv_category: categoryIndex >= 0 ? row[categoryIndex] : null,
      __lv_label: labelIndex >= 0 ? String(row[labelIndex] ?? '') : '',
      __lv_selected: rowIsSelected(envelope, dataset.id, dataset.columns, row),
      __lv_has_selection: envelope.selection.length > 0,
      __lv_highlighted: highlight.matchedRows.has(index),
      __lv_has_highlight: highlight.active,
      ...((layer.kind === 'point' || layer.tooltip.length > 0) && selectableRows ? rowLocator(dataset.id, index, layer.id) : {}),
    } })
  }
  return { type: 'FeatureCollection', features }
}

export function pathGeometry(envelope: VisualizationEnvelope, layer: Extract<VisualizationGeographicLayer, { kind: 'path' }>): FeatureCollection {
  const dataset = geographicDataset(envelope, layer.latitude.dataset)
  if (!dataset) return { type: 'FeatureCollection', features: [] }
  const latitudeIndex = dataset.columns.indexOf(layer.latitude.field), longitudeIndex = dataset.columns.indexOf(layer.longitude.field)
  const pathIndex = dataset.columns.indexOf(layer.path.field), orderIndex = dataset.columns.indexOf(layer.order.field)
  const valueIndex = layer.value ? dataset.columns.indexOf(layer.value.field) : -1
  const categoryIndex = layer.category ? dataset.columns.indexOf(layer.category.field) : -1
  const grouped = new Map<string, Array<{ coordinate: Position; order: unknown; value: unknown; category: unknown; rowIndex: number }>>()
  const highlight = projectVisualizationHighlights(envelope, dataset.id, dataset.columns, dataset.rows)
  for (let rowIndex = 0; rowIndex < dataset.rows.length; rowIndex++) {
    const row = dataset.rows[rowIndex]!
    const latitude = row[latitudeIndex], longitude = row[longitudeIndex]
    if (!validCoordinate(latitude, longitude)) continue
    const key = String(row[pathIndex])
    const points = grouped.get(key) ?? []
    points.push({ coordinate: [longitude as number, latitude as number], order: row[orderIndex], value: valueIndex >= 0 ? row[valueIndex] : 1, category: categoryIndex >= 0 ? row[categoryIndex] : null, rowIndex })
    grouped.set(key, points)
  }
  const features: Feature<Geometry>[] = []
  const locatableRows = envelope.dataState.kind !== 'spatial_windowed' || envelope.dataState.window?.precision === 'raw'
  for (const [id, points] of grouped) {
    points.sort((a, b) => String(a.order).localeCompare(String(b.order), undefined, { numeric: true }))
    if (points.length < 2) continue
    const last = points.at(-1)!
    // MapLibre's GeoJSON worker accepts string feature IDs into source tiles,
    // but does not paint those LineStrings. Keep the semantic path identity in
    // __lv_path and use the deterministic result order for the renderer ID.
    features.push({ type: 'Feature', id: features.length, geometry: { type: 'LineString', coordinates: points.map((point) => point.coordinate) }, properties: {
      __lv_value: last.value ?? 1,
      __lv_category: last.category ?? null,
      __lv_path: id,
      __lv_highlighted: points.some((point) => highlight.matchedRows.has(point.rowIndex)),
      __lv_has_highlight: highlight.active,
      ...(locatableRows ? rowLocator(dataset.id, last.rowIndex, layer.id) : {}),
    } })
  }
  return { type: 'FeatureCollection', features }
}

function validCoordinate(latitude: unknown, longitude: unknown): latitude is number {
  return typeof latitude === 'number' && Number.isFinite(latitude) && latitude >= -90 && latitude <= 90
    && typeof longitude === 'number' && Number.isFinite(longitude) && longitude >= -180 && longitude <= 180
}

function rowLocator(datasetID: string, rowIndex: number, layerID: string): Record<string, string | number> {
  return { __lv_dataset: datasetID, __lv_row_index: rowIndex, __lv_layer_id: layerID }
}

function rowIsSelected(envelope: VisualizationEnvelope, datasetID: string, columns: string[], row: unknown[]): boolean {
  if (envelope.selection.length === 0) return false
  return envelope.selection.some(({ datum }) => {
    if (datum.dataset !== datasetID || datum.dataRevision !== envelope.dataRevision) return false
    return Object.entries(datum.identity).every(([field, value]) => {
      const index = columns.indexOf(field)
      return index >= 0 && Object.is(row[index], value)
    })
  })
}
