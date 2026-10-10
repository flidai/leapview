# Table

Tables use the height required by their returned rows, headers, and controls.
The footer follows the last row. Larger results scroll within the available
visual height; tables, matrices, and pivots resize as the result or expanded
hierarchy changes. Dashboard grid positions remain authored layout bounds.

## Images, image previews, and hyperlinks

Use `presentation.cellContent` to render string URL fields as images or links.
The keys name query result aliases. This works for tables and for visible row
fields in matrices and pivots.

In the dashboard editor, select the table and open **Format**. Under the
image column, choose **Display as → Inline image** or **Image on hover**, then
set **Image width (px)** and **Image height (px)**. Clear a dimension to use the
renderer default. Increase **Row height** when a thumbnail needs more room;
hover-preview dimensions apply independently of the row height. These are
formatting controls; filters choose which records appear.

For example, a manufacturing parts table can use 56 × 56 pixel thumbnails in 80 pixel rows,
280 × 220 pixel hover previews, and a datasheet hyperlink labeled from the
part-name field. Include each URL field and its label field in the query.
Choose **Text** to return a media column to its original URL text.

```yaml
query:
  type: records
  dataset: parts
  fields: [photo_url, part_name, parent_assembly, stock, unit_cost, datasheet_url, preview_url]
  limit: 100
presentation:
  type: table
  rowHeight: 80
  showHeader: true
  striped: false
  cellContent:
    photo_url:
      kind: image
      display: inline
      width: 56
      height: 56
      altField: part_name
    preview_url:
      kind: image
      display: tooltip
      width: 280
      height: 220
      altField: part_name
    datasheet_url:
      kind: link
      labelField: part_name
      newTab: true
```

Inline images fit within the fixed row height, keeping table virtualization
stable. Set `rowHeight` to make thumbnails larger. Image dimensions accept
1–512 pixels. Tooltip dimensions apply to the preview instead of the row;
previews open on hover, focus, or tap and close on Escape or scrolling.

Image URLs can be HTTPS addresses or same-origin paths. They are loaded by the
browser with no referrer and are not fetched through a server proxy. Missing,
blocked, and broken images show an accessible placeholder. Use direct image
URLs the reader's browser can access.

Hyperlinks support HTTP, HTTPS, and same-origin paths. Their label can come from
another delivered field; without `labelField`, the URL is shown. `newTab`
defaults to true. Links open independently of row selection. Copy and CSV
export retain the original URL values for both image and link columns.

Include label and alt fields in the returned table columns. If you do not want
to show those columns, hide them with the table's **Columns** menu; that keeps
their values available to the image/link cells.

For a hierarchy, put media in separate visible row fields, or on the label
field of a parent-child/nested hierarchy. Dimension-level fields are collapsed
into a single hierarchy column and cannot carry `cellContent` settings.

On a fresh checkout, generate the normal browser contracts first with
`task ui-signals:generate dashboard-contracts:generate layout-contract:generate lucide-icons:generate`.
Run `bun run build:css` once if `static/app.css` is absent. Before/after links
are available only when the matching local frozen baseline bundle exists.

Run `bun scripts/table_media_playground.ts` for a local preview with illustrative
part illustrations, example datasheets, sizing controls, and an editable JSON source.
Run `bun scripts/table_formatting_playground.ts` to adjust the same manufacturing example
in the production dashboard editor. Its local adapter uses the production
authoring reducer; it does not write to your dashboard database.
Its three-dot menu also exposes **Explore**, opening the production Data Explorer
component over the same local records. This example supports records queries,
field selection, sorting, limits, and filters through a fixture adapter. It does
not connect to a deployed semantic model or save explorations. Image and link
formatting stays on the table; Explorer displays the underlying URL values.

Use a table when readers need exact record-level values, sorting, and a virtualized window over a governed result set.

{{< visual id="orders_table" >}}

```yaml visual-example=orders_table
visuals:
  - id: orders_table
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
color alone. The target `field` must be one of the visible table columns; a
field-rule `source` may use any field carried by the delivered row.

{{< visual id="orders_table_conditional" >}}

```yaml visual-example=orders_table_conditional
visuals:
  - id: orders_table_conditional
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
