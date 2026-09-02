import { currentVisualizationSchemaVersion, RendererRegistry } from './host-controller'

export const visualizationRegistry = new RendererRegistry()

visualizationRegistry.register({
  id: 'echarts', version: '6.1.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['cartesian', 'point', 'proportional', 'hierarchy', 'polar'],
  capabilities: { snapshot: true, windowed: false, interactive: true },
  load: async () => (await import('./adapters/echarts')).adapter,
})
visualizationRegistry.register({
  id: 'html', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'],
  capabilities: { snapshot: true, windowed: false, interactive: true },
  load: async () => (await import('./adapters/html')).adapter,
})
visualizationRegistry.register({
  id: 'tanstack', version: '9.0.0-beta.12', schemaVersion: currentVisualizationSchemaVersion, kinds: ['table', 'matrix', 'pivot'],
  capabilities: { snapshot: true, windowed: true, interactive: true },
  load: async () => (await import('./adapters/tanstack')).adapter,
})
visualizationRegistry.register({
  id: 'maplibre', version: '6.6.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['geographic'],
  capabilities: { snapshot: true, windowed: true, interactive: true },
  load: async () => (await import('./adapters/maplibre')).adapter,
})
