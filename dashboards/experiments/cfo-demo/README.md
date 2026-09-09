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
