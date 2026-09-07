import type { FeatureCollection } from 'geojson'
import type { SpatialTiledVisualizationDataState, VisualizationGeographicLayer } from '../../../../../generated/visualization'

export function tiledPrecisionLayerIDs(family: 'hidden' | 'raw' | 'aggregate', rawLayerIDs: readonly string[], aggregateLayerIDs: readonly string[], candidates?: readonly string[]): string[] {
	const layerIDs = family === 'raw' ? [...rawLayerIDs] : family === 'aggregate' ? [...aggregateLayerIDs] : []
	return candidates ? layerIDs.filter((id) => candidates.includes(id)) : layerIDs
}

export function mapLayer(id: string, layerOrKind: VisualizationGeographicLayer | VisualizationGeographicLayer['kind'], tiled?: SpatialTiledVisualizationDataState, sourceID = id): any {
  const layer = typeof layerOrKind === 'string' ? undefined : layerOrKind
  const kind = typeof layerOrKind === 'string' ? layerOrKind : layerOrKind.kind
  if (kind === 'choropleth') {
    const choropleth = layer?.kind === 'choropleth' ? layer : undefined
		return { id, source: id, type: 'fill', paint: { 'fill-color': ['case', ['==', ['get', '__lv_value'], null], choropleth?.color.nullColor ?? '#d8dee4', layerColorExpression(choropleth?.color)], 'fill-opacity': governedOpacity(choropleth?.opacity ?? 0.82, 0.2, 0.4), 'fill-outline-color': choropleth?.stroke.color ?? '#ffffff' } }
  }
  if (kind === 'reference') {
    const reference = layer?.kind === 'reference' ? layer : undefined
    return { id, source: id, type: 'fill', filter: ['==', ['geometry-type'], 'Polygon'], paint: { 'fill-color': paletteColors(reference?.color)[2], 'fill-opacity': reference?.opacity ?? 0.18, 'fill-outline-color': reference?.stroke.color ?? '#57606a' } }
  }
  if (kind === 'path') {
    const path = layer?.kind === 'path' ? layer : undefined
    const width = path?.line.width ?? 3
    return { id, source: id, type: 'line', paint: {
      'line-color': path?.category ? layerColorExpression(path.color) : path?.value ? pathValueColorExpression(path.color) : path?.stroke.color ?? '#0969da',
      'line-width': path?.value ? ['interpolate', ['linear'], ['sqrt', ['get', '__lv_weight']], 0, Math.max(1.5, width / 2), 1, width + 2] : width,
      'line-opacity': governedOpacity((path?.opacity ?? 0.82) * (path?.stroke.opacity ?? 1), 0.16),
    } }
  }
	if (kind === 'point') {
		const point = layer?.kind === 'point' ? layer : undefined
		const minimumRadius = point?.size?.minimumRadius ?? 5, maximumRadius = point?.size?.maximumRadius ?? 10
		const weight = tiled && point ? tiledWeightExpression(point, tiled) : ['get', '__lv_weight']
		const aggregateMinimumRadius = Math.max(12, minimumRadius)
		const aggregateMaximumRadius = Math.max(50, maximumRadius)
		const radius = tiled
			? ['case', ['boolean', ['get', '__lv_aggregate'], false], ['interpolate', ['linear'], ['sqrt', weight], 0, aggregateMinimumRadius, 1, aggregateMaximumRadius], ['interpolate', ['linear'], ['sqrt', weight], 0, minimumRadius, 1, maximumRadius]]
			: ['interpolate', ['linear'], ['sqrt', weight], 0, minimumRadius, 1, maximumRadius]
		return { id, source: sourceID, ...(tiled ? { 'source-layer': 'primary' } : {}), type: 'circle', filter: tiled ? ['all', ['==', ['geometry-type'], 'Point'], ['==', ['boolean', ['get', '__lv_aggregate'], false], false]] : ['!', ['has', 'point_count']], minzoom: tiled ? Math.max(point?.visibility.minimumZoom ?? 0, tiled.rawMinimumZoom) : point?.visibility.minimumZoom, maxzoom: point?.visibility.maximumZoom, paint: { 'circle-radius': ['case', featureFlag('__lv_selected'), maximumRadius + 3, radius], 'circle-color': tiled && point ? tiledColorExpression(point, tiled) : layerColorExpression(point?.color), 'circle-stroke-color': point?.stroke.color ?? '#ffffff', 'circle-stroke-opacity': point?.stroke.opacity ?? 1, 'circle-stroke-width': ['case', featureFlag('__lv_selected'), (point?.stroke.width ?? 1.5) + 1, ['boolean', ['get', '__lv_aggregate'], false], Math.max(point?.stroke.width ?? 1.5, 2), point?.stroke.width ?? 1.5], 'circle-opacity': governedOpacity(point?.opacity ?? 0.78, 0.16, 0.3) } }
	}
	const heat = layer?.kind === 'heat' || layer?.kind === 'density' ? layer : undefined
	const colors = paletteColors(heat?.color)
	const weight = tiled && heat ? tiledWeightExpression(heat, tiled) : ['get', '__lv_weight']
	return { id, source: sourceID, ...(tiled ? { 'source-layer': 'primary', filter: ['==', ['boolean', ['get', '__lv_aggregate'], false], false], minzoom: Math.max(heat?.visibility.minimumZoom ?? 0, tiled.rawMinimumZoom), maxzoom: heat?.visibility.maximumZoom } : {}), type: 'heatmap', paint: {
		'heatmap-weight': ['*', weight, ['case', featureFlag('__lv_selected'), 1, featureFlag('__lv_has_selection'), 0.75, featureFlag('__lv_highlighted'), 1, featureFlag('__lv_has_highlight'), 0.15, 0.75]],
    'heatmap-intensity': heat?.heat.intensity ?? (kind === 'density' ? 1.35 : 1),
    'heatmap-radius': heat?.heat.radius ?? (kind === 'density' ? 24 : 32),
    'heatmap-opacity': heat?.opacity ?? 0.86,
    'heatmap-color': ['interpolate', ['linear'], ['heatmap-density'], 0, transparentColor(colors[0]), 0.15, colors[0], 0.35, colors[1], 0.6, colors[2], 0.85, colors[3], 1, colors[4]],
  } }
}

