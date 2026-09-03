import type { FeatureCollection } from 'geojson'
import type { GeoJSONSource } from 'maplibre-gl'
import type { VisualizationEnvelope, VisualizationGeographicLayer } from '../../../../../generated/visualization'
import { interactionSelectionLabel, interactionSelectionValue, type OptimisticInteractionCommand } from '../../../interaction-selection'
import { clearInteractionCommand, commandIdentityKey, interactionCommandForRowIndex, interactionCommandSelected, type InteractionOption } from '../../interaction-command'
import { coordinateGeometry, joinGeometry, pathGeometry } from './data'
import { applyFeatureScales } from './layers'
import type { RenderedFeatureLocator } from './overlays'

type ClusterFeatureLocator = RenderedFeatureLocator & Readonly<{ geometry?: { type?: string; coordinates?: unknown } }>

export function aggregateExpansionCamera(properties: Record<string, unknown> | null | undefined): { center: [number, number]; zoom: number } | undefined {
	const west = properties?.__lv_west, south = properties?.__lv_south, east = properties?.__lv_east, north = properties?.__lv_north, targetZoom = properties?.__lv_target_zoom
	if (![west, south, east, north, targetZoom].every((value) => typeof value === 'number' && Number.isFinite(value))) return undefined
	let longitudeSpan = (east as number) - (west as number)
	if (longitudeSpan < 0) longitudeSpan += 360
	let longitude = (west as number) + longitudeSpan / 2
	if (longitude >= 180) longitude -= 360
	return { center: [longitude, ((south as number) + (north as number)) / 2], zoom: targetZoom as number }
}

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

export function mapInteractionOptions(
  envelope: VisualizationEnvelope,
  features: readonly RenderedFeatureLocator[],
  selectableLayerIDs: readonly string[],
  maximumOptions = 100,
): InteractionOption[] {
  if (envelope.dataState.kind !== 'spatial_tiled') return []
  const unique = new Map<string, InteractionOption>()
  const aggregateOptions: Array<InteractionOption & { value: number }> = []
  const pointLayer = envelope.spec.kind === 'geographic'
    ? envelope.spec.layers.find((layer): layer is Extract<VisualizationGeographicLayer, { kind: 'point' }> => layer.kind === 'point')
    : undefined
  const valueField = pointLayer?.value?.field ?? '__lv_count'
  for (const feature of features) {
    if (feature.properties?.__lv_aggregate === true) {
      const refinement = aggregateExpansionCamera(feature.properties)
      if (!refinement) continue
      const value = Number(feature.properties?.[valueField] ?? feature.properties?.__lv_coordinate_count ?? 0)
      const key = `refine:${refinement.zoom}:${refinement.center.join(':')}`
      if (!aggregateOptions.some((option) => option.key === key)) aggregateOptions.push({ key, label: '', selected: false, refinement, value: Number.isFinite(value) ? value : 0 })
      continue
    }
    const command = mapInteractionCommand(envelope, [feature], selectableLayerIDs)
    if (!command || command.action !== 'set') continue
    const key = commandIdentityKey(command)
    if (unique.has(key)) continue
    const label = command.mappings.map((mapping) => mapping.label || interactionSelectionLabel(mapping.value)).filter(Boolean).join(' · ')
    unique.set(key, {
      key,
      label: label || 'Unlabeled value',
      command,
      selected: feature.properties?.__lv_selected === true || interactionCommandSelected(envelope, command),
    })
    if (unique.size >= maximumOptions) break
  }
  const options = [...unique.values()]
  const labelCounts = new Map<string, number>()
  for (const option of options) labelCounts.set(option.label, (labelCounts.get(option.label) ?? 0) + 1)
  const labeledOptions = options.map((option) => {
    if ((labelCounts.get(option.label) ?? 0) < 2) return option
    const identity = option.command?.mappings.map((mapping) => interactionSelectionLabel(mapping.value)).filter(Boolean).join(' · ')
    return { ...option, label: identity && identity !== option.label ? `${option.label} · ${identity}` : option.label }
  })
  const areas = aggregateOptions
    .sort((left, right) => right.value - left.value || left.key.localeCompare(right.key))
    .slice(0, maximumOptions)
    .map((option, index) => ({ ...option, label: `Zoom to area ${index + 1} · ${abbreviatedSelectionValue(option.value)} ${Math.round(option.value) === 1 ? 'order' : 'orders'}` }))
  return labeledOptions.length > 0 ? labeledOptions : areas
}

function abbreviatedSelectionValue(value: number): string {
  if (value >= 1_000_000) return `${Number((value / 1_000_000).toFixed(1))}M`
  if (value >= 1_000) return `${Number((value / 1_000).toFixed(1))}k`
  return String(Math.round(value))
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
