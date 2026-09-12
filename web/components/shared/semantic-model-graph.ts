import { LitElement, html } from 'lit'
import { property } from 'lit/decorators.js'
import React from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { RotateCcw, Table2, type IconNode } from 'lucide'
import '@xyflow/react/dist/style.css'
import {
  Background,
  Controls,
  EdgeLabelRenderer,
  getBezierPath,
  Handle,
  Panel,
  Position,
  ReactFlow,
  type Edge,
  type EdgeProps,
  type Node,
  useNodesState,
} from '@xyflow/react'
import { fieldTypeIcon } from './field-type-icon'
import type {
  SemanticModelGraphEdgeSignal,
  SemanticModelGraphFieldSignal,
  SemanticModelGraphNodeSignal,
  SemanticModelGraphSignal,
} from '../../generated/signals'

type DatasetNodeData = SemanticModelGraphNodeSignal & Record<string, unknown> & {
  selected: boolean
  dimmed: boolean
  hiddenFieldCount: number
  highlightedFields: string[]
  onSelect: (id: string) => void
}

type DatasetEdgeData = SemanticModelGraphEdgeSignal & Record<string, unknown> & {
  emphasized: boolean
  sourceMarker: string
  targetMarker: string
}

type NodePosition = { x: number; y: number }
type DatasetNode = Node<DatasetNodeData, 'dataset'>
type DatasetEdge = Edge<DatasetEdgeData, 'relationship'>

const NODE_WIDTH = 280
const HEADER_HEIGHT = 40
const FIELD_HEIGHT = 28
const NODE_GAP_X = 380
const NODE_GAP_Y = 56
const NODE_OFFSET_X = 72
const NODE_OFFSET_Y = 42
const TARGET_COLUMN_HEIGHT = 1_200

class SemanticModelGraphElement extends LitElement {
  @property({ type: Object }) graph: SemanticModelGraphSignal | null = null
  @property({ attribute: 'storagekey' }) storageKey = ''
  private root?: Root
  private mount?: HTMLDivElement
  private manualPositions = new Map<string, NodePosition>()
  private lastLayoutKey = ''

  createRenderRoot(): HTMLElement {
    return this
  }

  firstUpdated(): void {
    this.mount = this.renderRoot.querySelector('.semantic-model-graph-root') as HTMLDivElement | null ?? undefined
    if (this.mount) {
      this.root = createRoot(this.mount)
      this.renderFlow()
    }
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has('graph') || changed.has('storageKey')) this.renderFlow()
  }

  disconnectedCallback(): void {
    this.root?.unmount()
    super.disconnectedCallback()
  }

  render() {
    return html`
      <style>${semanticModelGraphStyles}</style>
      <div class="semantic-model-graph-root"></div>
    `
  }

  private renderFlow(): void {
    if (!this.root) return
    const graph = this.resolvedGraph
    const layoutKey = graphLayoutKey(graph, this.storageKey)
    if (layoutKey !== this.lastLayoutKey) {
      this.manualPositions = loadLayout(layoutKey)
      this.lastLayoutKey = layoutKey
    }
    this.root.render(
      React.createElement(SemanticModelGraphFlow, {
        graph,
        layoutKey,
        manualPositions: this.manualPositions,
        onLayoutChange: (positions) => {
          this.manualPositions = positions
          saveLayout(layoutKey, positions)
        },
        onLayoutReset: () => {
          this.manualPositions = new Map()
          clearLayout(layoutKey)
        },
      }),
    )
  }

	private get resolvedGraph(): SemanticModelGraphSignal {
	return {
		datasets: this.graph?.datasets ?? [],
      nodes: this.graph?.nodes ?? [],
      edges: this.graph?.edges ?? [],
    }
  }
}

