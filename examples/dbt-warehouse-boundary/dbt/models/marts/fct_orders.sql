select
  order_id,
  customer_id,
  order_date,
  order_status,
  cast(sum(line_amount) as double) as revenue
from {{ ref('stg_orders') }}
group by order_id, customer_id, order_date, order_status
