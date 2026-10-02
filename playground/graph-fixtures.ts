import type {
  AssetLineageEdgeSignal,
  AssetLineageNodeSignal,
  SemanticModelGraphFieldSignal,
  SemanticModelGraphNodeSignal,
  SemanticModelGraphSignal,
} from '../web/generated/signals'

// The graph component accepts dependency edge kinds and run overlays in addition
// to the narrower dashboard lineage signal. Keep the adaptation at this boundary.
export type LineageFixture = {
  nodes: Array<Omit<AssetLineageNodeSignal, 'side'> & {
    side: 'upstream' | 'selected' | 'downstream'
    runStatus?: string
    runStatusLabel?: string
    runAnimate?: boolean
  }>
  edges: Array<Omit<AssetLineageEdgeSignal, 'kind'> & { kind: string }>
}

export type LineageScenario = 'dependencies' | 'run' | 'wide' | 'empty'
export type SemanticScenario = 'sales' | 'composite' | 'disconnected' | 'empty'

export function lineageFixture(scenario: LineageScenario, animate = false): LineageFixture {
  if (scenario === 'empty') return { nodes: [], edges: [] }
  const nodes: LineageFixture['nodes'] = [
    { id: 'warehouse', label: 'Commerce warehouse', kind: 'connection', meta: 'PostgreSQL', rank: -2, side: 'upstream' },
    { id: 'orders-source', label: 'Orders', kind: 'source', meta: 'raw.orders', rank: -1, side: 'upstream' },
    { id: 'customers-source', label: 'Customers', kind: 'source', meta: 'raw.customers', rank: -1, side: 'upstream' },
    { id: 'sales-model', label: 'Sales facts', kind: 'model', meta: 'One row per order', rank: 0, side: 'selected', selected: true },
    { id: 'sales-semantic', label: 'Sales analytics', kind: 'semantic_model', meta: 'Revenue and customer metrics', rank: 1, side: 'downstream' },
    { id: 'revenue-dashboard', label: 'Revenue overview', kind: 'dashboard', meta: 'Executive reporting', rank: 2, side: 'downstream' },
  ]
  const edges: LineageFixture['edges'] = [
    { id: 'warehouse-orders', source: 'warehouse', target: 'orders-source', kind: 'lineage_connection_source' },
    { id: 'warehouse-customers', source: 'warehouse', target: 'customers-source', kind: 'lineage_connection_source' },
    { id: 'orders-sales', source: 'orders-source', target: 'sales-model', kind: 'uses_source' },
    { id: 'customers-sales', source: 'customers-source', target: 'sales-model', kind: 'uses_source' },
    { id: 'sales-semantic', source: 'sales-model', target: 'sales-semantic', kind: 'lineage_model_semantic_model' },
    { id: 'semantic-dashboard', source: 'sales-semantic', target: 'revenue-dashboard', kind: 'lineage_semantic_model_dashboard' },
  ]
  if (scenario === 'wide') {
    for (const [index, region] of ['Europe', 'Americas', 'Asia Pacific', 'Middle East', 'Africa', 'Oceania'].entries()) {
      const id = `regional-model-${index}`
      nodes.push({ id, label: `${region} sales`, kind: 'model', meta: `Regional slice ${index + 1}`, rank: 0, side: 'upstream' })
      edges.push({ id: `orders-${id}`, source: 'orders-source', target: id, kind: 'uses_source' })
      edges.push({ id: `${id}-semantic`, source: id, target: 'sales-semantic', kind: 'uses_model' })
    }
    nodes.forEach((node) => { node.selected = node.id === 'sales-semantic'; node.side = node.rank < 1 ? 'upstream' : node.rank === 1 ? 'selected' : 'downstream' })
  }
  if (scenario === 'run') {
    const statuses: Record<string, [string, string]> = {
      warehouse: ['succeeded', 'Connected'],
      'orders-source': ['succeeded', 'Loaded · 24,580 rows'],
      'customers-source': ['failed', 'Connection timed out'],
      'sales-model': ['running', 'Building model'],
      'sales-semantic': ['queued', 'Waiting for dependencies'],
      'revenue-dashboard': ['skipped', 'Upstream unavailable'],
    }
    for (const node of nodes) {
      const [status, label] = statuses[node.id]
      node.runStatus = status
      node.runStatusLabel = label
      node.runAnimate = animate
    }
  }
  return { nodes, edges }
}