export function tiledAggregatePointLayer(id: string, sourceID: string, layer: Extract<VisualizationGeographicLayer, { kind: 'point' }>, state: SpatialTiledVisualizationDataState): any {
	const result = mapLayer(id, layer, state, sourceID)
	return {
		...result,
		filter: ['all', ['==', ['geometry-type'], 'Point'], ['==', ['boolean', ['get', '__lv_aggregate'], false], true]],
		minzoom: layer.visibility.minimumZoom,
		maxzoom: Math.min(layer.visibility.maximumZoom, state.rawMinimumZoom),
	}
}

export function tiledAggregateHeatLayer(id: string, sourceID: string, layer: Extract<VisualizationGeographicLayer, { kind: 'heat' | 'density' }>, state: SpatialTiledVisualizationDataState): any {
	const colors = paletteColors(layer.color)
	const authoredRadius = layer.heat.radius ?? (layer.kind === 'density' ? 24 : 32)
	return {
		id, source: sourceID, 'source-layer': 'primary', type: 'heatmap',
		filter: ['==', ['boolean', ['get', '__lv_aggregate'], false], true],
		minzoom: layer.visibility.minimumZoom, maxzoom: Math.min(layer.visibility.maximumZoom, state.rawMinimumZoom),
		paint: {
			'heatmap-weight': ['*', tiledAggregateHeatWeightExpression(layer, state), ['case', featureFlag('__lv_selected'), 1, featureFlag('__lv_has_selection'), 0.75, featureFlag('__lv_highlighted'), 1, featureFlag('__lv_has_highlight'), 0.15, 0.75]],
			'heatmap-intensity': tiledAggregateHeatIntensityExpression(layer, state),
			// Aggregate features represent occupied cells rather than individual
			// coordinates. Overlap their kernels so the aligned server grid reads
			// as a continuous density surface instead of a lattice of dots.
			'heatmap-radius': layer.kind === 'density'
				? Math.min(112, Math.max(72, authoredRadius * 2.5))
				: Math.min(96, Math.max(48, authoredRadius * 1.5)),
			'heatmap-opacity': layer.opacity ?? 0.86,
			'heatmap-color': ['interpolate', ['linear'], ['heatmap-density'], 0, transparentColor(colors[0]), 0.15, colors[0], 0.35, colors[1], 0.6, colors[2], 0.85, colors[3], 1, colors[4]],
		},
	}
}

