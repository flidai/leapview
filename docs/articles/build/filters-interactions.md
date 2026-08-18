# Filters and interactions

Filters and selections let readers change a report without giving the browser unrestricted query control. Dashboard YAML declares semantic fields, legal operators, option sources, URL identity, and interaction targets; the server validates and applies every command.

## Define dashboard filters

### Define a categorical filter

```yaml
filters:
  - id: state
    label: State
    description: Limit results to one or more customer states.
    dimension: customer_state
    control:
      type: multiSelect
      maxSelectedValues: 50
      options:
        type: distinct
        dataset: orders
        limit: 50
    operators: [in, notIn]
    urlParameter: state
```

The definition owns semantic meaning, legal operators, selection limits, and the governed option source. Distinct options are loaded lazily in bounded pages. For thousands of identifiers, enable search in the client while keeping page sizes conservative.

Place the same filter on a page with a typed filter component:

```yaml
components:
  - id: state-filter
    type: filter
    filter: state
    placement: {column: 1, row: 1, columnSpan: 4, rowSpan: 2}
```

The Filters pane and a page component share the same canonical filter state. Removing either presentation does not remove the filter or its effect.

### Define date and relative-period predicates

```yaml
filters:
  - id: purchase-period
    label: Purchase period
    dimension: purchase_date
    control:
      type: dateRange
    operators: [greaterThanOrEqual, lessThanOrEqual]
    urlParameter: period
```

Date, timestamp, calendar, timezone, and week-start semantics come from the semantic field. Resolve those semantics in the model instead of compensating in each visual.

### Define a text filter

```yaml
filters:
  - id: category
    label: Category
    dimension: category
    control:
      type: text
    operators: [contains, equals, startsWith, endsWith, notContains]
    urlParameter: category
```

URL parameters carry canonical applied state. Invalid values are rejected before query planning; the browser does not maintain a second predicate parser.

## Control interaction scope

By default, a filter applies to every semantically compatible consumer in its scope. Set `targets` on the filter when it should affect only named page components:

```yaml
filters:
  - id: state
    label: State
    dimension: customer_state
    control: {type: multiSelect}
    targets: [revenue-trend, orders-table]
```

Targets are dashboard definition IDs, not CSS selectors or browser element IDs. Test combinations, not just filters in isolation: two individually valid filters can produce an intentional empty result.

## Declare a visual interaction matrix

Selection interactions map delivered row values back to semantic fields and declare targets:

```yaml
interactions:
  - type: selection
    mode: multiple
    toggle: true
    mappings:
      - field: status
        dataset: orders
        value: status
        label: status
    targets: [orders-table, category-revenue]
    highlightTargets: [status-breakdown]
    noneTargets: [explanatory-note]
```

`targets` cross-filter target queries. `highlightTargets` emphasize the compatible selected subset while retaining the comparison result. `noneTargets` document intentional non-interaction. `field` and `value` are delivered result names in the visual contract; dataset identity is optional when the result is unambiguous.

Table row selection and map spatial selection use the same target lists. Keep mapping values typed: numeric zero, boolean false, string `"0"`, and null are not interchangeable.

Cross-filter, cross-highlight, and applied filters are separate state roots. An interaction never silently rewrites canonical filter state, and stale commands are rejected before affected consumers are planned.

## Verify predictable interactions

- Make selection state visually apparent.
- Provide a clear way to toggle or clear it.
- Target only components whose change users can anticipate.
- Avoid cycles where selections continually redefine one another.
- Verify behavior when page filters and selections are both active.
- Ensure superseded interactions cannot restore an older result.

Start with standalone correct visuals, then add one interaction at a time. The generated [Dashboard configuration](/docs/config/dashboard) lists current filter and interaction fields.
