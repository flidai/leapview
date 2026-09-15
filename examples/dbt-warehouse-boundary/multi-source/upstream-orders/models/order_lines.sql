select
  cast(order_id as varchar) as order_id,
  cast(customer_id as varchar) as customer_id,
  cast(order_date as date) as order_date,
  lower(cast(status as varchar)) as order_status,
  cast(line_amount as double) as line_amount
from {{ ref('upstream_orders', 'raw_orders') }}
