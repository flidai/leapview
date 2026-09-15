-- dbt resolves this package before publishing the consumer-owned physical mart.
select
  order_id,
  customer_id,
  order_date,
  order_status,
  cast(sum(line_amount) as double) as revenue
from {{ ref('upstream_orders', 'order_lines') }}
group by order_id, customer_id, order_date, order_status
