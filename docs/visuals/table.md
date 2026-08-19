# Table

Use a table when readers need exact record-level values, sorting, and a virtualized window over a governed result set.

{{< visual id="orders_table" >}}

```yaml visual-example=orders_table
visuals:
  orders_table:
    type: table
    title: Orders
    query:
      type: records
      dataset: orders
      fields:
      - order_id
      - status
      - revenue
    presentation:
      type: table
      rowHeight: 32
      showHeader: true
      striped: false
```

## Conditional formatting

Bind a closed field or numeric rule to compiled result names. Color-driven
categorical outcomes include a redundant icon cue so meaning is not conveyed by
color alone.

{{< visual id="orders_table_conditional" >}}

```yaml visual-example=orders_table_conditional
visuals:
  orders_table_conditional:
    type: table
    title: Orders with governed formatting
    query:
      type: records
      dataset: orders
      fields:
      - order_id
      - status
      - revenue
    presentation:
      type: table
      rowHeight: 32
      showHeader: true
      striped: true
      conditionalFormatting:
      - id: status-color
        target: cell_foreground
        field: status
        rule:
          kind: field
          source: status
          values:
            delivered: {color: success, icon: circle}
            shipped: {color: accent, icon: arrow_up}
            canceled: {color: danger, icon: warning}
          nullStyle: {color: neutral}
          defaultStyle: {color: ink, icon: square}
      - id: revenue-gradient
        target: cell_background
        field: revenue
        rule:
          kind: gradient
          minimum: 0
          maximum: 500
          low: {color: neutral}
          high: {color: accent}
          nullStyle: {color: neutral}
```
