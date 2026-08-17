import type { FeatureCollection } from 'geojson'
import type { GeoJSONSource } from 'maplibre-gl'
import type { VisualizationEnvelope, VisualizationGeographicLayer } from '../../../../../generated/visualization'
import { interactionSelectionLabel, interactionSelectionValue, type OptimisticInteractionCommand } from '../../../interaction-selection'
import { clearInteractionCommand, interactionCommandForRowIndex } from '../../interaction-command'
import { coordinateGeometry, joinGeometry, pathGeometry } from './data'
import { applyFeatureScales } from './layers'
import type { RenderedFeatureLocator } from './overlays'

type ClusterFeatureLocator = RenderedFeatureLocator & Readonly<{ geometry?: { type?: string; coordinates?: unknown } }>

export function clusterExpansionForRenderedFeatures(
  features: readonly ClusterFeatureLocator[],
  sourceByLayer: ReadonlyMap<string, string>,
): { sourceID: string; clusterID: number; center: [number, number] } | undefined {
  for (const feature of features) {
    const layerID = feature.layer?.id
    const clusterID = feature.properties?.cluster_id
    const coordinates = feature.geometry?.coordinates
    if (typeof layerID !== 'string' || !Number.isInteger(clusterID) || (clusterID as number) < 0) continue
    if (!Array.isArray(coordinates) || coordinates.length < 2 || !coordinates.slice(0, 2).every(Number.isFinite)) continue
    const sourceID = sourceByLayer.get(layerID)
    if (sourceID) return { sourceID, clusterID: clusterID as number, center: [coordinates[0] as number, coordinates[1] as number] }
  }
  return undefined
}

export function interactionCommandForRenderedFeatures(
  envelope: VisualizationEnvelope,
  features: readonly RenderedFeatureLocator[],
  selectableLayerIDs: readonly string[],
) {
  const selectable = new Set(selectableLayerIDs)
  for (const feature of features) {
    const renderedLayerID = feature.layer?.id
    const datasetID = feature.properties?.__lv_dataset
    const rowIndex = feature.properties?.__lv_row_index
    const authoredLayerID = feature.properties?.__lv_layer_id
    if (typeof renderedLayerID !== 'string' || !selectable.has(renderedLayerID)) continue
    if (renderedLayerID !== `lv-${authoredLayerID}` || typeof datasetID !== 'string' || typeof rowIndex !== 'number') continue
    const command = interactionCommandForRowIndex(envelope, datasetID, rowIndex)
    if (command) return command
  }
  return undefined
}

export function mapInteractionCommand(
  envelope: VisualizationEnvelope,
  features: readonly RenderedFeatureLocator[],
  selectableLayerIDs: readonly string[],
): OptimisticInteractionCommand | undefined {
	if (envelope.dataState.kind === 'spatial_tiled') {
		const selectable = new Set(selectableLayerIDs)
		for (const feature of features) {
			if (!feature.layer?.id || !selectable.has(feature.layer.id) || feature.properties?.__lv_aggregate === true) continue
			const interaction = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')
			if (!interaction) continue
			const mappings = interaction.mappings.map((mapping) => {
				const value = interactionSelectionValue(feature.properties?.[mapping.source.field])
				const label = interactionSelectionValue(feature.properties?.[mapping.label?.field ?? mapping.source.field])
				if (value === undefined || label === undefined) return undefined
				return { field: mapping.targetFieldID, ...(mapping.targetDatasetID ? { dataset: mapping.targetDatasetID } : {}), ...(mapping.grain ? { grain: mapping.grain } : {}), value, label: interactionSelectionLabel(label) }
			})
			if (mappings.some((mapping) => mapping === undefined)) continue
			return { sourceKind: 'visual', sourceId: envelope.visualID, interactionKind: interaction.id, action: 'set', toggle: interaction.mode === 'multiple', mappings: mappings as OptimisticInteractionCommand['mappings'] }
		}
	}
  return interactionCommandForRenderedFeatures(envelope, features, selectableLayerIDs)
    ?? (envelope.selection.length > 0 ? clearInteractionCommand(envelope) : undefined)
}

export function updateSelectionSources(
  envelope: VisualizationEnvelope,
  layers: readonly { spec: VisualizationGeographicLayer; sourceID: string; geometry?: FeatureCollection }[],
  getSource: (sourceID: string) => Pick<GeoJSONSource, 'setData'> | undefined,
): { updated: number; collections: FeatureCollection[] } {
  let updated = 0
  const collections: FeatureCollection[] = []
  for (const layer of layers) {
    const data = layer.spec.kind === 'choropleth' && layer.geometry
      ? joinGeometry(envelope, layer.spec, layer.geometry)
      : layer.spec.kind === 'path'
        ? pathGeometry(envelope, layer.spec)
        : layer.spec.kind === 'reference' && layer.geometry
          ? layer.geometry
          : coordinateGeometry(envelope, layer.spec)
    const scaled = applyFeatureScales(data, layer.spec)
    collections.push(scaled)
    const source = getSource(layer.sourceID)
    if (!source) continue
    source.setData(scaled)
    updated++
  }
  return { updated, collections }
}