function SemanticModelGraphFlow({
  graph,
  layoutKey,
  manualPositions,
  onLayoutChange,
  onLayoutReset,
}: {
  graph: SemanticModelGraphSignal
  layoutKey: string
  manualPositions: Map<string, NodePosition>
  onLayoutChange: (positions: Map<string, NodePosition>) => void
  onLayoutReset: () => void
}) {
  const [selectedID, setSelectedID] = React.useState<string | undefined>()
  const [selectedEdgeID, setSelectedEdgeID] = React.useState<string | undefined>()
  const [hoveredEdgeID, setHoveredEdgeID] = React.useState<string | undefined>()
  const [showAllFields, setShowAllFields] = React.useState(false)
  const [nodes, setNodes, onNodesChange] = useNodesState<DatasetNode>([])
  const displayGraph = React.useMemo(() => relationshipFocusedGraph(graph, showAllFields), [graph, showAllFields])
  const fieldCounts = React.useMemo(() => new Map(graph.nodes.map((node) => [node.id, node.fields.length])), [graph.nodes])
  const automaticPositions = React.useMemo(() => datasetNodePositions(displayGraph, fieldCounts), [displayGraph, fieldCounts])

  const selectedEdges = React.useMemo(() => relatedEdgeIDs(graph.edges, selectedID), [graph.edges, selectedID])
  const activeEdgeID = selectedEdgeID ?? hoveredEdgeID
  const activeEdge = React.useMemo(() => graph.edges.find((edge) => edge.id === activeEdgeID), [activeEdgeID, graph.edges])
  const highlightedFields = React.useMemo(() => relationshipFieldsByNode(activeEdge), [activeEdge])

  React.useEffect(() => {
    setSelectedID((current) => retainedSelectedNodeID(graph.nodes, current))
    setSelectedEdgeID((current) => current && graph.edges.some((edge) => edge.id === current) ? current : undefined)
  }, [graph.nodes, layoutKey])

  const selectNode = React.useCallback((id: string) => {
    setSelectedEdgeID(undefined)
    setSelectedID(id)
  }, [])

  React.useEffect(() => {
    setNodes(displayGraph.nodes.map((node) => toFlowNode(node, displayGraph, fieldCounts.get(node.id) ?? node.fields.length, selectedID, selectedEdges, activeEdge, highlightedFields, automaticPositions, manualPositions, selectNode)))
  }, [displayGraph, fieldCounts, layoutKey, automaticPositions, manualPositions, selectNode, setNodes])

  React.useEffect(() => {
    setNodes((currentNodes) => currentNodes.map((node) => ({
      ...node,
      data: {
        ...node.data,
        selected: node.id === selectedID,
        dimmed: nodeDimmed(node.id, graph.edges, selectedID, selectedEdges, activeEdge),
        highlightedFields: highlightedFields.get(node.id) ?? [],
      },
    })))
  }, [graph.edges, selectedID, selectedEdges, activeEdge, highlightedFields, setNodes])

  const edges = React.useMemo(() => graph.edges.map((edge) => toFlowEdge(edge, selectedID, selectedEdges, activeEdgeID)), [graph.edges, selectedID, selectedEdges, activeEdgeID])

  const saveDraggedLayout = React.useCallback((_event: unknown, node: DatasetNode) => {
    const next = new Map(nodes.map((current) => [current.id, current.position] as [string, NodePosition]))
    next.set(node.id, node.position)
    onLayoutChange(next)
  }, [nodes, onLayoutChange])

  const resetLayout = React.useCallback(() => {
    onLayoutReset()
    setNodes(displayGraph.nodes.map((node) => toFlowNode(node, displayGraph, fieldCounts.get(node.id) ?? node.fields.length, selectedID, selectedEdges, activeEdge, highlightedFields, automaticPositions, new Map(), selectNode)))
  }, [displayGraph, fieldCounts, onLayoutReset, selectedEdges, selectedID, activeEdge, highlightedFields, automaticPositions, selectNode, setNodes])

  const clearSelection = React.useCallback(() => {
    setSelectedID(undefined)
    setSelectedEdgeID(undefined)
    setHoveredEdgeID(undefined)
  }, [])

  const handleGraphClick = React.useCallback((event: React.MouseEvent<HTMLDivElement>) => {
    const target = event.target
    if (!(target instanceof Element)) return
    if (target.closest('.react-flow__node, button')) return
    clearSelection()
  }, [clearSelection])

  const handleGraphKeyDown = React.useCallback((event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'Escape') return
    event.preventDefault()
    clearSelection()
  }, [clearSelection])

  return React.createElement(
    'div',
    {
      className: 'semantic-model-graph-layout',
      tabIndex: 0,
      'aria-label': 'Semantic model relationship graph',
      onClick: handleGraphClick,
      onKeyDown: handleGraphKeyDown,
    },
    React.createElement(ReactFlow<DatasetNode, DatasetEdge>, {
      key: `${layoutKey}:${showAllFields ? 'all' : 'relationships'}`,
      nodes,
      edges,
      onNodesChange,
      onNodeDragStop: saveDraggedLayout,
      onPaneClick: clearSelection,
      onEdgeMouseEnter: (_event, edge) => setHoveredEdgeID(edge.id),
      onEdgeMouseLeave: (_event, edge) => setHoveredEdgeID((current) => current === edge.id ? undefined : current),
      onEdgeClick: (event, edge) => {
        event.stopPropagation()
        setSelectedID(undefined)
        setSelectedEdgeID((current) => current === edge.id ? undefined : edge.id)
      },
      nodeTypes: { dataset: DatasetNodeComponent },
      edgeTypes: { relationship: RelationshipEdge },
      fitView: true,
      fitViewOptions: { padding: 0.12 },
      minZoom: 0.2,
      maxZoom: 1.35,
      nodesDraggable: true,
      nodesConnectable: false,
      elementsSelectable: true,
      panOnDrag: true,
      zoomOnScroll: false,
      preventScrolling: false,
      children: [
        React.createElement(Background, { key: 'background', gap: 20, size: 1 }),
        React.createElement(Controls, { key: 'controls', showInteractive: false }),
        activeEdge ? React.createElement(RelationshipInspector, { key: 'relationship-inspector', edge: activeEdge, graph }) : null,
        React.createElement(Panel, { key: 'layout-panel', position: 'top-right', className: 'semantic-model-layout-actions' },
          React.createElement(
            'div',
            { className: 'semantic-model-fields-control', role: 'group', 'aria-label': 'Fields shown' },
            React.createElement('span', { className: 'semantic-model-fields-label' }, 'Fields:'),
            React.createElement('button', {
              className: 'semantic-model-fields-option',
              type: 'button',
              'aria-pressed': !showAllFields,
              onClick: () => setShowAllFields(false),
            }, 'Related'),
            React.createElement('button', {
              className: 'semantic-model-fields-option',
              type: 'button',
              'aria-pressed': showAllFields,
              onClick: () => setShowAllFields(true),
            }, 'All'),
          ),
          React.createElement(
            'button',
            {
              className: 'semantic-model-reset-button',
              type: 'button',
              title: 'Reset layout',
              'aria-label': 'Reset layout',
              onClick: resetLayout,
            },
            iconElement(RotateCcw, 'semantic-model-reset-icon'),
          ),
        ),
      ],
    }),
  )
}

