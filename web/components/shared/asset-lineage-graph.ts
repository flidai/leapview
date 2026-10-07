import { LitElement, html } from 'lit'
import { property } from 'lit/decorators.js'
import React, { useEffect, useRef } from 'react'
import { Maximize, Minimize } from 'lucide'
import { layoutLineageGraph, LINEAGE_NODE_WIDTH, LINEAGE_NODE_HEIGHT } from './asset-lineage-layout'
import { createRoot, type Root } from 'react-dom/client'
import '@xyflow/react/dist/style.css'
import {
  Background,
  ReactFlowProvider,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  useStore,
  getViewportForBounds,
  useReactFlow,
  type ReactFlowInstance,
  type Edge,
  type Node,
} from '@xyflow/react'

type LineageGraph = {
  nodes: LineageNode[]
  edges: LineageEdge[]
}

type LineageScope = 'focused' | 'full'
type LineageScopeMode = 'dependencies' | 'run'
type LineageViewportState = { mode: 'automatic' | 'overview' | 'manual' }

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
  runStatus?: string
  runStatusLabel?: string
  runAnimate?: boolean
}

type LineageEdge = {
  id: string
  source: string
  target: string
  label?: string
  kind: string
}

type LineagePathState = {
  selectedID?: string
  upstream: Set<string>
  downstream: Set<string>
  connectedEdges: Set<string>
}

type LineageNodeData = LineageNode & {
  pathState: 'neutral' | 'selected' | 'upstream' | 'downstream' | 'unrelated'
  onSelect: (id: string) => void
}

const FIT_OPTIONS = { padding: 0.16, minZoom: 0.02, maxZoom: 1 }
const nodeTypes = { lineageNode: LineageNodeComponent }
type Direction = 'all' | 'upstream' | 'downstream'

class AssetLineageGraph extends LitElement {
  @property({ type: Object }) graph: LineageGraph | null = null
  @property({ attribute: false }) scope: LineageScope = 'focused'
  @property({ attribute: 'scope-mode' }) scopeMode: LineageScopeMode = 'dependencies'
  @property({ attribute: false }) dialogTitle = ''
  private userSelectedNodeID?: string
  private root?: Root
  private mount?: HTMLDivElement
  private selectedNodeID?: string
  private selectionCleared = false
  private direction: Direction = 'all'
  private flow?: ReactFlowInstance
  private inline?: HTMLDivElement
  private dialog?: HTMLDialogElement
  private expanded = false
  private previousOverflow = ''
  private viewportState: LineageViewportState = { mode: 'automatic' }

  private changeViewportMode = (mode: LineageViewportState['mode']): void => {
    if (this.viewportState.mode === mode) return
    this.viewportState.mode = mode
    this.renderFlow()
  }

  private expand(): void {
    if (!this.dialog || !this.mount || this.expanded) return
    this.previousOverflow = this.ownerDocument.documentElement.style.overflow
    this.ownerDocument.documentElement.style.overflow = 'hidden'
    this.expanded = true
    // Move the React-owned container into the top layer without remounting it.
    // This preserves the current selection, direction and all live controls.
    this.dialog.append(this.mount)
    this.dialog.showModal()
    this.renderFlow()
    this.mount.querySelector<HTMLButtonElement>('.asset-lineage-expand')?.focus()
  }

  private collapse(restoreFocus = true): void {
    if (!this.expanded) return
    this.expanded = false
    this.dialog?.close()
    if (this.mount) this.inline?.append(this.mount)
    this.ownerDocument.documentElement.style.overflow = this.previousOverflow
    if (this.isConnected) this.renderFlow()
    if (restoreFocus) this.mount?.querySelector<HTMLButtonElement>('.asset-lineage-expand')?.focus()
  }

  private cancelExpanded(event: Event): void {
    event.preventDefault()
    this.collapse()
  }

  private trapDialogFocus(event: KeyboardEvent): void {
    if (!this.expanded || event.key !== 'Tab' || !this.dialog) return
    const focusable = Array.from(this.dialog.querySelectorAll<HTMLElement>(
      'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
    )).filter(element => element.getClientRects().length > 0)
    const root = this.getRootNode()
    const active = root instanceof ShadowRoot ? root.activeElement : this.ownerDocument.activeElement
    const index = focusable.indexOf(active as HTMLElement)
    if (event.shiftKey && index <= 0) {
      event.preventDefault()
      focusable.at(-1)?.focus()
    } else if (!event.shiftKey && (index < 0 || index === focusable.length - 1)) {
      event.preventDefault()
      focusable[0]?.focus()
    }
  }

