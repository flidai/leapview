import { LitElement, html } from 'lit'
import { property } from 'lit/decorators.js'
import React from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { RotateCcw, Table2, type IconNode } from 'lucide'
import '@xyflow/react/dist/style.css'
import {
  Background,
  BaseEdge,
  Controls,
  EdgeLabelRenderer,
  getBezierPath,
  getSmoothStepPath,
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
import { orderDatasetRanks, splitDatasetRankNodes } from './semantic-model-graph-layout'
import { FIELD_HEIGHT, HEADER_HEIGHT, NODE_WIDTH, semanticModelGraphStyles } from './semantic-model-graph.styles'
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
  sameRank: boolean
  sourceMarker: string
  targetMarker: string
}

type NodePosition = { x: number; y: number }
type DatasetNode = Node<DatasetNodeData, 'dataset'>
type DatasetEdge = Edge<DatasetEdgeData, 'relationship'>

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

  const edges = React.useMemo(() => {
    const ranks = datasetNodeRanks(graph)
    return graph.edges.map((edge) => toFlowEdge(edge, selectedID, selectedEdges, ranks.get(edge.source) === ranks.get(edge.target), activeEdgeID))
  }, [graph, selectedID, selectedEdges, activeEdgeID])

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
    if (event.key === 'Escape') {
      event.preventDefault()
      clearSelection()
      return
    }
    if (event.key !== 'Enter' && event.key !== ' ') return
    const target = event.target
    if (!(target instanceof Element)) return
    const edgeID = target.closest('.react-flow__edge')?.getAttribute('data-id')
    if (!edgeID) return
    event.preventDefault()
    setSelectedID(undefined)
    setSelectedEdgeID((current) => current === edgeID ? undefined : edgeID)
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
  const orderedRanks = orderDatasetRanks(graph, ranks, rankValues)
  const positions = new Map<string, NodePosition>()
  let columnOffset = 0

  for (const rank of rankValues) {
    const rankNodes = orderedRanks.get(rank) ?? []
    const totalHeight = rankNodes.reduce((height, node) => height + nodeHeight(node, fieldCounts.get(node.id) ?? node.fields.length), 0)
      + Math.max(0, rankNodes.length - 1) * NODE_GAP_Y
    const columnCount = Math.max(1, Math.min(rankNodes.length, Math.ceil(totalHeight / TARGET_COLUMN_HEIGHT)))
    const columns = splitDatasetRankNodes(rankNodes, columnCount, (node) => nodeHeight(node, fieldCounts.get(node.id) ?? node.fields.length), NODE_GAP_Y)
    const columnHeights = columns.map((column) => column.reduce((height, node) => height + nodeHeight(node, fieldCounts.get(node.id) ?? node.fields.length), 0) + Math.max(0, column.length - 1) * NODE_GAP_Y)
    const maxColumnHeight = Math.max(...columnHeights, 0)

    // Keep each rank in a predictable left-to-right order while centering
    // shorter wrapped columns against the tallest one. Contiguous columns are
    // important here: a shortest-column assignment can interleave unrelated
    // datasets and turn a readable rank into a bundle of crossing edges.
    for (const [column, nodes] of columns.entries()) {
      let y = NODE_OFFSET_Y + Math.max(0, (maxColumnHeight - columnHeights[column]) / 2)
      for (const node of nodes) {
        positions.set(node.id, {
          x: NODE_OFFSET_X + (columnOffset + column) * NODE_GAP_X,
          y,
        })
        y += nodeHeight(node, fieldCounts.get(node.id) ?? node.fields.length) + NODE_GAP_Y
      }
    }
    columnOffset += columnCount
  }

  return positions
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

function toFlowEdge(edge: SemanticModelGraphEdgeSignal, selectedID: string | undefined, selectedEdges: Set<string>, sameRank: boolean, activeEdgeID?: string): DatasetEdge {
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
    data: { ...edge, emphasized, sameRank, sourceMarker, targetMarker },
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
  const pathOptions = {
    sourceX: props.sourceX,
    sourceY: props.sourceY,
    sourcePosition: props.sourcePosition,
    targetX: props.targetX,
    targetY: props.targetY,
    targetPosition: props.targetPosition,
  }
  const [path, labelX, labelY] = props.data?.sameRank ? getBezierPath(pathOptions) : getSmoothStepPath({
    ...pathOptions,
    borderRadius: 12,
    offset: 24,
  })
  const data = props.data
  const style = props.style ?? {}
  const relationshipLabel = data ? `${data.source}.${data.sourceField} → ${data.target}.${data.targetField}` : 'Relationship'
  return React.createElement(React.Fragment, null,
    React.createElement(BaseEdge, {
      id: props.id,
      className: 'semantic-model-relationship-path',
      path,
      style,
      interactionWidth: props.interactionWidth,
    }),
    React.createElement(EdgeLabelRenderer, null,
      React.createElement('div', {
        className: `semantic-model-edge-label ${data?.emphasized ? 'selected' : ''}`,
        title: relationshipLabel,
        'aria-label': relationshipLabel,
        style: {
          transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
        },
      }, data?.label ?? ''),
      React.createElement('div', {
        className: 'semantic-model-edge-endpoint source',
        title: data ? `${data.source}.${data.sourceField}` : undefined,
        'aria-label': data ? `Source ${data.source}.${data.sourceField}` : undefined,
        style: {
          transform: `translate(-50%, -50%) translate(${props.sourceX}px,${props.sourceY}px)`,
        },
      }, data?.sourceMarker ?? ''),
      React.createElement('div', {
        className: 'semantic-model-edge-endpoint target',
        title: data ? `${data.target}.${data.targetField}` : undefined,
        'aria-label': data ? `Target ${data.target}.${data.targetField}` : undefined,
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
  return `leapview:semantic-model-graph:v5:${storageKey || (graph.datasets ?? []).join(',') || 'model'}:${nodePart}:${edgePart}`
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


if (!customElements.get('lv-semantic-model-graph')) customElements.define('lv-semantic-model-graph', SemanticModelGraphElement)
