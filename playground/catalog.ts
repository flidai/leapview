import catalog from '../docs/visuals/catalog.json'

/** Navigation metadata stays separate from the components loaded for each example. */
export const chartExamples = catalog.documents.map(({ source, title }) => ({ id: source, label: title }))

export const tokenExamples = [
  { id: 'colors', label: 'Colors' },
  { id: 'typography', label: 'Typography' },
  { id: 'spacing', label: 'Spacing & sizing' },
  { id: 'borders', label: 'Borders & shadows' },
  { id: 'motion', label: 'Motion' },
  { id: 'other', label: 'Other tokens' },
]

export const controlExamples = [
  { id: 'buttons', label: 'Buttons' },
  { id: 'fields', label: 'Form fields' },
  { id: 'select', label: 'Select menu' },
  { id: 'multiselect', label: 'Entity multiselect' },
  { id: 'date-picker', label: 'Date picker' },
  { id: 'filter-menu', label: 'Filter menu' },
  { id: 'toast', label: 'Toasts' },
  { id: 'loading', label: 'Loading' },
]

export const graphExamples = [
  { id: 'asset-lineage', label: 'Asset lineage graph' },
  { id: 'semantic-model', label: 'Semantic model graph' },
]

export const tableExamples = [
  { id: 'record', label: 'Record table' },
  { id: 'windowed', label: 'Windowed table' },
  { id: 'entity-list', label: 'Entity list' },
  { id: 'data-preview', label: 'Data preview table' },
  { id: 'data-explore', label: 'Data exploration table' },
]

export const contentExamples = [
  { id: 'code-editor', label: 'Code editor' },
  { id: 'code-block', label: 'Code block' },
  { id: 'config-viewer', label: 'Configuration viewer' },
  { id: 'markdown-view', label: 'Markdown' },
  { id: 'visual-artifact', label: 'Visual artifact' },
  { id: 'chat-composer', label: 'Chat composer' },
]

export const surfaceExamples = [
  { id: 'drawer', label: 'Drawer' },
  { id: 'identity', label: 'Avatars, brand & icons' },
  { id: 'toast-region', label: 'Notification stack' },
  { id: 'one-time-secret', label: 'One-time secret' },
  { id: 'empty-state', label: 'Empty states' },
  { id: 'page-header', label: 'Page header' },
  { id: 'breadcrumb', label: 'Breadcrumbs' },
  { id: 'settings', label: 'Settings sections' },
  { id: 'entity-detail', label: 'Entity detail' },
  { id: 'icon-picker', label: 'Dashboard icon picker' },
  { id: 'appearance', label: 'Dashboard appearance' },
  { id: 'report-footer', label: 'Report footer & zoom' },
]

export const filterExamples = [
  { id: 'leaf', label: 'Filter control' },
  { id: 'pane', label: 'Filter pane card' },
  { id: 'slicer', label: 'Dashboard slicer' },
  { id: 'dock', label: 'Filter dock' },
]
