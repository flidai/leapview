# Candlestick chart

Use a candlestick chart to compare open, close, low, and high metrics across an ordered category.

Every preview on this page is generated from the YAML shown below it using a fixed documentation dataset.

## Market OHLC

Use true open, close, low, and high metrics over an ordered month dimension. Each candle now represents the analytical contract directly rather than repurposing an unrelated distribution.

{{< visual id="market_candlestick" >}}

```yaml visual-example=market_candlestick
visuals:
  market_candlestick:
    title: Monthly market range
    description: Shows monthly open, close, low, and high values.
    type: candlestick
    query:
      type: aggregate
      dimensions:
      - month
      metrics:
      - market_open
      - market_close
      - market_low
      - market_high
      sort:
      - field: month
        direction: asc
      limit: 12
    presentation:
      type: cartesian
```

## Revenue range

Change the metric to revenue and enable `presentation.dataZoom` so dense monthly ranges remain explorable without changing the OHLC contract.

{{< visual id="revenue_candlestick" >}}

```yaml visual-example=revenue_candlestick
visuals:
  revenue_candlestick:
    title: Revenue OHLC by month
    type: candlestick
    presentation:
      type: cartesian
      dataZoom: true
    query:
      type: aggregate
      dimensions:
      - purchase_month
      metrics:
      - revenue_q1
      - revenue_q3
      - revenue_min
      - revenue_max
      sort:
      - field: purchase_month
        direction: asc
      limit: 30
```
