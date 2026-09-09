import type { MapOptions } from 'maplibre-gl'
import type { VisualizationDataState, VisualizationEnvelope, VisualizationMapStyleAsset } from '../../../../../generated/visualization'

export function mapClickCanRefineCamera(envelope: VisualizationEnvelope): boolean {
	if (envelope.spec.kind !== 'geographic') return false
	return envelope.spec.presentation.roam
		&& envelope.spec.presentation.camera.mode !== 'fixed'
		&& envelope.spec.interactions.length === 0
		&& envelope.spec.spatialInteractions.length === 0
}

function shouldPreserveCameraOnSpecUpdate(previous: VisualizationEnvelope, next: VisualizationEnvelope): boolean {
	if (previous.spec.kind !== 'geographic' || next.spec.kind !== 'geographic') return false
	const camera = next.spec.presentation.camera
	if (camera.mode === 'fixed' || camera.mode === 'preserve') return camera.mode === 'preserve'
	const previousCamera = previous.spec.presentation.camera
	return previousCamera.mode === camera.mode
		&& previousCamera.padding === camera.padding
		&& previousCamera.minimumZoom === camera.minimumZoom
		&& previousCamera.maximumZoom === camera.maximumZoom
		&& sameOptionalNumberArray(previousCamera.center, camera.center)
		&& previousCamera.zoom === camera.zoom
}

function sameOptionalNumberArray(left: readonly number[] | undefined, right: readonly number[] | undefined): boolean {
	if (left === undefined || right === undefined) return left === right
	return left.length === right.length && left.every((value, index) => value === right[index])
}

export function mapPointerOptions(envelope: VisualizationEnvelope): Pick<MapOptions, 'interactive' | 'scrollZoom' | 'boxZoom' | 'dragRotate' | 'dragPan' | 'keyboard' | 'doubleClickZoom' | 'touchZoomRotate' | 'touchPitch'> {
	const roam = envelope.spec.kind === 'geographic' ? envelope.spec.presentation.roam : false
	const selectable = envelope.spec.kind === 'geographic' && (envelope.spec.interactions.some((candidate) => candidate.kind === 'select') || envelope.spec.spatialInteractions.length > 0)
	return {
		interactive: roam || selectable,
		scrollZoom: roam,
		boxZoom: roam,
		dragRotate: roam,
		dragPan: roam,
		keyboard: roam,
		doubleClickZoom: roam,
		touchZoomRotate: roam,
		touchPitch: roam,
	}
}

export function mapBasemapIdentity(asset: VisualizationMapStyleAsset | undefined): string {
	if (!asset) return 'blank'
	return [asset.id, asset.styleUrl, asset.styleDigest, asset.archiveUrl, asset.archiveDigest, asset.glyphsUrl, asset.spriteUrl].join('\u0000')
}

function comparableDataState(state: VisualizationDataState): unknown {
	const { specRevision: _specRevision, ...withoutSpecRevision } = state
	if (state.kind !== 'inline') return withoutSpecRevision
	return {
		...withoutSpecRevision,
		datasets: state.datasets.map(({ specRevision: _datasetSpecRevision, ...dataset }) => dataset),
	}
}

function dataStateEquivalent(previous: VisualizationEnvelope, next: VisualizationEnvelope): boolean {
	if (previous.dataRevision !== next.dataRevision) return false
	return JSON.stringify(comparableDataState(previous.dataState)) === JSON.stringify(comparableDataState(next.dataState))
}

export function isLabelDensityOnlyChange(previous: VisualizationEnvelope | undefined, next: VisualizationEnvelope): boolean {
	if (!previous || previous.spec.kind !== 'geographic' || next.spec.kind !== 'geographic') return false
	const { labelDensity: _previousDensity, ...previousPresentation } = previous.spec.presentation
	const { labelDensity: _nextDensity, ...nextPresentation } = next.spec.presentation
	return dataStateEquivalent(previous, next) && JSON.stringify({ ...previous.spec, presentation: previousPresentation }) === JSON.stringify({ ...next.spec, presentation: nextPresentation })
}

export { shouldPreserveCameraOnSpecUpdate }
