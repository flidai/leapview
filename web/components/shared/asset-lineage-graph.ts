import { LitElement, html } from 'lit'
import { property } from 'lit/decorators.js'
import React from 'react'
import { createRoot, type Root } from 'react-dom/client'
import '@xyflow/react/dist/style.css'
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  type Edge,
  type Node,
} from '@xyflow/react'

type LineageGraph = {
  nodes: LineageNode[]
  edges: LineageEdge[]
}

type LineageNode = {
  id: string
  label: string
  kind: string
  meta?: string
  href?: string
  side?: 'upstream' | 'selected' | 'downstream'
  rank?: number
  selected?: boolean
  visibleUpstreamCount?: number
  visibleDownstreamCount?: number
  usesCount?: number
  usedByCount?: number
  containedCount?: number
  containedSummary?: string
}

type LineageEdge = {
  id: string
  source: string
  target: string
  label?: string
  kind: string
}

type LineageLayout = {
  rankIndex: Map<number, number>
  nodeIndex: Map<string, number>
}

type LineagePathState = {
  selectedID?: string
  upstream: Set<string>
  downstream: Set<string>
  connectedEdges: Set<string>
}

type LineageNodeData = LineageNode & {
  pathState: 'selected' | 'upstream' | 'downstream' | 'unrelated'
  onSelect: (id: string) => void
}

const NODE_GAP_X = 260
const NODE_GAP_Y = 124
const NODE_OFFSET_X = 96
const NODE_MIN_Y = 48

class AssetLineageGraph extends LitElement {
  @property({ type: Object }) graph: LineageGraph | null = null
  private root?: Root
  private mount?: HTMLDivElement
  private selectedNodeID?: string

  createRenderRoot(): HTMLElement {
    return this
  }

  firstUpdated(): void {
    this.mount = this.renderRoot.querySelector('.asset-lineage-root') as HTMLDivElement | null ?? undefined
    if (this.mount) {
      this.root = createRoot(this.mount)
      this.renderFlow()
    }
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has('graph')) this.renderFlow()
  }

  disconnectedCallback(): void {
    this.root?.unmount()
    super.disconnectedCallback()
  }

  render() {
    return html`
      <style>
        ${assetLineageGraphStyles}
      </style>
      <div class="asset-lineage-root"></div>
    `
  }

  private renderFlow(): void {
    if (!this.root) return
    const graph = this.resolvedGraph
    const layout = createLineageLayout(graph.nodes)
    const selectedNode = selectedLineageNode(graph.nodes, this.selectedNodeID)
    this.selectedNodeID = selectedNode?.id
    const pathState = createPathState(graph, this.selectedNodeID)
    this.root.render(
      React.createElement(
        'div',
        { className: 'asset-lineage-layout' },
        React.createElement(
          'div',
          { className: 'asset-lineage-flow', 'aria-label': 'Asset lineage graph' },
          React.createElement(ReactFlow, {
            nodes: graph.nodes.map((node) => toFlowNode(node, layout, pathState, (id) => {
              this.selectedNodeID = id
              this.renderFlow()
            })),
            edges: graph.edges.map((edge) => toFlowEdge(edge, pathState)),
            nodeTypes: { lineageNode: LineageNodeComponent },
            fitView: true,
            fitViewOptions: { padding: 0.12 },
            minZoom: 0.15,
            maxZoom: 1.35,
            nodesDraggable: false,
            nodesConnectable: false,
            elementsSelectable: true,
            panOnDrag: true,
            zoomOnScroll: false,
            preventScrolling: false,
            children: [
              React.createElement(Background, { key: 'background', gap: 18, size: 1 }),
              React.createElement(Controls, { key: 'controls', showInteractive: false }),
            ],
          }),
        ),
        React.createElement(LineageInspectorPanel, { key: selectedNode?.id ?? 'empty', node: selectedNode }),
      ),
    )
  }

  private get resolvedGraph(): LineageGraph {
    if (this.graph) {
      return {
        nodes: this.graph.nodes ?? [],
        edges: this.graph.edges ?? [],
      }
    }
    return { nodes: [], edges: [] }
  }
}

