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
      dataset: market_ohlc
      dimensions:
        month: market_ohlc.month
      metrics:
        market_open: null
        market_close: null
        market_low: null
        market_high: null
      sort:
        - field: month
          direction: asc
      limit: 12
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
      dimensions:
        purchase_month: orders.purchase_month
      metrics:
        revenue_q1: null
        revenue_q3: null
        revenue_min: null
        revenue_max: null
      sort:
        - field: purchase_month
          direction: asc
      limit: 30
```