function retainedSelectedNodeID(nodes: SemanticModelGraphNodeSignal[], current?: string): string | undefined {
  if (current && nodes.some((node) => node.id === current)) return current
  return undefined
}

function relatedEdgeIDs(edges: SemanticModelGraphEdgeSignal[], selected?: string): Set<string> {
  if (!selected) return new Set()
  return new Set(edges.filter((edge) => edge.source === selected || edge.target === selected).map((edge) => edge.id))
}

function toFlowNode(
  node: SemanticModelGraphNodeSignal,
  graph: SemanticModelGraphSignal,
  totalFieldCount: number,
  selectedID: string | undefined,
  selectedEdges: Set<string>,
  activeEdge: SemanticModelGraphEdgeSignal | undefined,
  highlightedFields: Map<string, string[]>,
  automaticPositions: Map<string, NodePosition>,
  manualPositions: Map<string, NodePosition>,
  onSelect: (id: string) => void,
): DatasetNode {
  const position = manualPositions.get(node.id) ?? automaticPositions.get(node.id) ?? { x: NODE_OFFSET_X, y: NODE_OFFSET_Y }
  return {
    id: node.id,
    type: 'dataset',
    position,
    sourcePosition: Position.Right,
    targetPosition: Position.Left,
    data: {
      ...node,
      selected: node.id === selectedID,
      dimmed: nodeDimmed(node.id, graph.edges, selectedID, selectedEdges, activeEdge),
      hiddenFieldCount: Math.max(0, totalFieldCount - node.fields.length),
      highlightedFields: highlightedFields.get(node.id) ?? [],
      onSelect,
    },
  }
}

function nodeDimmed(id: string, edges: SemanticModelGraphEdgeSignal[], selectedID: string | undefined, selectedEdges: Set<string>, activeEdge?: SemanticModelGraphEdgeSignal): boolean {
  if (activeEdge) return false
  return Boolean(selectedID && id !== selectedID && !edges.some((edge) => selectedEdges.has(edge.id) && (edge.source === id || edge.target === id)))
}

function relationshipFieldsByNode(edge?: SemanticModelGraphEdgeSignal): Map<string, string[]> {
  if (!edge) return new Map()
  return new Map([
    [edge.source, relationshipFieldNames(edge.sourceField)],
    [edge.target, relationshipFieldNames(edge.targetField)],
  ])
}

function relationshipFieldNames(fields: string): string[] {
  return fields.split(',').map((field) => field.trim()).filter(Boolean)
}

function relationshipFocusedGraph(graph: SemanticModelGraphSignal, showAllFields: boolean): SemanticModelGraphSignal {
  if (showAllFields) return graph
  return {
    ...graph,
    nodes: graph.nodes.map((node) => {
      const relationshipFields = node.fields.filter((field) => field.grain || field.join)
      return {
        ...node,
        fields: relationshipFields.length > 0 ? relationshipFields : node.fields.slice(0, 1),
      }
    }),
  }
}

