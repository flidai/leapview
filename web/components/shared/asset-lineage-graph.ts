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
  type ReactFlowInstance,
} from '@xyflow/react'

type LineageGraph = {
  nodes: LineageNode[]
  edges: LineageEdge[]
}

type LineageScope = 'focused' | 'full'
type LineageScopeMode = 'dependencies' | 'run'

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
  // Run-only overlay. Definition and Overview graphs do not supply this.
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

type LineageLayout = {
  rankIndex: Map<number, number>
  nodeIndex: Map<string, number>
  rankNodeCount: Map<number, number>
  maxNodeCount: number
  nodeGapY: number
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

const NODE_GAP_X = 260
const NODE_GAP_Y = 124
const DENSE_NODE_GAP_Y = 88
const NODE_OFFSET_X = 96
const NODE_MIN_Y = 48
const NARROW_FOCUS_GAP_Y = 132
const FIT_MIN_ZOOM = 0.4
const FIT_MAX_ZOOM = 1
const NARROW_GRAPH_WIDTH = 460

class AssetLineageGraph extends LitElement {
  @property({ type: Object }) graph: LineageGraph | null = null
  @property({ attribute: false }) scope: LineageScope = 'focused'
  @property({ attribute: 'scope-mode' }) scopeMode: LineageScopeMode = 'dependencies'
  @property({ attribute: false }) dialogTitle = ''
  private root?: Root
  private mount?: HTMLDivElement
  private initialFitScope?: LineageScope
  private flow?: ReactFlowInstance
  private resizeObserver?: ResizeObserver
  private fitFrame?: number
  private selectedNodeID?: string
  private userSelectedNodeID?: string
  private selectionCleared = false
  private lastFitKey = ''
  private skipScopeFit = false
  private expanded = false
  private restoreFocusTo?: HTMLElement

  createRenderRoot(): HTMLElement {
    return this
  }

  firstUpdated(): void {
    this.initialFitScope = this.scope
    this.mount = this.renderRoot.querySelector('.asset-lineage-root') as HTMLDivElement | null ?? undefined
    if (this.mount) {
      this.root = createRoot(this.mount)
      this.resizeObserver = new ResizeObserver(() => {
        this.renderFlow()
        if (this.expanded) this.scheduleCenterSelectedAtCurrentZoom(this.selectedNodeID)
      })
      this.resizeObserver.observe(this)
      this.resizeObserver.observe(this.mount)
      this.renderFlow()
    }
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has('graph')) {
      this.selectionCleared = false
      const nodes = this.resolvedGraph.nodes
      this.userSelectedNodeID = this.userSelectedNodeID && nodes.some((node) => node.id === this.userSelectedNodeID)
        ? this.userSelectedNodeID
        : undefined
      this.selectedNodeID = this.userSelectedNodeID ?? nodes.find((node) => node.selected)?.id
    }
    if (changed.has('graph') || changed.has('scope') || changed.has('scopeMode') || changed.has('dialogTitle')) {
      this.renderFlow()
    }
  }

  disconnectedCallback(): void {
    this.resizeObserver?.disconnect()
    if (this.fitFrame !== undefined) cancelAnimationFrame(this.fitFrame)
    const dialog = this.querySelector<HTMLDialogElement>('.asset-lineage-dialog')
    if (dialog?.open) dialog.close()
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
    const dialogTitle = this.dialogTitle.trim() || 'Expanded dependency graph'
    const graph = this.resolvedGraph
    const layout = createLineageLayout(graph.nodes)
    const nodeRanks = new Map(graph.nodes.map((node) => [node.id, nodeRank(node)]))
    const selectedNode = this.selectionCleared ? undefined : selectedLineageNode(graph.nodes, this.selectedNodeID)
    this.selectedNodeID = selectedNode?.id
    const pathState = createPathState(graph, this.selectedNodeID)
    const narrow = this.clientWidth > 0 && this.clientWidth <= NARROW_GRAPH_WIDTH
    const scopeAnchorID = this.selectedNodeID ?? selectedLineageNode(graph.nodes)?.id
    const visibleNodeIDs = this.nodeIDsInScope(graph, scopeAnchorID)
    const visibleNodeSet = new Set(visibleNodeIDs)
    const focusPositions = this.scope === 'focused' && this.selectedNodeID
      ? createFocusedPositions(graph, layout, this.selectedNodeID, narrow)
      : undefined
    const fitKey = `${this.selectedNodeID ?? '*'}:${narrow}`
    const shouldRefit = this.lastFitKey !== '' && this.lastFitKey !== fitKey && this.scope === 'focused' && !this.skipScopeFit
    this.skipScopeFit = false
    this.lastFitKey = fitKey
    const clearSelection = () => {
      if (!this.selectedNodeID && this.selectionCleared) return
      this.selectedNodeID = undefined
      this.userSelectedNodeID = undefined
      this.selectionCleared = true
      this.renderFlow()
    }
    this.root.render(
      React.createElement(
        'dialog',
        {
          className: 'asset-lineage-dialog',
          tabIndex: -1,
          role: this.expanded ? 'dialog' : 'presentation',
          'aria-modal': this.expanded ? 'true' : undefined,
          'aria-label': this.expanded ? dialogTitle : undefined,
          onCancel: (event: React.SyntheticEvent<HTMLDialogElement>) => {
            event.preventDefault()
            this.closeExpanded()
          },
          onClick: (event: React.MouseEvent<HTMLDialogElement>) => {
            const target = event.target
            if (!(target instanceof Element)) return
            if (this.expanded && target === event.currentTarget) {
              this.closeExpanded()
              return
            }
            if (!this.expanded && !target.closest('.react-flow__node, button')) clearSelection()
          },
          onKeyDown: this.handleDialogKeyDown,
        },
        React.createElement(
          'div',
          { className: 'asset-lineage-layout' },
          React.createElement('h2', { className: 'asset-lineage-dialog-title', hidden: !this.expanded }, dialogTitle),
          React.createElement(
            'div',
            { className: 'asset-lineage-actions', role: 'group', 'aria-label': 'Graph actions' },
            React.createElement('button', {
              type: 'button',
              title: this.scope === 'full' ? 'Show the focused path for the selected asset' : 'Show the complete dependency graph',
              onClick: () => this.toggleScope(),
            }, this.scopeActionLabel),
            React.createElement('button', {
              type: 'button',
              title: 'Fit currently included nodes in the viewport without changing graph scope',
              'aria-description': 'Adjusts the viewport to show included nodes; graph scope stays the same.',
              onClick: () => this.fitToScope(graph, this.selectedNodeID),
            }, 'Fit'),
            this.expanded
              ? React.createElement('button', { type: 'button', onClick: () => this.closeExpanded() }, 'Close graph')
              : React.createElement('button', {
                type: 'button',
                'aria-pressed': 'false',
                onClick: () => this.openExpanded(),
              }, 'Expand graph'),
          ),
          React.createElement(
            'div',
            { className: 'asset-lineage-flow', 'aria-label': 'Asset lineage graph' },
            React.createElement(ReactFlow, {
              nodes: graph.nodes.filter((node) => visibleNodeSet.has(node.id)).map((node) => toFlowNode(node, layout, pathState, (id) => {
                this.selectedNodeID = id
                this.userSelectedNodeID = id
                this.selectionCleared = false
                this.renderFlow()
                this.dispatchEvent(new CustomEvent('lv-lineage-select', { bubbles: true, composed: true, detail: { id } }))
              }, focusPositions?.get(node.id), focusPositions !== undefined && narrow)),
              edges: graph.edges
                .filter((edge) => visibleNodeSet.has(edge.source) && visibleNodeSet.has(edge.target))
                .map((edge) => toFlowEdge(edge, pathState, nodeRanks)),
              nodeTypes: { lineageNode: LineageNodeComponent },
              onInit: (instance: ReactFlowInstance) => {
                this.flow = instance
                const initialFitScope = this.initialFitScope
                this.initialFitScope = undefined
                if (initialFitScope && initialFitScope === this.scope) {
                  this.scheduleFit(this.resolvedGraph, this.selectedNodeID, initialFitScope)
                }
              },
              fitView: false,
              minZoom: 0.25,
              maxZoom: 1.35,
              nodesDraggable: false,
              nodesConnectable: false,
              elementsSelectable: true,
              panOnDrag: true,
              zoomOnScroll: false,
              preventScrolling: false,
              onPaneClick: clearSelection,
              children: [
                React.createElement(Background, { key: 'background', gap: 18, size: 1 }),
                React.createElement(Controls, { key: 'controls', showFitView: false, showInteractive: false }),
              ],
            }),
          ),
        ),
      ),
    )
    if (shouldRefit) this.scheduleFit(graph, this.selectedNodeID, this.scope)
  }

  private scheduleFit(graph: LineageGraph, selectedID: string | undefined, scope: LineageScope): void {
    if (this.fitFrame !== undefined) cancelAnimationFrame(this.fitFrame)
    this.fitFrame = requestAnimationFrame(() => {
      this.fitFrame = requestAnimationFrame(() => {
        this.fitFrame = undefined
        this.fitToScope(graph, selectedID, scope, 0)
      })
    })
  }

  private fitToScope(graph: LineageGraph, selectedID?: string, scope = this.scope, duration = 180): void {
    const ids = this.nodeIDsInScope(graph, selectedID ?? selectedLineageNode(graph.nodes)?.id, scope)
    void this.flow?.fitView({
      nodes: ids.map((id) => ({ id })),
      padding: 0.08,
      minZoom: FIT_MIN_ZOOM,
      maxZoom: FIT_MAX_ZOOM,
      duration,
    })
  }

  private toggleScope(): void {
    const graph = this.resolvedGraph
    const selectedID = this.selectedNodeID ?? selectedLineageNode(graph.nodes)?.id
    if (this.fitFrame !== undefined) cancelAnimationFrame(this.fitFrame)
    this.fitFrame = undefined
    this.initialFitScope = undefined
    this.scope = this.scope === 'focused' ? 'full' : 'focused'
    this.skipScopeFit = true
    this.renderFlow()
    this.dispatchEvent(new CustomEvent('lv-lineage-scope-change', {
      bubbles: true,
      composed: true,
      detail: { scope: this.scope },
    }))
    this.scheduleCenterSelectedAtCurrentZoom(selectedID)
  }

  private scheduleCenterSelectedAtCurrentZoom(selectedID?: string): void {
    if (!selectedID || !this.flow) return
    requestAnimationFrame(() => requestAnimationFrame(() => {
      const node = this.flow?.getNode(selectedID)
      if (!node) return
      const zoom = this.flow?.getZoom() ?? 1
      const width = node.measured?.width ?? node.width ?? 200
      const height = node.measured?.height ?? node.height ?? 96
      void this.flow?.setCenter(node.position.x + width / 2, node.position.y + height / 2, { zoom, duration: 0 })
    }))
  }

  private openExpanded(): void {
    if (this.expanded) return
    this.restoreFocusTo = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
    const dialog = this.querySelector<HTMLDialogElement>('.asset-lineage-dialog')
    if (!dialog) return
    this.expanded = true
    dialog.showModal()
    this.renderFlow()
    this.scheduleCenterSelectedAtCurrentZoom(this.selectedNodeID)
    requestAnimationFrame(() => this.querySelector<HTMLButtonElement>('.asset-lineage-actions button:last-child')?.focus())
  }

  private closeExpanded(): void {
    if (!this.expanded) return
    this.expanded = false
    const dialog = this.querySelector<HTMLDialogElement>('.asset-lineage-dialog')
    if (dialog?.open) dialog.close()
    this.renderFlow()
    this.scheduleCenterSelectedAtCurrentZoom(this.selectedNodeID)
    const restoreFocusTo = this.restoreFocusTo
    this.restoreFocusTo = undefined
    requestAnimationFrame(() => restoreFocusTo?.focus())
  }

  private readonly handleDialogKeyDown = (event: React.KeyboardEvent<HTMLDialogElement>): void => {
    if (this.expanded && event.key === 'Tab') {
      const dialog = event.currentTarget
      const focusable = Array.from(dialog.querySelectorAll<HTMLElement>(
        'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      )).filter((element) => element.getClientRects().length > 0)
      if (!focusable.length) {
        event.preventDefault()
        dialog.focus()
        return
      }
      const root = dialog.getRootNode()
      const active = root instanceof ShadowRoot ? root.activeElement : document.activeElement
      const activeIndex = focusable.findIndex((element) => element === active)
      if (event.shiftKey && activeIndex <= 0) {
        event.preventDefault()
        focusable[focusable.length - 1]?.focus()
      } else if (!event.shiftKey && (activeIndex === focusable.length - 1 || activeIndex < 0)) {
        event.preventDefault()
        focusable[0]?.focus()
      }
      return
    }
    if (event.key === 'Escape' && !this.expanded) {
      event.preventDefault()
      if (!this.selectedNodeID && this.selectionCleared) return
      this.selectedNodeID = undefined
      this.userSelectedNodeID = undefined
      this.selectionCleared = true
      this.renderFlow()
    }
  }

  private nodeIDsInScope(graph: LineageGraph, selectedID?: string, scope = this.scope): string[] {
    if (scope === 'full') return graph.nodes.map((node) => node.id)
    const anchorID = selectedID ?? selectedLineageNode(graph.nodes)?.id
    return focusedLineageNodeIDs(graph, anchorID)
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
  lv-asset-lineage-graph .asset-lineage-dialog:not([open]) {
    position: static;
    inset: auto;
    display: block;
    box-sizing: border-box;
    width: 100%;
    height: 100%;
    max-width: none;
    max-height: none;
    margin: 0;
    border: 0;
    padding: 0;
    overflow: visible;
    background: transparent;
    color: inherit;
  }

  lv-asset-lineage-graph .asset-lineage-dialog[open] {
    position: fixed;
    inset: 0;
    z-index: var(--zIndex-modal, 1200);
    box-sizing: border-box;
    display: block;
    width: 100vw;
    height: 100svh;
    max-width: none;
    max-height: none;
    margin: 0;
    border: 0;
    padding: var(--base-size-16, 16px);
    overflow: hidden;
    background: transparent;
    color: inherit;
  }

  lv-asset-lineage-graph .asset-lineage-dialog::backdrop {
    background: var(--lv-modal-backdrop, rgb(0 0 0 / 56%));
  }

  lv-asset-lineage-graph .asset-lineage-dialog[open] .asset-lineage-root {
    box-sizing: border-box;
    overflow: hidden;
    border: var(--lv-border-default, 1px solid var(--lv-line-muted));
    border-radius: var(--lv-radius-panel, var(--borderRadius-default));
    background: var(--lv-bg-panel);
    box-shadow: var(--shadow-floating-large, 0 12px 36px rgb(0 0 0 / 24%));
  }

  lv-asset-lineage-graph .asset-lineage-dialog[open] .asset-lineage-layout {
    grid-template-rows: auto auto minmax(0, 1fr);
    background: var(--lv-bg-panel);
  }

  lv-asset-lineage-graph .asset-lineage-dialog-title {
    margin: 0;
    padding: var(--base-size-12) var(--base-size-16) var(--base-size-4);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-semibold);
    overflow-wrap: anywhere;
  }

  lv-asset-lineage-graph .asset-lineage-root,
  lv-asset-lineage-graph .asset-lineage-layout {
    height: 100%;
    min-height: 0;
    min-width: 0;
  }

  lv-asset-lineage-graph .asset-lineage-layout {
    display: grid;
    grid-template-rows: auto minmax(0, 1fr);
    outline: 0;
  }

  lv-asset-lineage-graph .asset-lineage-actions {
    display: flex;
    align-items: center;
    gap: var(--base-size-8);
    padding: var(--base-size-4) var(--base-size-8);
  }

  lv-asset-lineage-graph .asset-lineage-actions button {
    min-height: var(--control-medium-size, 2rem);
    border: var(--lv-border-muted, 1px solid var(--lv-line-muted));
    border-radius: var(--lv-radius-default, var(--borderRadius-default));
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    padding: 0 var(--base-size-12);
    font: var(--lv-type-body-compact);
    cursor: pointer;
  }

  lv-asset-lineage-graph .asset-lineage-actions button:focus-visible {
    outline: var(--focus-outline, 2px solid var(--lv-line-accent));
    outline-offset: var(--focus-outline-offset, 2px);
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

  lv-asset-lineage-graph .react-flow__handle-top {
    top: 0;
    left: 50%;
    transform: translate(-50%, -50%);
  }

  lv-asset-lineage-graph .react-flow__handle-bottom {
    bottom: 0;
    left: 50%;
    transform: translate(-50%, 50%);
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
    opacity: 1;
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
    margin-top: var(--base-size-4);
    color: var(--lv-fg-default);
    overflow-wrap: anywhere;
    white-space: normal;
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
    margin-top: var(--base-size-6);
    color: var(--lv-fg-muted);
    overflow-wrap: anywhere;
    white-space: normal;
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

function toFlowNode(
  node: LineageNode,
  layout: LineageLayout,
  pathState: LineagePathState,
  onSelect: (id: string) => void,
  position?: { x: number; y: number },
  verticalFocus = false,
): Node<LineageNodeData> {
  const { x, y } = position ?? positionFor(node, layout)
  return {
    id: node.id,
    type: 'lineageNode',
    position: { x, y },
    sourcePosition: verticalFocus ? Position.Bottom : Position.Right,
    targetPosition: verticalFocus ? Position.Top : Position.Left,
    className: `asset-lineage-flow-node asset-lineage-flow-node-${nodePathState(node.id, pathState)}`,
    data: {
      ...node,
      selected: node.id === pathState.selectedID,
      pathState: nodePathState(node.id, pathState),
      onSelect,
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

function createFocusedPositions(graph: LineageGraph, layout: LineageLayout, selectedID: string, narrow: boolean): Map<string, { x: number; y: number }> {
  const positions = new Map<string, { x: number; y: number }>()
  const nodeIDs = new Set(graph.nodes.map((node) => node.id))
  const upstream = [...new Set(graph.edges.filter((edge) => edge.target === selectedID).map((edge) => edge.source))]
    .filter((id) => nodeIDs.has(id))
  const downstream = [...new Set(graph.edges.filter((edge) => edge.source === selectedID).map((edge) => edge.target))]
    .filter((id) => nodeIDs.has(id))
  const focus = new Set([selectedID, ...upstream, ...downstream])
  if (narrow) {
    const x = NODE_OFFSET_X
    upstream.forEach((id, index) => positions.set(id, { x, y: NODE_MIN_Y + index * NARROW_FOCUS_GAP_Y }))
    const selectedY = NODE_MIN_Y + upstream.length * NARROW_FOCUS_GAP_Y
    positions.set(selectedID, { x, y: selectedY })
    downstream.forEach((id, index) => positions.set(id, { x, y: selectedY + (index + 1) * NARROW_FOCUS_GAP_Y }))
  } else {
    const selectedY = NODE_MIN_Y + Math.max(0, upstream.length - 1) * NARROW_FOCUS_GAP_Y / 2
    upstream.forEach((id, index) => positions.set(id, { x: NODE_OFFSET_X, y: NODE_MIN_Y + index * NARROW_FOCUS_GAP_Y }))
    positions.set(selectedID, { x: NODE_OFFSET_X + NODE_GAP_X, y: selectedY })
    downstream.forEach((id, index) => positions.set(id, {
      x: NODE_OFFSET_X + NODE_GAP_X * 2,
      y: selectedY + (index - (downstream.length - 1) / 2) * NARROW_FOCUS_GAP_Y,
    }))
  }
  const offscreenX = NODE_OFFSET_X + Math.max(layout.rankIndex.size + 2, 4) * NODE_GAP_X
  for (const node of graph.nodes) {
    if (focus.has(node.id)) continue
    const position = positionFor(node, layout)
    positions.set(node.id, { x: position.x + offscreenX, y: position.y })
  }
  return positions
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

function nodePathState(id: string, pathState: LineagePathState): 'neutral' | 'selected' | 'upstream' | 'downstream' | 'unrelated' {
  if (!pathState.selectedID) return 'neutral'
  if (id === pathState.selectedID) return 'selected'
  if (pathState.upstream.has(id)) return 'upstream'
  if (pathState.downstream.has(id)) return 'downstream'
  return 'unrelated'
}

function createLineageLayout(nodes: LineageNode[]): LineageLayout {
  const ranks = Array.from(new Set(nodes.map(nodeRank))).sort((left, right) => left - right)
  const rankIndex = new Map(ranks.map((rank, index) => [rank, index]))
  const nodeIndex = new Map<string, number>()
  const rankNodeCount = new Map<number, number>()
  let maxNodeCount = 0

  for (const rank of ranks) {
    const rankNodes = nodes
      .filter((candidate) => nodeRank(candidate) === rank)
      .sort((left, right) => nodeSortKey(left).localeCompare(nodeSortKey(right)))
    rankNodeCount.set(rank, rankNodes.length)
    maxNodeCount = Math.max(maxNodeCount, rankNodes.length)
    rankNodes.forEach((candidate, index) => {
      if (!nodeIndex.has(candidate.id)) nodeIndex.set(candidate.id, index)
    })
  }

  return {
    rankIndex,
    nodeIndex,
    rankNodeCount,
    maxNodeCount,
    nodeGapY: maxNodeCount >= 8 ? DENSE_NODE_GAP_Y : NODE_GAP_Y,
  }
}

function positionFor(node: LineageNode, layout: LineageLayout): { x: number; y: number } {
  const rank = nodeRank(node)
  const rankIndex = layout.rankIndex.get(rank) ?? 0
  const index = layout.nodeIndex.get(node.id) ?? 0
  const rankNodeCount = layout.rankNodeCount.get(rank) ?? 1
  const rankOffsetY = Math.max(0, layout.maxNodeCount - rankNodeCount) * layout.nodeGapY / 2
  return {
    x: NODE_OFFSET_X + rankIndex * NODE_GAP_X,
    y: NODE_MIN_Y + rankOffsetY + index * layout.nodeGapY,
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
  return 'var(--lv-line-muted)'
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