  private closedExpanded(): void {
    if (!this.dialog?.open) this.collapse()
  }

  connectedCallback(): void {
    super.connectedCallback()
    if (this.mount && !this.root) {
      this.root = createRoot(this.mount)
      this.renderFlow()
    }
  }

  createRenderRoot(): HTMLElement {
    return this
  }

  firstUpdated(): void {
    this.inline = this.renderRoot.querySelector('.asset-lineage-inline') as HTMLDivElement
    this.dialog = this.renderRoot.querySelector('.asset-lineage-dialog') as HTMLDialogElement
    this.mount = this.ownerDocument.createElement('div')
    this.mount.className = 'asset-lineage-root'
    this.inline.append(this.mount)
    if (this.mount) {
      this.root = createRoot(this.mount)
      this.renderFlow()
    }
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has('graph')) {
      const previousGraph = changed.get('graph') as LineageGraph | null | undefined
      const previousAnchor = previousGraph?.nodes.find(node => node.selected)?.id
      const nextAnchor = this.graph?.nodes.find(node => node.selected)?.id
      // Keep an intentional clear through status refreshes, but honor a new
      // anchor chosen by an external control such as the run's model list.
      if (previousAnchor !== nextAnchor) this.selectionCleared = false
      if (!this.userSelectedNodeID && !this.selectionCleared) this.selectedNodeID = this.graph?.nodes.find(node => node.selected)?.id
      if (this.selectedNodeID !== undefined && !this.graph?.nodes.some((node) => node.id === this.selectedNodeID)) {
        this.selectedNodeID = undefined
        this.userSelectedNodeID = undefined
        this.selectionCleared = false
        this.direction = 'all'
      }
    }
    if (changed.has('graph') || changed.has('scope') || changed.has('scopeMode') || changed.has('dialogTitle')) this.renderFlow()
  }

  disconnectedCallback(): void {
    this.collapse(false)
    this.root?.unmount()
    this.root = undefined
    this.flow = undefined
    super.disconnectedCallback()
  }

  render() {
    return html`
      <style>
        ${assetLineageGraphStyles}
      </style>
      <div class="asset-lineage-inline"></div>
      <dialog class="asset-lineage-dialog" aria-label=${this.dialogTitle.trim() || "Full-page lineage explorer"}
        @cancel=${this.cancelExpanded} @close=${this.closedExpanded} @keydown=${this.trapDialogFocus}></dialog>
    `
  }

  private renderFlow(): void {
    if (!this.root) return
    const graph = this.resolvedGraph
    const selectedNode = this.selectionCleared ? undefined : selectedLineageNode(graph.nodes, this.selectedNodeID)
    this.selectedNodeID = selectedNode?.id
    const pathState = createPathState(graph, this.selectedNodeID)
    const scopeIDs = new Set(this.scope === 'full' ? graph.nodes.map(node => node.id) : focusedLineageNodeIDs(graph, this.selectedNodeID ?? selectedLineageNode(graph.nodes)?.id))
    const visibleIDs = this.direction === 'all' ? new Set(graph.nodes.map((node) => node.id))
      : new Set([...(this.direction === 'upstream' ? pathState.upstream : pathState.downstream), ...(this.selectedNodeID ? [this.selectedNodeID] : [])])
    const nodes = graph.nodes.filter((node) => visibleIDs.has(node.id) && scopeIDs.has(node.id))
    const includedIDs = new Set(nodes.map(node => node.id))
    const edges = graph.edges.filter((edge) => includedIDs.has(edge.source) && includedIDs.has(edge.target))
    const positions = layoutLineageGraph(nodes, edges)
    const nodeRanks = new Map(nodes.map((node) => [node.id, positions.get(node.id)?.x ?? 0]))
    const clearSelection = () => {
      this.selectedNodeID = undefined
      this.userSelectedNodeID = undefined
      this.selectionCleared = true
      this.direction = 'all'
      this.renderFlow()
    }
    const select = (id: string) => {
      this.selectedNodeID = id
      this.userSelectedNodeID = id
      this.selectionCleared = false
      this.direction = 'all'
      this.renderFlow()
      this.dispatchEvent(new CustomEvent('lv-lineage-select', { bubbles: true, composed: true, detail: { id } }))
    }
    const button = (label: string, onClick: () => void, options = {}) => React.createElement('button', {
      type: 'button', onClick, ...options,
    }, label)
    const signature = JSON.stringify([nodes.map((node) => node.id).sort(), edges.map((edge) => [edge.source, edge.target]).sort()])
    this.root.render(React.createElement(ReactFlowProvider, null, React.createElement('div', {
      className: 'asset-lineage-layout',
      onClick: (event: React.MouseEvent) => {
        if (event.target instanceof Element && event.target.matches('.react-flow__renderer')) clearSelection()
      },
      onKeyDown: (event: React.KeyboardEvent) => {
        if (event.key !== 'Escape') return
        event.preventDefault()
        if (this.expanded) this.collapse()
        else clearSelection()
      },
    },
      React.createElement('div', { className: 'asset-lineage-toolbar', 'aria-label': 'Lineage controls' },
        this.expanded && this.dialogTitle.trim() ? React.createElement('h2', { className: 'asset-lineage-dialog-title' }, this.dialogTitle.trim()) : null,
        React.createElement('label', { className: 'asset-lineage-search' },
          'Find asset',
          React.createElement('select', {
            'aria-label': 'Find asset', value: this.selectedNodeID ?? '',
            onChange: (event: React.ChangeEvent<HTMLSelectElement>) => event.target.value ? select(event.target.value) : clearSelection(),
          }, React.createElement('option', { value: '' }, 'Select an asset…'),
          ...[...graph.nodes].sort((a, b) => a.label.localeCompare(b.label)).map((node) =>
            React.createElement('option', { key: node.id, value: node.id }, `${node.label} · ${kindLabel(node.kind)}`))),
        ),
        React.createElement('div', { className: 'asset-lineage-directions', role: 'group', 'aria-label': 'Trace dependencies in this view' },
          button('Show all', () => { this.changeScope('full'); clearSelection() }, { 'aria-pressed': this.direction === 'all' && this.scope === 'full' }),
          ...(['upstream', 'downstream'] as const).map((direction) => button(
            `${direction === 'upstream' ? 'Upstream' : 'Downstream'} (${[...pathState[direction]].filter(id => scopeIDs.has(id)).length})`,
            () => { this.direction = direction; this.renderFlow() },
            { key: direction, 'aria-pressed': this.direction === direction, disabled: !selectedNode || ![...pathState[direction]].some(id => scopeIDs.has(id)) },
          )),
        ),
        button(this.scopeActionLabel, () => this.changeScope(this.scope === 'full' ? 'focused' : 'full'), { className: 'asset-lineage-scope' }),
        button('Focus selected', () => {
          const node = selectedNode ? this.flow?.getNode(selectedNode.id) : undefined
          if (node) {
            this.changeViewportMode('manual')
            void this.flow?.setCenter(node.position.x + LINEAGE_NODE_WIDTH / 2, node.position.y + LINEAGE_NODE_HEIGHT / 2, { zoom: FIT_OPTIONS.maxZoom })
          }
        }, { disabled: !selectedNode }),
      ),
      React.createElement('div', { className: 'asset-lineage-flow', 'aria-label': 'Asset lineage graph' },
        nodes.length ? React.createElement(ReactFlow, {
          nodes: nodes.map((node) => toFlowNode(node, positions, pathState, select)),
          edges: edges.map((edge) => toFlowEdge(edge, pathState, nodeRanks)),
          nodeTypes,
          onInit: (flow: ReactFlowInstance) => { this.flow = flow },
          minZoom: FIT_OPTIONS.minZoom, maxZoom: 2,
          nodesDraggable: false, nodesConnectable: false, nodesFocusable: false,
          edgesFocusable: false, elementsSelectable: true,
          panOnDrag: true, zoomOnScroll: false, preventScrolling: false,
          onPaneClick: clearSelection,
          onMoveStart: (event: MouseEvent | TouchEvent | null) => { if (event) this.changeViewportMode('manual') },
          children: [
            React.createElement(Background, { key: 'background', gap: 18, size: 1 }),
            React.createElement(FitLineage, { key: 'fit', viewportState: this.viewportState, signature, scope: this.scope, selectedID: this.selectedNodeID, anchorID: selectedLineageNode(graph.nodes)?.id }),
          ],
        }) : React.createElement('div', { className: 'asset-lineage-empty', role: 'status' }, 'No assets in this lineage.'),
      ),
      nodes.length ? React.createElement(LineageViewportControls, { viewportState: this.viewportState, onModeChange: this.changeViewportMode, expanded: this.expanded, onToggleExpanded: () => this.expanded ? this.collapse() : this.expand() }) : null,
      React.createElement('div', { className: 'asset-lineage-summary', role: 'status' },
        React.createElement('span', null, `${nodes.length} of ${graph.nodes.length} assets in this view · ${edges.length} connections`),
        selectedNode ? React.createElement('span', { className: 'asset-lineage-selection' },
          React.createElement('strong', null, selectedNode.label),
          selectedNode.containedSummary ? React.createElement('span', null, selectedNode.containedSummary) : null,
          selectedNode.href ? React.createElement('a', { href: selectedNode.href }, 'Open asset') : null,
        ) : React.createElement('span', null, 'Select an asset to trace its dependencies. Data flows left to right.'),
      ),
    )))
  }

  private changeScope(scope: LineageScope): void {
    if (scope === this.scope) return
    this.scope = scope
    this.direction = 'all'
    this.dispatchEvent(new CustomEvent('lv-lineage-scope-change', { bubbles: true, composed: true, detail: { scope } }))
    this.renderFlow()
  }

  private get scopeActionLabel(): string {
    if (this.scopeMode === 'run') return this.scope === 'full' ? 'Show focused path' : 'Show full run graph'
    return this.scope === 'full' ? 'Show direct dependencies' : 'Show all upstream'
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
  lv-asset-lineage-graph .asset-lineage-inline { height: 100%; min-height: 0; }
  lv-asset-lineage-graph .asset-lineage-dialog {
    position: fixed;
    inset: 0;
    width: 100vw;
    height: 100dvh;
    max-width: none;
    max-height: none;
    margin: 0;
    border: 0;
    padding: 0;
    overflow: hidden;
    overscroll-behavior: contain;
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
  }
  lv-asset-lineage-graph .asset-lineage-dialog::backdrop { background: var(--lv-bg-panel); }

  lv-asset-lineage-graph { container-type: inline-size; }
  lv-asset-lineage-graph .asset-lineage-toolbar,
  lv-asset-lineage-graph .asset-lineage-summary {
    display: flex; align-items: center; flex-wrap: wrap; gap: 10px;
    padding: 12px 16px; background: var(--lv-bg-panel); font: var(--lv-type-caption);
    color: var(--lv-fg-muted);
  }
  lv-asset-lineage-graph .asset-lineage-dialog-title { margin: 0; flex-basis: 100%; font: var(--lv-type-body); color: var(--lv-fg-default); }
  lv-asset-lineage-graph .asset-lineage-toolbar { border-bottom: var(--lv-border-default); }
  lv-asset-lineage-graph .asset-lineage-summary { justify-content: space-between; border-top: var(--lv-border-default); }
  lv-asset-lineage-graph .asset-lineage-search { display: flex; align-items: center; gap: 8px; flex: 1; min-width: 180px; white-space: nowrap; }
  lv-asset-lineage-graph .asset-lineage-search select { min-width: 0; width: 100%; max-width: 280px; }
  lv-asset-lineage-graph .asset-lineage-directions { display: flex; gap: 4px; }
  lv-asset-lineage-graph .asset-lineage-toolbar button,
  lv-asset-lineage-graph .asset-lineage-toolbar select {
    border: var(--lv-border-default); border-radius: var(--borderRadius-default);
    background: var(--lv-bg-panel); color: var(--lv-fg-default); font: inherit;
    min-height: 32px; padding: 6px 10px;
  }
  lv-asset-lineage-graph button { cursor: pointer; }
  lv-asset-lineage-graph button:disabled { cursor: default; opacity: .45; }
  lv-asset-lineage-graph .asset-lineage-toolbar button[aria-pressed="true"] {
    color: var(--lv-fg-link); border-color: var(--lv-line-accent);
    background: color-mix(in srgb, var(--lv-line-accent) 8%, var(--lv-bg-panel));
  }
  lv-asset-lineage-graph button:focus-visible,
  lv-asset-lineage-graph select:focus-visible,
  lv-asset-lineage-graph a:focus-visible { outline: 2px solid var(--lv-line-accent); outline-offset: 2px; }
  lv-asset-lineage-graph .asset-lineage-selection { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
  lv-asset-lineage-graph .asset-lineage-selection strong { color: var(--lv-fg-default); overflow-wrap: anywhere; }
  lv-asset-lineage-graph .asset-lineage-selection a { color: var(--lv-fg-link); white-space: nowrap; }
  lv-asset-lineage-graph .asset-lineage-empty { display: grid; height: 100%; place-content: center; color: var(--lv-fg-muted); }
  @container (max-width: 600px) {
    lv-asset-lineage-graph .asset-lineage-search { flex-basis: 100%; }
    lv-asset-lineage-graph .asset-lineage-search select { max-width: none; }
    lv-asset-lineage-graph .asset-lineage-toolbar { padding: 8px; gap: 6px; }
    lv-asset-lineage-graph .asset-lineage-toolbar button { padding: 6px; }
  }

  lv-asset-lineage-graph .asset-lineage-root,
  lv-asset-lineage-graph .asset-lineage-layout {
    height: 100%;
    min-height: 0;
    min-width: 0;
  }

  lv-asset-lineage-graph .asset-lineage-layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: auto minmax(120px, 1fr) auto auto;
    overflow: auto;
    outline: 0;
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
    pointer-events: all;
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

  lv-asset-lineage-graph .react-flow__attribution { display: none; }

  lv-asset-lineage-graph .asset-lineage-viewport-controls {
    justify-self: start;
    margin: var(--base-size-12);
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 4px;
    border: var(--lv-border-default);
    border-radius: var(--borderRadius-default);
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-resting-small);
    color: var(--lv-fg-default);
    font: var(--lv-type-caption);
  }
  lv-asset-lineage-graph .asset-lineage-viewport-controls button {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 36px;
    min-width: 36px;
    border: 0;
    border-radius: var(--borderRadius-default);
    padding: 0 8px;
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    font: inherit;
  }
  lv-asset-lineage-graph .asset-lineage-viewport-controls button:not(:disabled):hover {
    background: var(--lv-bg-panel-muted);
  }
  lv-asset-lineage-graph .asset-lineage-zoom-level {
    width: 44px;
    text-align: center;
    font-variant-numeric: tabular-nums;
  }
  lv-asset-lineage-graph .asset-lineage-viewport-controls .asset-lineage-fit {
    border-left: var(--lv-border-default);
    border-radius: 0;
    padding-inline: 12px;
  }

  lv-asset-lineage-graph .asset-lineage-node {
    box-sizing: border-box;
    width: ${LINEAGE_NODE_WIDTH}px;
    height: ${LINEAGE_NODE_HEIGHT}px;
    display: flex;
    flex-direction: column;
    justify-content: center;
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
    filter: saturate(0.35);
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

  lv-asset-lineage-graph .asset-lineage-node-run-status {
    display: inline-flex;
    width: fit-content;
    margin-top: var(--base-size-6);
    border: 1px solid currentColor;
    border-radius: 999px;
    padding: 1px var(--base-size-6);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    line-height: 1.25;
  }

  lv-asset-lineage-graph .asset-lineage-node-run-running .asset-lineage-node-run-status { color: var(--lv-fg-accent); }
  lv-asset-lineage-graph .asset-lineage-node-run-animated .asset-lineage-node-run-status {
    animation: lineage-running-pulse 1.8s ease-in-out infinite;
  }

  lv-asset-lineage-graph .asset-lineage-node-run-succeeded .asset-lineage-node-run-status { color: var(--lv-fg-success); }
  lv-asset-lineage-graph .asset-lineage-node-run-failed .asset-lineage-node-run-status,
  lv-asset-lineage-graph .asset-lineage-node-run-cancelled .asset-lineage-node-run-status { color: var(--lv-fg-danger); }
  lv-asset-lineage-graph .asset-lineage-node-run-prepared .asset-lineage-node-run-status { color: var(--lv-fg-warning); }

  @keyframes lineage-running-pulse {
    50% { box-shadow: 0 0 0 4px color-mix(in srgb, var(--lv-fg-accent), transparent 82%); }
  }

  @media (prefers-reduced-motion: reduce) {
    lv-asset-lineage-graph .asset-lineage-node-run-animated .asset-lineage-node-run-status { animation: none; }
  }

`

const ZOOM_LEVELS = [0.02, 0.05, 0.1, 0.15, 0.25, 0.5, 0.75, 1, 1.25, 1.5, 2]

function LineageViewportControls({ viewportState, onModeChange, expanded, onToggleExpanded }: { viewportState: LineageViewportState; onModeChange: (mode: LineageViewportState['mode']) => void; expanded: boolean; onToggleExpanded: () => void }) {
  const { getNodes, getNodesBounds, getViewport, setViewport } = useReactFlow()
  const zoom = useStore((state) => state.transform[2])
  const x = useStore((state) => state.transform[0])
  const y = useStore((state) => state.transform[1])
  const width = useStore((state) => state.width)
  const height = useStore((state) => state.height)
  const bounds = getNodesBounds(getNodes())
  const fitted = getViewportForBounds(bounds, width, height, FIT_OPTIONS.minZoom, FIT_OPTIONS.maxZoom, FIT_OPTIONS.padding)
  const isFitted = Math.abs(fitted.x - x) < 1 && Math.abs(fitted.y - y) < 1 && Math.abs(fitted.zoom - zoom) < 0.001
  // Read the latest viewport inside each click so rapid clicks cannot reuse a
  // stale zoom or queue competing animations. Keep the viewport centre fixed.
  const zoomTo = (next: number) => {
    onModeChange('manual')
    const current = getViewport()
    const ratio = next / current.zoom
    void setViewport({
      x: width / 2 - (width / 2 - current.x) * ratio,
      y: height / 2 - (height / 2 - current.y) * ratio,
      zoom: next,
    })
  }
  const step = (direction: 'in' | 'out') => {
    const current = getViewport().zoom
    const levels = direction === 'in' ? ZOOM_LEVELS : [...ZOOM_LEVELS].reverse()
    const next = levels.find(level => direction === 'in' ? level > current + 0.001 : level < current - 0.001)
    if (next !== undefined) zoomTo(next)
  }
  const control = (text: string, label: string, title: string, onClick: () => void, disabled: boolean, className?: string) =>
    React.createElement('button', { type: 'button', 'aria-label': label, title, onClick, disabled, className }, text)
  return React.createElement('div', { className: 'asset-lineage-viewport-controls', role: 'group', 'aria-label': 'Graph view' },
    control('−', 'Zoom out', 'Zoom out to see more of the graph', () => step('out'), zoom <= ZOOM_LEVELS[0]! + 0.001),
    React.createElement('output', { className: 'asset-lineage-zoom-level', role: 'status', 'aria-label': 'Zoom level', 'aria-live': 'polite' }, `${Math.round(zoom * 100)}%`),
    control('+', 'Zoom in', 'Zoom in to read asset details', () => step('in'), zoom >= 2 - 0.001),
    control('Fit graph', 'Fit graph', isFitted && viewportState.mode === 'overview' ? 'All visible assets already fit in the graph' : 'Centre and fit all visible assets', () => { onModeChange('overview'); void setViewport(fitted) }, isFitted && viewportState.mode === 'overview', 'asset-lineage-fit'),
    React.createElement('button', {
      type: 'button', className: 'asset-lineage-expand', onClick: onToggleExpanded,
      'aria-label': expanded ? 'Exit full page' : 'Expand to full page', 'aria-expanded': expanded,
      title: expanded ? 'Exit full page (Esc)' : 'Expand lineage to the full page',
    }, React.createElement('svg', {
      width: 16, height: 16, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor',
      strokeWidth: 2, strokeLinecap: 'round', strokeLinejoin: 'round', 'aria-hidden': true,
    }, ...(expanded ? Minimize : Maximize).map(([tag, attrs], key) => React.createElement(tag, { ...attrs, key })))),
  )
}

function FitLineage({ viewportState, signature, scope, selectedID, anchorID }: { viewportState: LineageViewportState; signature: string; scope: LineageScope; selectedID?: string; anchorID?: string }) {
  const { getNodes, getNode, getNodesBounds, getViewport, setCenter, setViewport } = useReactFlow()
  const previous = useRef<{ signature: string; scope: LineageScope; selectedID?: string } | undefined>(undefined)
  const dimensions = useRef<{ width: number; height: number } | undefined>(undefined)
  const domNode = useStore((state) => state.domNode)
  useEffect(() => {
    if (!domNode) return
    let frame = 0
    const scopeChanged = previous.current !== undefined && previous.current.scope !== scope
    const selectionChanged = previous.current !== undefined && previous.current.selectedID !== selectedID
    const topologyChanged = previous.current?.signature !== signature
    previous.current = { signature, scope, selectedID }
    let centerChangedSelection = scopeChanged || Boolean(selectedID && topologyChanged && (selectionChanged || viewportState.mode === 'manual'))
    const resize = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        const { width, height } = domNode.getBoundingClientRect()
        if (!width || !height) return
        const oldDimensions = dimensions.current
        dimensions.current = { width, height }
        if (centerChangedSelection) {
          centerChangedSelection = false
          const node = selectedID || anchorID ? getNode((selectedID ?? anchorID)!) : undefined
          if (node) {
            void setCenter(node.position.x + LINEAGE_NODE_WIDTH / 2, node.position.y + LINEAGE_NODE_HEIGHT / 2, { zoom: getViewport().zoom })
            return
          }
        }
        if (viewportState.mode === 'manual' && oldDimensions) {
          const current = getViewport()
          void setViewport({ ...current, x: current.x + (width - oldDimensions.width) / 2, y: current.y + (height - oldDimensions.height) / 2 })
          return
        }
        const nodes = getNodes()
        const fitted = getViewportForBounds(getNodesBounds(nodes), width, height, FIT_OPTIONS.minZoom, FIT_OPTIONS.maxZoom, FIT_OPTIONS.padding)
        const preferredAnchorID = selectedID ?? anchorID
        const anchor = (preferredAnchorID ? getNode(preferredAnchorID) : undefined) ?? nodes[0]
        // Automatic views keep an asset readable; Fit graph explicitly requests the complete overview.
        void setViewport(viewportState.mode === 'automatic' && fitted.zoom < FIT_OPTIONS.maxZoom && anchor
          ? getViewportForBounds(getNodesBounds([anchor]), width, height, FIT_OPTIONS.minZoom, FIT_OPTIONS.maxZoom, FIT_OPTIONS.padding)
          : fitted)
      })
    }
    let observedWidth = domNode.clientWidth
    let observedHeight = domNode.clientHeight
    const observer = new ResizeObserver(() => {
      if (domNode.clientWidth === observedWidth && domNode.clientHeight === observedHeight) return
      observedWidth = domNode.clientWidth
      observedHeight = domNode.clientHeight
      resize()
    })
    observer.observe(domNode)
    if (topologyChanged || scopeChanged) resize()
    return () => { observer.disconnect(); cancelAnimationFrame(frame) }
  }, [viewportState, signature, scope, selectedID, anchorID, getNodes, getNode, getNodesBounds, getViewport, setCenter, setViewport, domNode])
  return null
}

function toFlowNode(node: LineageNode, positions: Map<string, { x: number; y: number }>, pathState: LineagePathState, onSelect: (id: string) => void): Node<LineageNodeData> {
  return {
    id: node.id, type: 'lineageNode', width: LINEAGE_NODE_WIDTH, height: LINEAGE_NODE_HEIGHT, position: positions.get(node.id) ?? { x: 0, y: 0 },
    sourcePosition: Position.Right, targetPosition: Position.Left,
    className: `asset-lineage-flow-node asset-lineage-flow-node-${nodePathState(node.id, pathState)}`,
    data: { ...node, selected: node.id === pathState.selectedID, pathState: nodePathState(node.id, pathState), onSelect },
  }
}

function toFlowEdge(edge: LineageEdge, pathState: LineagePathState, nodeRanks: Map<string, number>): Edge {
  const context = edge.kind === 'contains'
  const connected = edge.source === pathState.selectedID || edge.target === pathState.selectedID || pathState.connectedEdges.has(edge.id)
  const muted = pathState.selectedID ? !connected : false
  return {
    id: edge.id,
    source: edge.source,
    target: edge.target,
    // Node titles and the relationship tables carry the useful explanation.
    // Raw backend relationship labels make projected graphs noisy and can
    // describe the inverse data direction (for example, "Feeds model").
    label: '',
    type: nodeRanks.get(edge.source) === nodeRanks.get(edge.target) ? 'default' : 'smoothstep',
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

function focusedLineageNodeIDs(graph: LineageGraph, selectedID?: string): string[] {
  if (!selectedID) return graph.nodes.map((node) => node.id)
  const focused = new Set([selectedID])
  for (const edge of graph.edges) {
    if (edge.source === selectedID) focused.add(edge.target)
    if (edge.target === selectedID) focused.add(edge.source)
  }
  return graph.nodes.filter((node) => focused.has(node.id)).map((node) => node.id)
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
  state.upstream.delete(selectedID)
  state.downstream.delete(selectedID)
  return state
}

function walkLineagePath(
  nodeID: string,
  edgesByNode: Map<string, LineageEdge[]>,
  peerKey: 'source' | 'target',
  seenNodes: Set<string>,
  seenEdges: Set<string>,
): void {
  const pending = [nodeID]
  const expanded = new Set<string>()
  while (pending.length) {
    const current = pending.pop()!
    if (expanded.has(current)) continue
    expanded.add(current)
    for (const edge of edgesByNode.get(current) ?? []) {
      seenEdges.add(edge.id)
      seenNodes.add(edge[peerKey])
      if (!expanded.has(edge[peerKey])) pending.push(edge[peerKey])
    }
  }
}

function nodePathState(id: string, pathState: LineagePathState): 'neutral' | 'selected' | 'upstream' | 'downstream' | 'unrelated' {
  if (!pathState.selectedID) return 'neutral'
  if (id === pathState.selectedID) return 'selected'
  if (pathState.upstream.has(id)) return 'upstream'
  if (pathState.downstream.has(id)) return 'downstream'
  return 'unrelated'
}

function LineageNodeComponent({ data }: { data: LineageNodeData }) {
  const styles = nodeStyle(data)
  const { getNode, getViewport, setCenter } = useReactFlow()
  const onFocus = (event: React.FocusEvent<HTMLDivElement>) => {
    // Pointer focus must not move the target before its click completes.
    if (!event.currentTarget.matches(':focus-visible')) return
    const bounds = event.currentTarget.getBoundingClientRect()
    const surface = event.currentTarget.closest('.asset-lineage-flow')?.getBoundingClientRect()
    if (!surface || (bounds.left >= surface.left && bounds.right <= surface.right && bounds.top >= surface.top && bounds.bottom <= surface.bottom)) return
    const node = getNode(data.id)
    if (node) void setCenter(node.position.x + LINEAGE_NODE_WIDTH / 2, node.position.y + LINEAGE_NODE_HEIGHT / 2, { zoom: getViewport().zoom })
  }
  const runStatus = visibleRunStatus(data.runStatus)
  const className = [
    'asset-lineage-node',
    data.selected ? 'asset-lineage-node-selected' : '',
    `asset-lineage-node-${data.pathState}`,
    runStatus ? `asset-lineage-node-run-${runStatus}` : '',
    runStatus === 'running' && data.runAnimate ? 'asset-lineage-node-run-animated' : '',
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
      'aria-label': `${kindLabel(data.kind)} ${data.label}${runStatus ? `, ${data.runStatusLabel || runStatus}` : ''}`,
      onClick: select,
      onFocus,
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
    runStatus ? React.createElement('span', { className: 'asset-lineage-node-run-status' }, data.runStatusLabel || kindLabel(runStatus)) : null,
    React.createElement(Handle, { type: 'source', position: Position.Right }),
  )
}

function visibleRunStatus(status?: string): string | undefined {
  switch (status) {
    case 'queued':
    case 'running':
    case 'prepared':
    case 'succeeded':
    case 'failed':
    case 'cancelled':
    case 'superseded':
    case 'skipped':
      return status
    default:
      return undefined
  }
}

const nodePalette: Record<string, [string, string, string]> = {
  catalog: ['var(--lv-asset-catalog-bg)', 'var(--lv-asset-catalog-accent)', 'var(--lv-asset-catalog-border)'],
  connection: ['var(--lv-asset-connection-bg)', 'var(--lv-asset-connection-accent)', 'var(--lv-asset-connection-border)'],
  dashboard: ['var(--lv-asset-dashboard-bg)', 'var(--lv-asset-dashboard-accent)', 'var(--lv-asset-dashboard-border)'],
  field: ['var(--lv-asset-dimension-bg)', 'var(--lv-asset-dimension-accent)', 'var(--lv-asset-dimension-border)'],
  filter: ['var(--lv-asset-filter-bg)', 'var(--lv-asset-filter-accent)', 'var(--lv-asset-filter-border)'],
  metric: ['var(--lv-asset-metric-bg)', 'var(--lv-asset-metric-accent)', 'var(--lv-asset-metric-border)'],
  model: ['var(--lv-asset-model-bg)', 'var(--lv-asset-model-accent)', 'var(--lv-asset-model-border)'],
  page: ['var(--lv-asset-page-bg)', 'var(--lv-asset-page-accent)', 'var(--lv-asset-page-border)'],
  page_item: ['var(--lv-asset-page-bg)', 'var(--lv-asset-page-accent)', 'var(--lv-asset-page-border)'],
  relationship: ['var(--lv-asset-dimension-bg)', 'var(--lv-asset-dimension-accent)', 'var(--lv-asset-dimension-border)'],
  semantic_model: ['var(--lv-asset-semantic-model-bg)', 'var(--lv-asset-semantic-model-accent)', 'var(--lv-asset-semantic-model-border)'],
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
  return 'var(--lv-fg-muted)'
}

function kindLabel(kind: string): string {
  switch (kind) {
    case 'model':
      return 'Model'
    case 'page_item':
      return 'Page item'
    case 'semantic_model':
      return 'Semantic model'
    default:
      return kind.replaceAll('_', ' ').replace(/\b\w/g, (char) => char.toUpperCase())
  }
}

customElements.define('lv-asset-lineage-graph', AssetLineageGraph)