const assetLineageGraphStyles = `
  lv-asset-lineage-graph .asset-lineage-root,
  lv-asset-lineage-graph .asset-lineage-layout {
    height: 100%;
    min-height: 0;
    min-width: 0;
  }

  lv-asset-lineage-graph .asset-lineage-layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(17rem, 20rem);
  }

  lv-asset-lineage-graph .asset-lineage-flow {
    height: 100%;
    min-height: 0;
    min-width: 0;
    background:
      linear-gradient(var(--lv-bg-page), var(--lv-bg-page)),
      radial-gradient(circle at 1px 1px, color-mix(in srgb, var(--lv-fg-muted), transparent 87%) 1px, transparent 0);
    background-size: auto, 18px 18px;
  }

  lv-asset-lineage-graph .asset-lineage-panel {
    display: grid;
    align-content: start;
    gap: var(--base-size-16);
    min-width: 0;
    border-left: var(--borderWidth-thin) solid var(--lv-line-muted);
    background: var(--lv-bg-panel);
    padding: var(--base-size-16);
  }

  lv-asset-lineage-graph .asset-lineage-panel-eyebrow {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  lv-asset-lineage-graph .asset-lineage-panel-title {
    overflow: hidden;
    margin: var(--base-size-4) 0 0;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-semibold);
  }

  lv-asset-lineage-graph .asset-lineage-panel-key {
    overflow: hidden;
    margin-top: var(--base-size-6);
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-code-inline);
  }

  lv-asset-lineage-graph .asset-lineage-panel-stats {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--base-size-8);
  }

  lv-asset-lineage-graph .asset-lineage-panel-stat {
    display: grid;
    gap: var(--base-size-4);
    min-width: 0;
    border: var(--borderWidth-thin) solid var(--lv-line-muted);
    border-radius: var(--borderRadius-default);
    background: var(--lv-bg-panel-muted);
    padding: var(--base-size-8);
  }

  lv-asset-lineage-graph .asset-lineage-panel-stat span {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  lv-asset-lineage-graph .asset-lineage-panel-stat strong {
    color: var(--lv-fg-default);
    font: var(--lv-type-body-compact);
  }

  lv-asset-lineage-graph .asset-lineage-panel-summary {
    min-width: 0;
    color: var(--lv-fg-muted);
    font: var(--lv-type-body);
  }

  lv-asset-lineage-graph .asset-lineage-panel-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 2rem;
    border: var(--borderWidth-thin) solid var(--lv-line-accent);
    border-radius: var(--borderRadius-default);
    background: var(--lv-line-accent);
    color: var(--lv-fg-on-emphasis);
    padding: 0 var(--base-size-12);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-medium);
    text-decoration: none;
  }

  lv-asset-lineage-graph .asset-lineage-panel-action:hover,
  lv-asset-lineage-graph .asset-lineage-panel-action:focus-visible {
    outline: 0;
    filter: brightness(1.06);
  }

  lv-asset-lineage-graph .react-flow {
    position: relative;
    overflow: hidden;
    width: 100%;
    height: 100%;
    direction: ltr;
    color: var(--lv-fg-default);
    background-color: transparent;
  }

  lv-asset-lineage-graph .react-flow__container {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
  }

  lv-asset-lineage-graph .react-flow__pane {
    z-index: 1;
    touch-action: none;
  }

  lv-asset-lineage-graph .react-flow__viewport {
    z-index: 2;
    pointer-events: none;
    transform-origin: 0 0;
  }

  lv-asset-lineage-graph .react-flow__renderer {
    z-index: 4;
  }

  lv-asset-lineage-graph .react-flow__nodes {
    pointer-events: none;
    transform-origin: 0 0;
  }

  lv-asset-lineage-graph .react-flow__node {
    position: absolute;
    box-sizing: border-box;
    pointer-events: all;
    transform-origin: 0 0;
    user-select: none;
  }

  lv-asset-lineage-graph .react-flow .react-flow__edges,
  lv-asset-lineage-graph .react-flow .react-flow__edges svg {
    position: absolute;
  }

  lv-asset-lineage-graph .react-flow .react-flow__edges svg {
    overflow: visible;
    pointer-events: none;
  }

  lv-asset-lineage-graph .react-flow__edge {
    pointer-events: visibleStroke;
  }

  lv-asset-lineage-graph .react-flow__edge-path,
  lv-asset-lineage-graph .react-flow__connection-path {
    fill: none;
  }

  lv-asset-lineage-graph .react-flow__edge-textwrapper {
    pointer-events: all;
  }

  lv-asset-lineage-graph .react-flow__edge .react-flow__edge-text {
    pointer-events: none;
    user-select: none;
  }

  lv-asset-lineage-graph .react-flow__background {
    pointer-events: none;
    z-index: -1;
  }

  lv-asset-lineage-graph .react-flow__handle {
    position: absolute;
    width: 6px;
    height: 6px;
    min-width: 5px;
    min-height: 5px;
    border: 1px solid var(--lv-bg-panel);
    border-radius: 100%;
    background: var(--lv-fg-muted);
    pointer-events: none;
  }

  lv-asset-lineage-graph .react-flow__handle-left {
    top: 50%;
    left: 0;
    transform: translate(-50%, -50%);
  }

  lv-asset-lineage-graph .react-flow__handle-right {
    top: 50%;
    right: 0;
    transform: translate(50%, -50%);
  }

  lv-asset-lineage-graph .react-flow__panel {
    position: absolute;
    z-index: 5;
    margin: var(--base-size-16);
  }

  lv-asset-lineage-graph .react-flow__panel.left {
    left: 0;
  }

  lv-asset-lineage-graph .react-flow__panel.bottom {
    bottom: 0;
  }

  lv-asset-lineage-graph .react-flow__controls {
    display: flex;
    flex-direction: column;
  }

  lv-asset-lineage-graph .react-flow__controls.horizontal {
    flex-direction: row;
  }

  lv-asset-lineage-graph .react-flow__controls-button {
    display: flex;
    width: 26px;
    height: 26px;
    align-items: center;
    justify-content: center;
    border: 0;
    padding: 4px;
    cursor: pointer;
    user-select: none;
  }

  lv-asset-lineage-graph .react-flow__controls-button svg {
    width: 100%;
    max-width: 12px;
    max-height: 12px;
    fill: currentColor;
  }

  lv-asset-lineage-graph .react-flow__attribution {
    display: none;
  }

  lv-asset-lineage-graph .react-flow__controls {
    border: var(--lv-border-default);
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-resting-small);
  }

  lv-asset-lineage-graph .react-flow__controls-button {
    border-bottom-color: var(--lv-line-muted);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
  }

  lv-asset-lineage-graph .asset-lineage-node {
    width: 200px;
    border: var(--borderWidth-default) solid var(--lineage-node-border);
    border-left: var(--borderWidth-thicker) solid var(--lineage-node-accent);
    border-radius: var(--borderRadius-default);
    background: var(--lineage-node-bg);
    box-shadow: var(--shadow-resting-small);
    color: var(--lv-fg-default);
    padding: var(--base-size-8) var(--base-size-12);
    cursor: pointer;
  }

  lv-asset-lineage-graph .asset-lineage-node-selected {
    border-color: var(--lv-line-accent);
    box-shadow: 0 0 0 var(--borderWidth-default) color-mix(in srgb, var(--lv-line-accent), transparent 28%), var(--shadow-resting-small);
  }

  lv-asset-lineage-graph .asset-lineage-node:focus-visible {
    outline: var(--borderWidth-thicker) solid var(--lv-line-accent);
    outline-offset: var(--base-size-2);
  }

  lv-asset-lineage-graph .asset-lineage-node-unrelated {
    opacity: 0.34;
  }

  lv-asset-lineage-graph .asset-lineage-node-upstream,
  lv-asset-lineage-graph .asset-lineage-node-downstream {
    box-shadow: 0 0 0 var(--borderWidth-thin) color-mix(in srgb, var(--lv-line-accent), transparent 58%), var(--shadow-resting-small);
  }

  lv-asset-lineage-graph .asset-lineage-node-kind {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  lv-asset-lineage-graph .asset-lineage-node-title {
    display: block;
    overflow: hidden;
    margin-top: var(--base-size-4);
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-semibold);
    text-decoration: none;
  }

  lv-asset-lineage-graph .asset-lineage-node-title[href]:hover,
  lv-asset-lineage-graph .asset-lineage-node-title[href]:focus-visible {
    color: var(--lv-fg-link);
    outline: 0;
    text-decoration: underline;
  }

  lv-asset-lineage-graph .asset-lineage-node-meta {
    overflow: hidden;
    margin-top: var(--base-size-6);
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-caption);
  }

  @media (max-width: 860px) {
    lv-asset-lineage-graph .asset-lineage-layout {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: minmax(26rem, 1fr) auto;
    }

    lv-asset-lineage-graph .asset-lineage-panel {
      border-top: var(--borderWidth-thin) solid var(--lv-line-muted);
      border-left: 0;
    }
  }
`

