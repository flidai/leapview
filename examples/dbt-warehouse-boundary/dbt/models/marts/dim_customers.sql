select
  c.customer_id,
  c.customer_name,
  c.region,
  cast(count(o.order_id) as bigint) as order_count,
  cast(coalesce(sum(o.revenue), 0) as double) as lifetime_value
from {{ ref('stg_customers') }} c
left join {{ ref('fct_orders') }} o using (customer_id)
group by c.customer_id, c.customer_name, c.region