function tiledAggregateHeatIntensityExpression(layer: Extract<VisualizationGeographicLayer, { kind: 'heat' | 'density' }>, state: SpatialTiledVisualizationDataState): number | unknown[] {
	const authored = layer.heat.intensity ?? (layer.kind === 'density' ? 1.35 : 1)
	const minimumZoom = Math.max(layer.visibility.minimumZoom, state.minimumZoom)
	const transitionZoom = Math.min(layer.visibility.maximumZoom, state.rawMinimumZoom)
	if (transitionZoom <= minimumZoom) return authored
	// Aggregation replaces many raw heat kernels with one cell kernel. Preserve
	// the authored relative weights, but compensate for that lost contribution
	// count at overview zooms. Taper to the authored intensity at the global raw
	// transition so the two precision families meet without a visual jump.
	const overviewBoost = layer.kind === 'density' ? 6 : 4
	return ['interpolate', ['linear'], ['zoom'], minimumZoom, authored * overviewBoost, transitionZoom, authored]
}

export function tiledAggregateCountLayer(id: string, sourceID: string, layer: Extract<VisualizationGeographicLayer, { kind: 'point' }>, state?: SpatialTiledVisualizationDataState): any {
	const valueField = tiledValueField(layer)
	return {
		id, source: sourceID, 'source-layer': 'primary', type: 'symbol',
		filter: ['==', ['boolean', ['get', '__lv_aggregate'], false], true],
		minzoom: layer.visibility.minimumZoom, maxzoom: Math.min(layer.visibility.maximumZoom, state?.rawMinimumZoom ?? layer.visibility.maximumZoom),
		layout: {
			'text-field': layer.cluster.showCount ? abbreviatedNumberExpression(valueField) : '',
			'text-font': ['Noto Sans Medium'], 'text-size': 11,
			'text-allow-overlap': true, 'text-ignore-placement': true,
		},
		paint: { 'text-color': '#ffffff', 'text-halo-color': '#0550ae', 'text-halo-width': 0.75 },
	}
}

function tiledValueField(layer: Extract<VisualizationGeographicLayer, { kind: 'point' | 'heat' | 'density' }>): string {
	return layer.value?.field ?? '__lv_count'
}

function tiledWeightExpression(layer: Extract<VisualizationGeographicLayer, { kind: 'point' | 'heat' | 'density' }>, state: SpatialTiledVisualizationDataState): unknown[] {
	const field = tiledValueField(layer)
	const raw = state.rawDomains.find((domain) => domain.field === field)
	const aggregate = state.aggregateDomains.find((domain) => domain.field === field)
	const normalize = (domain: typeof raw): unknown[] => {
		const minimum = domain?.minimum ?? 0, maximum = domain?.maximum ?? Math.max(minimum + 1, 1)
		if (minimum === maximum) return ['case', ['==', ['to-number', ['get', field], 0], 0], 0, 1]
		return ['max', 0, ['min', 1, ['/', ['-', ['to-number', ['get', field], 0], minimum], maximum - minimum]]]
	}
	return ['case', ['boolean', ['get', '__lv_aggregate'], false], normalize(aggregate), normalize(raw)]
}