function toFlowNode(node: LineageNode, layout: LineageLayout, pathState: LineagePathState, onSelect: (id: string) => void): Node<LineageNodeData> {
  const { x, y } = positionFor(node, layout)
  return {
    id: node.id,
    type: 'lineageNode',
    position: { x, y },
    sourcePosition: Position.Right,
    targetPosition: Position.Left,
    className: `asset-lineage-flow-node asset-lineage-flow-node-${nodePathState(node.id, pathState)}`,
    data: {
      ...node,
      selected: node.id === pathState.selectedID,
      pathState: nodePathState(node.id, pathState),
      onSelect,
    },
  }
}

function toFlowEdge(edge: LineageEdge, pathState: LineagePathState): Edge {
  const context = edge.kind === 'contains'
  const connected = edge.source === pathState.selectedID || edge.target === pathState.selectedID || pathState.connectedEdges.has(edge.id)
  const muted = pathState.selectedID ? !connected : false
  return {
    id: edge.id,
    source: edge.source,
    target: edge.target,
    label: context ? '' : edge.label ?? '',
    type: context ? 'smoothstep' : 'default',
    markerEnd: context ? undefined : { type: MarkerType.ArrowClosed },
    interactionWidth: context ? 8 : 14,
    style: {
      stroke: edgeStroke(edge.kind),
      strokeWidth: connected && !context ? 2.4 : context ? 1 : 1.8,
      strokeDasharray: context ? '4 7' : undefined,
      opacity: muted ? 0.18 : context ? 0.28 : 0.9,
    },
    labelStyle: {
      fill: context ? 'color-mix(in srgb, var(--lv-fg-muted), transparent 12%)' : 'var(--lv-fg-muted)',
      fontSize: 10,
      fontWeight: 500,
    },
    labelBgStyle: {
      fill: 'var(--lv-bg-page)',
      fillOpacity: 0.92,
    },
  }
}