function datasetNodePositions(graph: SemanticModelGraphSignal, fieldCounts: Map<string, number>): Map<string, NodePosition> {
  const ranks = datasetNodeRanks(graph)
  const rankValues = [...new Set(ranks.values())].sort((left, right) => left - right)
  const positions = new Map<string, NodePosition>()
  let columnOffset = 0

  for (const rank of rankValues) {
    const rankNodes = graph.nodes
      .filter((candidate) => (ranks.get(candidate.id) ?? 0) === rank)
      .sort((left, right) => left.id.localeCompare(right.id))
    const totalHeight = rankNodes.reduce((height, node) => height + nodeHeight(node, fieldCounts.get(node.id) ?? node.fields.length), 0)
      + Math.max(0, rankNodes.length - 1) * NODE_GAP_Y
    const columnCount = Math.max(1, Math.min(rankNodes.length, Math.ceil(totalHeight / TARGET_COLUMN_HEIGHT)))
    const columnHeights = Array.from({ length: columnCount }, () => 0)

    // Greedy height balancing keeps variable-height schemas compact while the
    // rank bands still enforce the graph's left-to-right dependency order.
    for (const node of rankNodes) {
      const column = shortestColumn(columnHeights)
      positions.set(node.id, {
        x: NODE_OFFSET_X + (columnOffset + column) * NODE_GAP_X,
        y: NODE_OFFSET_Y + columnHeights[column],
      })
      columnHeights[column] += nodeHeight(node, fieldCounts.get(node.id) ?? node.fields.length) + NODE_GAP_Y
    }
    columnOffset += columnCount
  }

  return positions
}

function shortestColumn(heights: number[]): number {
  let shortest = 0
  for (let index = 1; index < heights.length; index += 1) {
    if (heights[index] < heights[shortest]) shortest = index
  }
  return shortest
}

function datasetNodeRanks(graph: SemanticModelGraphSignal): Map<string, number> {
  const ranks = new Map<string, number>()
  const nodeIDs = new Set(graph.nodes.map((node) => node.id))
  const incoming = new Map(graph.nodes.map((node) => [node.id, 0]))
  const outgoing = new Map(graph.nodes.map((node) => [node.id, [] as string[]]))

  for (const edge of graph.edges) {
    if (!nodeIDs.has(edge.source) || !nodeIDs.has(edge.target) || edge.source === edge.target) continue
    outgoing.get(edge.source)?.push(edge.target)
    incoming.set(edge.target, (incoming.get(edge.target) ?? 0) + 1)
  }

  const queue = graph.nodes
    .filter((node) => (incoming.get(node.id) ?? 0) === 0)
    .map((node) => node.id)
    .sort((left, right) => left.localeCompare(right))
  for (const root of queue) ranks.set(root, 0)

  while (queue.length) {
    const current = queue.shift() ?? ''
    const currentRank = ranks.get(current) ?? 0
    for (const next of outgoing.get(current) ?? []) {
      ranks.set(next, Math.max(ranks.get(next) ?? 0, currentRank + 1))
      const remaining = (incoming.get(next) ?? 1) - 1
      incoming.set(next, remaining)
      if (remaining === 0) queue.push(next)
    }
  }

  // Cyclic components have no topological root. Keep them together in the
  // first rank so the layout remains bounded and users can separate them by
  // dragging without an unbounded rank walk.
  for (const node of graph.nodes) {
    if (!ranks.has(node.id)) ranks.set(node.id, 0)
  }
  return ranks
}

function nodeHeight(node: SemanticModelGraphNodeSignal, totalFieldCount = node.fields.length): number {
  const summaryRows = totalFieldCount > node.fields.length ? 1 : 0
  return HEADER_HEIGHT + (Math.max(1, node.fields.length) + summaryRows) * FIELD_HEIGHT + 12
}

function toFlowEdge(edge: SemanticModelGraphEdgeSignal, selectedID: string | undefined, selectedEdges: Set<string>, activeEdgeID?: string): DatasetEdge {
  const emphasized = edge.id === activeEdgeID || Boolean(selectedID && selectedEdges.has(edge.id))
  const dimmed = activeEdgeID ? edge.id !== activeEdgeID : Boolean(selectedID && !selectedEdges.has(edge.id))
  const [sourceMarker, targetMarker] = relationshipEndpointMarkers(edge.cardinality)
  return {
    id: edge.id,
    type: 'relationship',
    source: edge.source,
    target: edge.target,
    // Composite endpoints retain their complete ordered tuple in the signal;
    // React Flow anchors the edge to the first physical field's handle while
    // the tuple remains visible in the edge label and metadata.
    sourceHandle: `${endpointAnchorField(edge.sourceField)}:source`,
    targetHandle: `${endpointAnchorField(edge.targetField)}:target`,
    interactionWidth: 18,
    ariaLabel: `${edge.source}.${edge.sourceField} to ${edge.target}.${edge.targetField}, ${cardinalityLabel(edge.cardinality)}`,
    data: { ...edge, emphasized, sourceMarker, targetMarker },
    style: {
		stroke: 'var(--lv-fg-muted)',
      strokeWidth: emphasized ? 2.6 : 1.6,
      opacity: dimmed ? 0.18 : 0.82,
    },
  }
}