function tiledAggregateHeatWeightExpression(layer: Extract<VisualizationGeographicLayer, { kind: 'heat' | 'density' }>, state: SpatialTiledVisualizationDataState): unknown[] {
	const field = tiledValueField(layer)
	const domain = state.aggregateDomains.find((candidate) => candidate.field === field)
	const minimum = domain?.minimum ?? 0, maximum = domain?.maximum ?? Math.max(minimum + 1, 1)
	if (minimum === maximum) return ['case', ['==', ['to-number', ['get', field], 0], 0], 0, 1]
	const normalized = ['max', 0, ['min', 1, ['/', ['-', ['to-number', ['get', field], 0], minimum], maximum - minimum]]]
	// Whole-filter totals provide a stable aggregate domain across tile loads,
	// but make low-zoom cell values tiny on large datasets. Square-root scaling
	// preserves that stable domain while retaining visible differences.
	return ['sqrt', normalized]
}

function tiledColorExpression(layer: Extract<VisualizationGeographicLayer, { kind: 'point' | 'heat' | 'density' }>, state: SpatialTiledVisualizationDataState): unknown[] {
	const colors = paletteColors(layer.color)
	const weight = tiledWeightExpression(layer, state)
	const raw = ['interpolate', ['linear'], weight, 0, colors[0], 0.25, colors[1], 0.5, colors[2], 0.75, colors[3], 1, colors[4]]
	const aggregate = ['interpolate', ['linear'], ['sqrt', weight], 0, colors[2], 0.5, colors[3], 1, colors[4]]
	return ['case', ['boolean', ['get', '__lv_aggregate'], false], aggregate, raw]
}

function abbreviatedNumberExpression(field: string): unknown[] {
	const value = ['max', 0, ['to-number', ['get', field], 0]]
	const whole = { 'min-fraction-digits': 0, 'max-fraction-digits': 0 }
	const short = { 'min-fraction-digits': 0, 'max-fraction-digits': 1 }
	return ['step', value,
		['number-format', value, whole],
		1_000, ['concat', ['number-format', ['/', value, 1_000], short], 'k'],
		1_000_000, ['concat', ['number-format', ['/', value, 1_000_000], short], 'M'],
	]
}

function governedOpacity(base: number, highlightDimmed: number, selectionDimmed = highlightDimmed): unknown[] {
  return [
    'case',
		featureFlag('__lv_selected'), 1,
		featureFlag('__lv_has_selection'), selectionDimmed,
		featureFlag('__lv_highlighted'), 1,
		featureFlag('__lv_has_highlight'), highlightDimmed,
    base,
  ]
}

function featureFlag(property: string): unknown[] {
	return ['boolean', ['get', property], false]
}

function colorInterpolation(scale?: { palette: string; reverse: boolean }): unknown[] {
  const colors = paletteColors(scale)
  return ['interpolate', ['linear'], ['get', '__lv_weight'], 0, colors[0], 0.25, colors[1], 0.5, colors[2], 0.75, colors[3], 1, colors[4]]
}

function layerColorExpression(scale?: { kind: string; palette: string; reverse: boolean; nullColor: string }): unknown[] {
  if (scale?.kind === 'categorical') return ['coalesce', ['get', '__lv_color'], scale.nullColor]
  return colorInterpolation(scale)
}

function pathValueColorExpression(scale: { palette: string; reverse: boolean }): unknown[] {
  const colors = paletteColors(scale)
  // Route lines occupy far fewer pixels than points or filled regions. Keep
  // the sequential ordering, but reserve the visible half of the palette so
  // low-ranked routes do not disappear against a light geographic basemap.
  const visible = scale.reverse ? colors.slice(0, 3) : colors.slice(2, 5)
  return ['interpolate', ['linear'], ['sqrt', ['get', '__lv_weight']], 0, visible[0], 0.55, visible[1], 1, visible[2]]
}