function selectedLineageNode(nodes: LineageNode[], selectedID?: string): LineageNode | undefined {
  return nodes.find((node) => node.id === selectedID) ?? nodes.find((node) => node.selected) ?? nodes[0]
}

function createPathState(graph: LineageGraph, selectedID?: string): LineagePathState {
  const state: LineagePathState = {
    selectedID,
    upstream: new Set<string>(),
    downstream: new Set<string>(),
    connectedEdges: new Set<string>(),
  }
  if (!selectedID) return state
  const incoming = new Map<string, LineageEdge[]>()
  const outgoing = new Map<string, LineageEdge[]>()
  for (const edge of graph.edges) {
    if (!incoming.has(edge.target)) incoming.set(edge.target, [])
    incoming.get(edge.target)?.push(edge)
    if (!outgoing.has(edge.source)) outgoing.set(edge.source, [])
    outgoing.get(edge.source)?.push(edge)
  }
  walkLineagePath(selectedID, incoming, 'source', state.upstream, state.connectedEdges)
  walkLineagePath(selectedID, outgoing, 'target', state.downstream, state.connectedEdges)
  return state
}

function walkLineagePath(
  nodeID: string,
  edgesByNode: Map<string, LineageEdge[]>,
  peerKey: 'source' | 'target',
  seenNodes: Set<string>,
  seenEdges: Set<string>,
): void {
  for (const edge of edgesByNode.get(nodeID) ?? []) {
    const peerID = edge[peerKey]
    seenEdges.add(edge.id)
    if (seenNodes.has(peerID)) continue
    seenNodes.add(peerID)
    walkLineagePath(peerID, edgesByNode, peerKey, seenNodes, seenEdges)
  }
}

function nodePathState(id: string, pathState: LineagePathState): 'selected' | 'upstream' | 'downstream' | 'unrelated' {
  if (!pathState.selectedID || id === pathState.selectedID) return 'selected'
  if (pathState.upstream.has(id)) return 'upstream'
  if (pathState.downstream.has(id)) return 'downstream'
  return 'unrelated'
}