function endpointAnchorField(fields: string): string {
  return fields.split(',')[0]?.trim() ?? fields.trim()
}

function RelationshipEdge(props: EdgeProps<DatasetEdge>) {
  const [path, labelX, labelY] = getBezierPath(props)
  const data = props.data
  const style = props.style ?? {}
  return React.createElement(React.Fragment, null,
    React.createElement('path', {
      id: props.id,
      className: 'react-flow__edge-path semantic-model-relationship-path',
      d: path,
      style,
    }),
    React.createElement(EdgeLabelRenderer, null,
      React.createElement('div', {
        className: `semantic-model-edge-label ${data?.emphasized ? 'selected' : ''}`,
        style: {
          transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
        },
      }, data?.label ?? ''),
      React.createElement('div', {
        className: 'semantic-model-edge-endpoint source',
        style: {
          transform: `translate(-50%, -50%) translate(${props.sourceX}px,${props.sourceY}px)`,
        },
      }, data?.sourceMarker ?? ''),
      React.createElement('div', {
        className: 'semantic-model-edge-endpoint target',
        style: {
          transform: `translate(-50%, -50%) translate(${props.targetX}px,${props.targetY}px)`,
        },
      }, data?.targetMarker ?? ''),
    ),
  )
}

function RelationshipInspector({ edge, graph }: { edge: SemanticModelGraphEdgeSignal; graph: SemanticModelGraphSignal }) {
  const titles = new Map(graph.nodes.map((node) => [node.id, node.title]))
  const sourceTitle = titles.get(edge.source) ?? edge.source
  const targetTitle = titles.get(edge.target) ?? edge.target
  const source = `${sourceTitle}.${edge.sourceField}`
  const target = `${targetTitle}.${edge.targetField}`
  return React.createElement(
    Panel,
    { position: 'top-left', className: 'semantic-model-relationship-inspector' },
    React.createElement('strong', null, 'Relationship'),
    React.createElement('span', { className: 'semantic-model-relationship-fields' }, `${source} → ${target}`),
    React.createElement('span', null, `${cardinalityLabel(edge.cardinality)} · Direction: ${sourceTitle} → ${targetTitle}`),
  )
}

function cardinalityLabel(cardinality: string): string {
  const label = cardinality.replaceAll('_', ' ')
  return label.charAt(0).toUpperCase() + label.slice(1)
}

function relationshipEndpointMarkers(cardinality: string): [string, string] {
  switch (cardinality) {
    case 'many_to_one':
      return ['*', '1']
    case 'one_to_one':
      return ['1', '1']
    default:
      return ['', '']
  }
}

function graphLayoutKey(graph: SemanticModelGraphSignal, storageKey: string): string {
  const nodePart = graph.nodes.map((node) => `${node.id}:${node.fields.map((field) => field.name).join(',')}`).join('|')
  const edgePart = graph.edges.map((edge) => `${edge.id}:${edge.source}.${edge.sourceField}->${edge.target}.${edge.targetField}:${edge.cardinality}`).join('|')
  return `leapview:semantic-model-graph:v4:${storageKey || (graph.datasets ?? []).join(',') || 'model'}:${nodePart}:${edgePart}`
}

function loadLayout(key: string): Map<string, NodePosition> {
  try {
    const raw = globalThis.localStorage?.getItem(key)
    if (!raw) return new Map()
    const parsed = JSON.parse(raw) as Record<string, NodePosition>
    return new Map(Object.entries(parsed).filter((entry): entry is [string, NodePosition] => (
      Number.isFinite(entry[1]?.x) && Number.isFinite(entry[1]?.y)
    )))
  } catch {
    return new Map()
  }
}

function saveLayout(key: string, positions: Map<string, NodePosition>): void {
  try {
    globalThis.localStorage?.setItem(key, JSON.stringify(Object.fromEntries(positions)))
  } catch {
    // Layout persistence is progressive enhancement only.
  }
}

function clearLayout(key: string): void {
  try {
    globalThis.localStorage?.removeItem(key)
  } catch {
    // Layout persistence is progressive enhancement only.
  }
}