export function paletteColors(scale?: { palette: string; reverse: boolean }): string[] {
  const palettes: Record<string, string[]> = {
    blue: ['#ddf4ff', '#80ccff', '#54aeff', '#0969da', '#0550ae'],
    teal: ['#e1f7f5', '#90e0d9', '#39c5bb', '#008c95', '#006d77'],
    purple: ['#fbefff', '#d8b9ff', '#bf87ff', '#8250df', '#6639ba'],
    orange: ['#fff1e5', '#ffc680', '#fb8f44', '#d15704', '#bc4c00'],
    red: ['#ffebe9', '#ffb3b6', '#ff8182', '#cf222e', '#a40e26'],
  }
  const selected = [...(palettes[scale?.palette ?? 'blue'] ?? palettes.blue!)]
  return scale?.reverse ? selected.reverse() : selected
}

function transparentColor(color: string): string {
  if (/^#[0-9a-f]{6}$/i.test(color)) return `${color}00`
  return 'rgba(9,105,218,0)'
}

function layerWeightDomain(layer: VisualizationGeographicLayer): { domainMinimum?: number; domainMidpoint?: number; domainMaximum?: number } | undefined {
  if (layer.kind === 'point' && layer.size && (layer.size.domainMinimum !== undefined || layer.size.domainMaximum !== undefined)) return layer.size
  if ('color' in layer) return layer.color
  return undefined
}

export function mapOutlineLayer(id: string, source: string): any {
  return {
    id, source, type: 'line',
    filter: ['==', ['get', '__lv_selected'], true],
    paint: { 'line-color': '#bf3989', 'line-opacity': 1, 'line-width': 3 },
  }
}

export function normalizeFeatureWeights(data: FeatureCollection, domain?: { domainMinimum?: number; domainMidpoint?: number; domainMaximum?: number }): FeatureCollection {
  const values = data.features.map((feature) => feature.properties?.__lv_value).filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  const minimum = domain?.domainMinimum ?? (values.length > 0 ? Math.min(...values) : 0)
  const maximum = domain?.domainMaximum ?? (values.length > 0 ? Math.max(...values) : 0)
  const span = maximum - minimum
  return {
    ...data,
    features: data.features.map((feature) => {
      const value = feature.properties?.__lv_value
      let weight = typeof value !== 'number' || !Number.isFinite(value) ? 0 : span === 0 ? (value === 0 ? 0 : 1) : Math.max(0, Math.min(1, (value - minimum) / span))
      const midpoint = domain?.domainMidpoint
      if (typeof value === 'number' && Number.isFinite(value) && midpoint !== undefined && midpoint > minimum && midpoint < maximum) {
        weight = value <= midpoint
          ? 0.5 * Math.max(0, Math.min(1, (value - minimum) / (midpoint - minimum)))
          : 0.5 + 0.5 * Math.max(0, Math.min(1, (value - midpoint) / (maximum - midpoint)))
      }
      return { ...feature, properties: { ...feature.properties, __lv_weight: weight } }
    }),
  }
}

export function applyFeatureScales(data: FeatureCollection, layer: VisualizationGeographicLayer): FeatureCollection {
  const normalized = normalizeFeatureWeights(data, layerWeightDomain(layer))
  if (!('color' in layer) || layer.color.kind !== 'categorical') return normalized
  const categories = [...new Set(normalized.features.map((feature) => feature.properties?.__lv_category).filter((value) => value !== null && value !== undefined).map(String))].sort((a, b) => a.localeCompare(b))
  const colors = paletteColors(layer.color)
  const colorByCategory = new Map(categories.map((category, index) => [category, colors[index % colors.length]!]))
  return {
    ...normalized,
    features: normalized.features.map((feature) => {
      const category = feature.properties?.__lv_category
      const color = category === null || category === undefined ? layer.color.nullColor : colorByCategory.get(String(category)) ?? layer.color.nullColor
      return { ...feature, properties: { ...feature.properties, __lv_color: color } }
    }),
  }
}
