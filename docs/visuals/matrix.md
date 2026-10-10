# Matrix

Use a matrix for grouped rows and metrics, optionally split across a column dimension.

{{< visual id="status_matrix" >}}

```yaml visual-example=status_matrix
visuals:
  - id: status_matrix
    type: matrix
    title: Orders by category and status
    query:
      type: pivot
      rows:
      - category
      columns:
      - status
      metrics:
      - order_count
      - revenue
    presentation:
      type: table
      rowHeight: 32
      showHeader: true
      striped: false
```

Conditional-format targets may name visible row fields or metric aliases.
Column dimensions are used to generate the cross-tab headers and are not
visible target columns.

## Expandable row hierarchies

Add `presentation.hierarchy` to a matrix or pivot. Disclosure buttons expand
individual branches; **Expand all** and **Collapse all** change the current
view. Hierarchies have no fixed level limit and use the existing virtualized
table. The governed result still has its normal row budget.

For dimension levels, put the fields in their display order:

```yaml
query:
  type: pivot
  rows: [division, plant, production_line]
  columns: [month]
  metrics: [output_units]
presentation:
  type: table
  rowHeight: 32
  showHeader: true
  striped: false
  hierarchy:
    mode: levels
    fields: [division, plant, production_line]
    label: Manufacturing
    defaultExpandedDepth: 2
```

Dimension groups display headings with blank metric cells. They do not compute
subtotals by adding already aggregated values.

For a chart of accounts, deliver each account's ID, parent ID, and label as row
dimensions, then use their result aliases:

```yaml
query:
  type: pivot
  rows: [account_id, parent_account_id, account_name]
  columns: [period]
  metrics: [balance]
presentation:
  type: table
  rowHeight: 32
  showHeader: true
  striped: false
  hierarchy:
    mode: parent_child
    idField: account_id
    parentField: parent_account_id
    labelField: account_name
    defaultExpandedDepth: 2
```

Disable `query.totals.columns` and `query.totals.grand` for parent-child and
nested hierarchies: their appended total rows have no tree identity. Row-total
columns remain available.

Parent balances must be supplied by the governed query. The renderer preserves
those values and never calculates them from children. IDs must be unique;
cycles produce an error. A null parent identifies a root. Accounts whose
parents are absent from a filtered result appear as roots.

Nested children use `mode: nested`, `childrenField`, `labelField`, and an
optional `idField`. Child nodes repeat the same label, ID, metric, and children
keys as their parent. For cross-tabs, child metric keys must be the final grid
field IDs, including generated pivot cell IDs, rather than metric aliases;
children are already shaped rows and are not independently queried or pivoted.
For governed query transport, expose the children as a
string field containing a JSON array (for example, serialize the source's
DuckDB list of structs in a model). Include that field and the root label/ID
in `query.rows`. Direct component payloads also accept native child arrays.
The tree is traversed iteratively, so its depth is not tied to JavaScript's
call-stack limit. Nested descendants count toward the source-row budget.

```yaml
hierarchy:
  mode: nested
  childrenField: children_json
  labelField: assembly_name
  idField: assembly_id
  defaultExpandedDepth: 2
```

On a fresh checkout, generate the normal browser contracts first with
`task ui-signals:generate dashboard-contracts:generate layout-contract:generate lucide-icons:generate`.
Run `bun run build:css` once if `static/app.css` is absent. Before/after links
are available only when the matching local frozen baseline bundle exists.

Run `bun scripts/matrix_hierarchy_playground.ts` for an interactive local
preview with chart-of-accounts, nested manufacturing, dimension-level, and
deep-hierarchy examples. The playground uses illustrative data and the
production table component; its JSON editor lets you try your own inputs.
