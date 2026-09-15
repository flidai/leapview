-- Independently published CRM snapshot. It does not read the commerce mart,
-- the dbt repository, or any LeapView catalog. Aggregates are producer-owned.
select customer_id::varchar as customer_id, customer_name::varchar as customer_name,
       region::varchar as region, order_count::bigint as order_count,
       lifetime_value::double as lifetime_value
from (values
  ('c-1', 'Ada', 'north', 2, 52.25),
  ('c-2', 'Grace', 'south', 1, 75.00),
  ('c-3', 'Linus', 'north', 1, 9.99)
) as directory(customer_id, customer_name, region, order_count, lifetime_value)