function createLineageLayout(nodes: LineageNode[]): LineageLayout {
  const ranks = Array.from(new Set(nodes.map(nodeRank))).sort((left, right) => left - right)
  const rankIndex = new Map(ranks.map((rank, index) => [rank, index]))
  const nodeIndex = new Map<string, number>()

  for (const rank of ranks) {
    const rankNodes = nodes
      .filter((candidate) => nodeRank(candidate) === rank)
      .sort((left, right) => nodeSortKey(left).localeCompare(nodeSortKey(right)))
    rankNodes.forEach((candidate, index) => {
      if (!nodeIndex.has(candidate.id)) nodeIndex.set(candidate.id, index)
    })
  }

  return { rankIndex, nodeIndex }
}

function positionFor(node: LineageNode, layout: LineageLayout): { x: number; y: number } {
  const rank = nodeRank(node)
  const rankIndex = layout.rankIndex.get(rank) ?? 0
  const index = layout.nodeIndex.get(node.id) ?? 0
  return {
    x: NODE_OFFSET_X + rankIndex * NODE_GAP_X,
    y: NODE_MIN_Y + index * NODE_GAP_Y,
  }
}

function nodeRank(node: LineageNode): number {
  if (typeof node.rank === 'number' && Number.isFinite(node.rank)) return node.rank
  if (node.selected || node.side === 'selected') return 0
  if (node.side === 'upstream') return -1
  return 1
}

function nodeSortKey(node: LineageNode): string {
  return `${node.kind}:${node.label}:${node.id}`
}

function LineageNodeComponent({ data }: { data: LineageNodeData }) {
  const styles = nodeStyle(data)
  const className = [
    'asset-lineage-node',
    data.selected ? 'asset-lineage-node-selected' : '',
    `asset-lineage-node-${data.pathState}`,
  ].filter(Boolean).join(' ')
  const select = () => data.onSelect(data.id)
  return React.createElement(
    'div',
    {
      className,
      style: styles,
      role: 'button',
      tabIndex: 0,
      'aria-pressed': data.selected ? 'true' : 'false',
      'aria-label': `${kindLabel(data.kind)} ${data.label}`,
      onClick: select,
      onKeyDown: (event: React.KeyboardEvent) => {
        if (event.key !== 'Enter' && event.key !== ' ') return
        event.preventDefault()
        select()
      },
    },
    React.createElement(Handle, { type: 'target', position: Position.Left }),
    React.createElement('div', { className: 'asset-lineage-node-kind' }, kindLabel(data.kind)),
    React.createElement('div', { className: 'asset-lineage-node-title', title: data.label }, data.label),
    data.meta ? React.createElement('div', { className: 'asset-lineage-node-meta' }, data.meta) : null,
    React.createElement(Handle, { type: 'source', position: Position.Right }),
  )
}

function LineageInspectorPanel({ node }: { node?: LineageNode }) {
  if (!node) {
    return React.createElement(
      'aside',
      { className: 'asset-lineage-panel', 'aria-label': 'Selected lineage asset' },
      React.createElement('div', null,
        React.createElement('div', { className: 'asset-lineage-panel-eyebrow' }, 'Lineage'),
        React.createElement('p', { className: 'asset-lineage-panel-summary' }, 'Select a node to inspect its lineage context.'),
      ),
    )
  }
  return React.createElement(
    'aside',
    { className: 'asset-lineage-panel', 'aria-label': 'Selected lineage asset' },
    React.createElement('div', null,
      React.createElement('div', { className: 'asset-lineage-panel-eyebrow' }, kindLabel(node.kind)),
      React.createElement('h2', { className: 'asset-lineage-panel-title', title: node.label }, node.label),
      node.meta ? React.createElement('div', { className: 'asset-lineage-panel-key', title: node.meta }, node.meta) : null,
    ),
    React.createElement('div', { className: 'asset-lineage-panel-stats' },
      panelStat('Visible upstream', node.visibleUpstreamCount ?? 0),
      panelStat('Visible downstream', node.visibleDownstreamCount ?? 0),
      panelStat('Uses', node.usesCount ?? 0),
      panelStat('Used by', node.usedByCount ?? 0),
    ),
    React.createElement(
      'div',
      { className: 'asset-lineage-panel-summary' },
      node.containedCount
        ? `${node.containedCount} contained assets: ${node.containedSummary ?? 'mixed assets'}`
        : 'No directly contained assets.',
    ),
    node.href ? React.createElement('a', { className: 'asset-lineage-panel-action', href: node.href }, 'Open details') : null,
  )
}

