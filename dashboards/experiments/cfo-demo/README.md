# CFO demo

This project is a decision-oriented finance demonstration for a fictional
company, Northstar Consumer Products.

The actual sales, discount, COGS, and profit inputs come from Microsoft's
public Financial Sample workbook. `task bootstrap:cfo` downloads the exact
pinned workbook from Microsoft's official download endpoint and verifies its
SHA-256 digest before LeapView sees it.

All dates are shifted twelve years so the original 2013–2014 sample appears as
2025–2026. Budget, forecast, operating-expense, EBITDA, variance-driver, and
13-week cash values are deterministic fictional extensions defined in the
Models. They are not Microsoft data and must be described as demo scenarios.

## Model shape

The semantic model is a finance fact constellation. Financial performance,
P&L statement rows, EBITDA variance contributions, and weekly cash forecasts
are separate facts because they have different grains. They share conformed
date, segment, country, and product dimensions where those concepts apply.
Discount band, P&L line, variance driver, forecast week, and cash scenario are
role-specific dimensions.

The small, stable reference sets use their public source values as natural
keys. Each fact declares those keys as foreign entities and every semantic
relationship points to a primary dimension entity. This keeps join cardinality
explicit without inventing warehouse-generated surrogate IDs for demo data.

Run the project with:

```sh
task dev:cfo
```

The demo is delivered as one **CFO Command Center** dashboard so report-level
filters and navigation remain in one governed context. Its four pages are:

1. **Executive Overview** — compare revenue, margin, and EBITDA with plan.
2. **P&L and Budget** — inspect the financial statement and EBITDA bridge.
3. **Cash and Working Capital** — compare base, upside, and downside cash paths.
4. **Profitability Drivers** — identify the products and markets driving margin.

The decision charts declare explicit selection interactions. Selecting a month,
segment, product, country, discount band, or cash scenario cross-filters only
the compatible visuals on that page; selecting the same value again clears it.

Source: <https://learn.microsoft.com/power-bi/create-reports/sample-financial-download>
