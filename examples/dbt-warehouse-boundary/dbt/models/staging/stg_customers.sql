select
  cast(customer_id as varchar) as customer_id,
  trim(customer_name) as customer_name,
  lower(trim(region)) as region
from {{ ref('raw_customers') }}