function panelStat(label: string, value: number) {
  return React.createElement('div', { className: 'asset-lineage-panel-stat' },
    React.createElement('span', null, label),
    React.createElement('strong', null, String(value)),
  )
}

const nodePalette: Record<string, [string, string, string]> = {
  catalog: ['var(--lv-asset-catalog-bg)', 'var(--lv-asset-catalog-accent)', 'var(--lv-asset-catalog-border)'],
  connection: ['var(--lv-asset-connection-bg)', 'var(--lv-asset-connection-accent)', 'var(--lv-asset-connection-border)'],
  dashboard: ['var(--lv-asset-dashboard-bg)', 'var(--lv-asset-dashboard-accent)', 'var(--lv-asset-dashboard-border)'],
  field: ['var(--lv-asset-dimension-bg)', 'var(--lv-asset-dimension-accent)', 'var(--lv-asset-dimension-border)'],
  filter: ['var(--lv-asset-filter-bg)', 'var(--lv-asset-filter-accent)', 'var(--lv-asset-filter-border)'],
  measure: ['var(--lv-asset-measure-bg)', 'var(--lv-asset-measure-accent)', 'var(--lv-asset-measure-border)'],
  model_table: ['var(--lv-asset-model-table-bg)', 'var(--lv-asset-model-table-accent)', 'var(--lv-asset-model-table-border)'],
  page: ['var(--lv-asset-page-bg)', 'var(--lv-asset-page-accent)', 'var(--lv-asset-page-border)'],
  page_item: ['var(--lv-asset-page-bg)', 'var(--lv-asset-page-accent)', 'var(--lv-asset-page-border)'],
  relationship: ['var(--lv-asset-dimension-bg)', 'var(--lv-asset-dimension-accent)', 'var(--lv-asset-dimension-border)'],
  semantic_model: ['var(--lv-asset-semantic-model-bg)', 'var(--lv-asset-semantic-model-accent)', 'var(--lv-asset-semantic-model-border)'],
  semantic_table: ['var(--lv-asset-model-table-bg)', 'var(--lv-asset-model-table-accent)', 'var(--lv-asset-model-table-border)'],
  source: ['var(--lv-asset-source-bg)', 'var(--lv-asset-source-accent)', 'var(--lv-asset-source-border)'],
  table: ['var(--lv-asset-table-bg)', 'var(--lv-asset-table-accent)', 'var(--lv-asset-table-border)'],
  visual: ['var(--lv-asset-visual-bg)', 'var(--lv-asset-visual-accent)', 'var(--lv-asset-visual-border)'],
}

function nodeStyle(node: LineageNode): Record<string, string> {
  const [bg, accent, border] = nodePalette[node.kind] ?? nodePalette.semantic_model
  return {
    '--lineage-node-bg': bg,
    '--lineage-node-accent': node.selected ? 'var(--lv-line-accent)' : accent,
    '--lineage-node-border': border,
  } as Record<string, string>
}

function edgeStroke(kind: string): string {
  if (kind === 'contains') return 'var(--lv-line-muted)'
  if (kind.startsWith('lineage')) return 'var(--lv-line-accent)'
  if (kind.startsWith('uses')) return 'var(--lv-line-accent)'
  if (kind.startsWith('reads')) return 'var(--lv-fg-warning)'
  if (kind.startsWith('filters')) return 'var(--lv-fg-success)'
  return 'var(--lv-line-muted)'
}

function kindLabel(kind: string): string {
  switch (kind) {
    case 'model_table':
      return 'Model table'
    case 'page_item':
      return 'Page item'
    case 'semantic_model':
      return 'Semantic model'
    case 'semantic_table':
      return 'Semantic table'
    default:
      return kind.replaceAll('_', ' ').replace(/\b\w/g, (char) => char.toUpperCase())
  }
}

customElements.define('lv-asset-lineage-graph', AssetLineageGraph)
