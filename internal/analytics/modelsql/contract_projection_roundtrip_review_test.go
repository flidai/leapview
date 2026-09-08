package modelsql

import "testing"

func TestCanonicalProjectionRoundTripsThroughValidator(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{
			name: "limit and offset",
			sql:  `SELECT id FROM source.orders LIMIT 5 OFFSET 2`,
		},
		{
			name: "rank and lag windows",
			sql: `SELECT
  rank() OVER (PARTITION BY customer_id ORDER BY order_id ROWS BETWEEN 2 PRECEDING AND CURRENT ROW) AS rank_value,
  lag(amount, 1, 0) OVER (ORDER BY order_id) AS previous_amount
FROM source.orders`,
		},
		{
			name: "typed casts and nested type values",
			sql:  `SELECT CAST(amount AS DECIMAL(38, 2)) AS exact_amount, TRY_CAST(amount AS INTEGER) AS integer_amount FROM source.orders`,
		},
		{
			name: "union and order",
			sql:  `SELECT id FROM source.orders UNION ALL SELECT id FROM source.payments ORDER BY id LIMIT 10`,
		},
		{
			name: "CTE over values",
			sql:  `WITH input AS (VALUES (1, 'one'), (2, 'two')) SELECT * FROM input`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection, err := CanonicalProjection(test.sql)
			if err != nil {
				t.Fatalf("CanonicalProjection() error = %v", err)
			}
			if projection == nil || *projection == "" {
				t.Fatal("CanonicalProjection() returned an empty projection")
			}
			if err := ValidateCanonicalProjection(*projection); err != nil {
				t.Fatalf("ValidateCanonicalProjection() error = %v\nprojection = %s", err, *projection)
			}
		})
	}
}