function DatasetNodeComponent({ data }: { data: DatasetNodeData }) {
  const className = [
    'semantic-model-node',
    data.selected ? 'semantic-model-node-selected' : '',
    data.dimmed ? 'semantic-model-node-dimmed' : '',
  ].filter(Boolean).join(' ')
  const select = () => data.onSelect(data.id)
  return React.createElement(
    'div',
    {
      className,
      role: 'button',
      tabIndex: 0,
      'aria-label': `Dataset ${data.title}`,
      'aria-pressed': data.selected ? 'true' : 'false',
      onClick: select,
      onKeyDown: (event: React.KeyboardEvent) => {
        if (event.key !== 'Enter' && event.key !== ' ') return
        event.preventDefault()
        select()
      },
    },
    React.createElement('div', { className: 'semantic-model-node-header' },
		React.createElement('div', { className: 'semantic-model-node-title', title: data.title },
			iconElement(Table2, 'semantic-dataset-icon'),
			React.createElement('span', null, data.title),
		),
	),
    React.createElement('div', { className: 'semantic-model-node-fields' },
      data.fields.map((field, index) => React.createElement(ModelFieldRow, { key: field.name, field, grainEntity: data.grainEntity, index, highlighted: data.highlightedFields.includes(field.name) })),
      data.hiddenFieldCount > 0 ? React.createElement('div', { className: 'semantic-model-hidden-fields' }, `+${data.hiddenFieldCount} more fields`) : null,
    ),
  )
}

function ModelFieldRow({ field, grainEntity, index, highlighted }: { field: SemanticModelGraphFieldSignal; grainEntity?: string; index: number; highlighted: boolean }) {
  const top = HEADER_HEIGHT + index * FIELD_HEIGHT + FIELD_HEIGHT / 2
  const className = [
    'semantic-model-field',
    field.join ? 'semantic-model-field-join' : '',
    field.grain ? 'semantic-model-field-grain' : '',
    highlighted ? 'semantic-model-field-highlighted' : '',
  ].filter(Boolean).join(' ')
  const identity = field.entities?.length ? `; entities: ${field.entities.join(', ')}` : ''
  return React.createElement(
    'div',
    { className },
    field.join ? React.createElement(Handle, { id: `${field.name}:target`, type: 'target', position: Position.Left, style: { top } }) : null,
    React.createElement('span', { className: 'semantic-model-field-type-icon', title: field.type ? `Column type ${field.type}` : 'Column type unknown' }, iconElement(fieldTypeIcon(field.type), 'semantic-model-type-icon')),
    React.createElement('span', { className: 'semantic-model-field-name', title: `${field.name}${field.grain ? ` (grain: ${grainEntity ?? 'model grain'})` : ''}${identity}` }, field.name),
    field.grain ? React.createElement('span', { className: 'semantic-model-field-grain-marker', title: `Grain field for ${grainEntity ?? 'model grain'}` }, 'G') : null,
    field.join ? React.createElement(Handle, { id: `${field.name}:source`, type: 'source', position: Position.Right, style: { top } }) : null,
  )
}

function iconElement(icon: IconNode, className: string) {
  return React.createElement(
    'svg',
    {
      className,
      xmlns: 'http://www.w3.org/2000/svg',
      width: 14,
      height: 14,
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      strokeWidth: 2,
      strokeLinecap: 'round',
      strokeLinejoin: 'round',
      'aria-hidden': 'true',
    },
    icon.map(([tag, attrs], index) => React.createElement(tag, { ...attrs, key: index })),
  )
}