const field = (name: string, type: string, options: Partial<SemanticModelGraphFieldSignal> = {}): SemanticModelGraphFieldSignal => ({ name, type, ...options })

export function semanticFixture(scenario: SemanticScenario): SemanticModelGraphSignal {
  if (scenario === 'empty') return { datasets: [], nodes: [], edges: [] }
  const nodes: SemanticModelGraphNodeSignal[] = [
    {
      id: 'orders', title: 'Orders', grainEntity: 'order',
      entities: [{ name: 'order', type: 'primary', fields: ['order_id'], grain: true }],
      fields: [
        field('order_id', 'BIGINT', { grain: true, entities: ['order'] }),
        field('customer_id', 'BIGINT', { join: true, entities: ['customer'], relationships: ['orders-customers'] }),
        field('product_id', 'BIGINT', { join: true, relationships: ['orders-products'] }),
        field('ordered_at', 'TIMESTAMP'), field('revenue', 'DECIMAL(12,2)'), field('is_returned', 'BOOLEAN'),
      ],
    },
    {
      id: 'customers', title: 'Customers', grainEntity: 'customer',
      entities: [{ name: 'customer', type: 'primary', fields: ['customer_id'], grain: true }],
      fields: [field('customer_id', 'BIGINT', { grain: true, join: true, entities: ['customer'], relationships: ['orders-customers'] }), field('name', 'VARCHAR'), field('segment', 'VARCHAR'), field('country', 'VARCHAR')],
    },
    {
      id: 'products', title: 'Products', grainEntity: 'product',
      entities: [{ name: 'product', type: 'primary', fields: ['product_id'], grain: true }],
      fields: [field('product_id', 'BIGINT', { grain: true, join: true, relationships: ['orders-products'] }), field('product_name', 'VARCHAR'), field('category', 'VARCHAR'), field('unit_price', 'DECIMAL(12,2)')],
    },
  ]
  const graph: SemanticModelGraphSignal = {
    datasets: nodes.map((node) => node.id), nodes,
    edges: [
      { id: 'orders-customers', source: 'orders', target: 'customers', sourceField: 'customer_id', targetField: 'customer_id', cardinality: 'many_to_one', label: '*:1' },
      { id: 'orders-products', source: 'orders', target: 'products', sourceField: 'product_id', targetField: 'product_id', cardinality: 'many_to_one', label: '*:1' },
    ],
  }
  if (scenario === 'composite') {
    graph.nodes = [
      { id: 'inventory', title: 'Inventory', grainEntity: 'stock_item', fields: [field('warehouse_id', 'BIGINT', { grain: true, join: true }), field('product_id', 'BIGINT', { grain: true, join: true }), field('quantity', 'INTEGER'), field('updated_at', 'TIMESTAMP')] },
      { id: 'availability', title: 'Availability', grainEntity: 'stock_item', fields: [field('warehouse_id', 'BIGINT', { grain: true, join: true }), field('product_id', 'BIGINT', { grain: true, join: true }), field('available_quantity', 'INTEGER'), field('in_stock', 'BOOLEAN')] },
    ]
    graph.edges = [{ id: 'inventory-availability', source: 'inventory', target: 'availability', sourceField: 'warehouse_id, product_id', targetField: 'warehouse_id, product_id', cardinality: 'one_to_one', label: '1:1 · composite key' }]
    graph.datasets = ['inventory', 'availability']
  }
  if (scenario === 'disconnected') {
    graph.nodes.push({ id: 'targets', title: 'Monthly targets', grainEntity: 'month', fields: [field('month', 'DATE', { grain: true }), field('target_revenue', 'DECIMAL(12,2)')] })
    graph.datasets?.push('targets')
  }
  return graph
}