const semanticModelGraphStyles = `
  lv-semantic-model-graph,
  lv-semantic-model-graph .semantic-model-graph-root,
  lv-semantic-model-graph .semantic-model-graph-layout {
    display: block;
    height: 100%;
    min-width: 0;
    min-height: 0;
  }

  lv-semantic-model-graph .semantic-model-graph-layout {
    background:
      linear-gradient(var(--lv-bg-page, var(--lv-bg-app)), var(--lv-bg-page, var(--lv-bg-app))),
      radial-gradient(circle at 1px 1px, color-mix(in srgb, var(--lv-fg-muted), transparent 88%) 1px, transparent 0);
    background-size: auto, 20px 20px;
    outline: 0;
  }

  lv-semantic-model-graph .react-flow {
    position: relative;
    overflow: hidden;
    width: 100%;
    height: 100%;
    color: var(--lv-fg-default);
    background-color: transparent;
  }

  lv-semantic-model-graph .react-flow__container,
  lv-semantic-model-graph .react-flow__renderer,
  lv-semantic-model-graph .react-flow__viewport,
  lv-semantic-model-graph .react-flow__pane,
  lv-semantic-model-graph .react-flow__nodes,
  lv-semantic-model-graph .react-flow .react-flow__edges,
  lv-semantic-model-graph .react-flow .react-flow__edges svg {
    position: absolute;
  }

  lv-semantic-model-graph .react-flow__container,
  lv-semantic-model-graph .react-flow__viewport {
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    transform-origin: 0 0;
  }

  lv-semantic-model-graph .react-flow__node {
    position: absolute;
    box-sizing: border-box;
    pointer-events: all;
    transform-origin: 0 0;
    user-select: none;
  }

  lv-semantic-model-graph .react-flow__nodes {
    pointer-events: none;
  }

  lv-semantic-model-graph .react-flow .react-flow__edges svg {
    overflow: visible;
    pointer-events: none;
  }

  lv-semantic-model-graph .react-flow__edge-path,
  lv-semantic-model-graph .react-flow__connection-path {
    fill: none;
  }

  lv-semantic-model-graph .semantic-model-relationship-path {
    pointer-events: visibleStroke;
    cursor: pointer;
  }

  lv-semantic-model-graph .react-flow__edgelabel-renderer {
    position: absolute;
    width: 100%;
    height: 100%;
    pointer-events: none;
    user-select: none;
  }

  lv-semantic-model-graph .react-flow__background {
    pointer-events: none;
    z-index: -1;
  }

  lv-semantic-model-graph .react-flow__handle {
    position: absolute;
    width: 8px;
    height: 8px;
    min-width: 8px;
    min-height: 8px;
    border: 1px solid var(--lv-bg-panel);
    border-radius: 50%;
    background: var(--lv-fg-muted);
    pointer-events: none;
  }

  lv-semantic-model-graph .react-flow__handle-left {
    left: 0;
    transform: translate(-50%, -50%);
  }

  lv-semantic-model-graph .react-flow__handle-right {
    right: 0;
    transform: translate(50%, -50%);
  }

  lv-semantic-model-graph .react-flow__panel {
    position: absolute;
    z-index: 5;
    margin: var(--base-size-16);
  }

  lv-semantic-model-graph .react-flow__panel.left {
    left: 0;
  }

  lv-semantic-model-graph .react-flow__panel.bottom {
    bottom: 0;
  }

  lv-semantic-model-graph .react-flow__controls {
    display: flex;
    flex-direction: column;
    border: var(--lv-border-default);
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-resting-small, none);
  }

  lv-semantic-model-graph .react-flow__controls-button {
    display: flex;
    width: 26px;
    height: 26px;
    align-items: center;
    justify-content: center;
    border: 0;
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    padding: 4px;
    cursor: pointer;
  }

  lv-semantic-model-graph .react-flow__controls-button svg {
    width: 100%;
    max-width: 12px;
    max-height: 12px;
    fill: currentColor;
  }

  lv-semantic-model-graph .semantic-model-layout-actions {
    display: flex;
    align-items: center;
    gap: var(--base-size-6);
  }

  lv-semantic-model-graph .semantic-model-fields-control,
  lv-semantic-model-graph .semantic-model-reset-button {
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-tight);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    font: var(--lv-type-caption);
  }

  lv-semantic-model-graph .semantic-model-fields-control {
    display: inline-flex;
    min-height: 28px;
    align-items: center;
    overflow: hidden;
  }

  lv-semantic-model-graph .semantic-model-fields-label {
    color: var(--lv-fg-muted);
    padding: 0 var(--base-size-8);
  }

  lv-semantic-model-graph .semantic-model-fields-option {
    align-self: stretch;
    border: 0;
    border-left: var(--lv-border-muted);
    background: transparent;
    color: var(--lv-fg-muted);
    padding: 0 var(--base-size-8);
    font: inherit;
    cursor: pointer;
  }

  lv-semantic-model-graph .semantic-model-fields-option[aria-pressed='true'] {
    background: var(--lv-bg-control-active, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-semibold);
  }

  lv-semantic-model-graph .semantic-model-reset-button {
    display: inline-flex;
    width: 28px;
    height: 28px;
    align-items: center;
    justify-content: center;
    padding: 0;
  }

  lv-semantic-model-graph .semantic-model-fields-option:hover,
  lv-semantic-model-graph .semantic-model-fields-option:focus-visible,
  lv-semantic-model-graph .semantic-model-reset-button:hover,
  lv-semantic-model-graph .semantic-model-reset-button:focus-visible {
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    outline: 0;
  }

  lv-semantic-model-graph .semantic-model-relationship-inspector {
    display: grid;
    max-width: min(520px, calc(100% - 32px));
    gap: var(--base-size-4);
    border: var(--lv-border-default);
    border-radius: var(--borderRadius-default);
    background: color-mix(in srgb, var(--lv-bg-panel), transparent 4%);
    box-shadow: var(--shadow-resting-small, none);
    color: var(--lv-fg-muted);
    padding: var(--base-size-8) var(--base-size-10);
    font: var(--lv-type-caption);
    pointer-events: none;
  }

  lv-semantic-model-graph .semantic-model-relationship-inspector strong {
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-semibold);
  }

  lv-semantic-model-graph .semantic-model-relationship-fields {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-code-inline);
  }

  lv-semantic-model-graph .semantic-model-reset-icon {
    display: block;
    width: 14px;
    height: 14px;
  }

  lv-semantic-model-graph .react-flow__attribution {
    display: none;
  }

  lv-semantic-model-graph .semantic-model-edge-label,
  lv-semantic-model-graph .semantic-model-edge-endpoint {
    position: absolute;
    display: inline-grid;
    place-items: center;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-full);
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-resting-small, none);
    color: var(--lv-fg-default);
    font: var(--lv-type-caption);
    line-height: 1;
    pointer-events: none;
  }

  lv-semantic-model-graph .semantic-model-edge-label {
    min-width: 30px;
    min-height: 20px;
    padding: 0 var(--base-size-6);
  }

  lv-semantic-model-graph .semantic-model-edge-label.selected {
    border-color: var(--lv-fg-muted);
    color: var(--lv-fg-default);
  }

  lv-semantic-model-graph .semantic-model-edge-endpoint {
    width: 20px;
    height: 20px;
    border-color: color-mix(in srgb, var(--lv-fg-muted), transparent 35%);
    color: var(--lv-fg-default);
  }

  lv-semantic-model-graph .semantic-model-edge-endpoint.source {
    margin-left: -16px;
  }

  lv-semantic-model-graph .semantic-model-edge-endpoint.target {
    margin-left: 16px;
  }

  lv-semantic-model-graph .semantic-model-node {
    width: ${NODE_WIDTH}px;
    overflow: hidden;
    border: var(--borderWidth-default) solid var(--lv-line-muted);
    border-radius: var(--borderRadius-default);
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-resting-small, none);
    color: var(--lv-fg-default);
    cursor: pointer;
  }

  lv-semantic-model-graph .semantic-model-node-selected {
    border-color: var(--lv-fg-muted);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--lv-fg-muted), transparent 35%), var(--shadow-resting-small, none);
  }

  lv-semantic-model-graph .semantic-model-node-dimmed {
    opacity: 0.42;
  }

  lv-semantic-model-graph .semantic-model-node:focus-visible {
    outline: 2px solid var(--lv-fg-muted);
    outline-offset: 2px;
  }

  lv-semantic-model-graph .semantic-model-node-header {
    display: flex;
    min-height: ${HEADER_HEIGHT}px;
    min-width: 0;
    gap: var(--base-size-8);
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
    padding: 0 var(--base-size-12);
    align-items: center;
    justify-content: space-between;
  }

  lv-semantic-model-graph .semantic-model-node-title {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-6);
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-semibold);
  }

  lv-semantic-model-graph .semantic-model-node-title span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  lv-semantic-model-graph .semantic-dataset-icon {
    flex: 0 0 auto;
    color: var(--lv-fg-muted);
  }

  lv-semantic-model-graph .semantic-model-node-fields {
    display: grid;
  }

  lv-semantic-model-graph .semantic-model-field {
    display: grid;
    min-height: ${FIELD_HEIGHT}px;
    grid-template-columns: 18px minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--base-size-6);
    border-bottom: var(--lv-border-muted);
    box-shadow: inset 0 0 0 0 transparent;
    padding: 0 var(--lv-space-control);
  }

  lv-semantic-model-graph .semantic-model-field:last-child {
    border-bottom: 0;
  }

  lv-semantic-model-graph .semantic-model-hidden-fields {
    display: flex;
    min-height: ${FIELD_HEIGHT}px;
    align-items: center;
    border-top: var(--lv-border-muted);
    color: var(--lv-fg-muted);
    padding: 0 var(--lv-space-control);
    font: var(--lv-type-caption);
  }

  lv-semantic-model-graph .semantic-model-field-join {
    background: color-mix(in srgb, var(--lv-fg-muted), transparent 90%);
    box-shadow: inset 2px 0 0 color-mix(in srgb, var(--lv-fg-muted), transparent 42%);
  }

  lv-semantic-model-graph .semantic-model-field-highlighted {
    background: color-mix(in srgb, var(--lv-line-accent), transparent 88%);
    box-shadow: inset 3px 0 0 var(--lv-line-accent);
  }

  lv-semantic-model-graph .semantic-model-field-name {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-code-inline);
  }

  lv-semantic-model-graph .semantic-model-field-grain .semantic-model-field-name {
    font-weight: var(--base-text-weight-semibold);
  }

  lv-semantic-model-graph .semantic-model-field-type-icon {
    display: inline-grid;
    width: 18px;
    height: 18px;
    place-items: center;
    color: var(--lv-fg-muted);
  }

  lv-semantic-model-graph .semantic-model-type-icon {
    display: block;
    width: 14px;
    height: 14px;
  }

  lv-semantic-model-graph .semantic-model-field-grain-marker {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    line-height: 1;
  }
`

if (!customElements.get('lv-semantic-model-graph')) customElements.define('lv-semantic-model-graph', SemanticModelGraphElement)
