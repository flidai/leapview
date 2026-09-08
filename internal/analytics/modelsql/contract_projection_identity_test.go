package modelsql

import "testing"

func TestContractSQLProjectionPreservesMeaningAndIgnoresFormatting(t *testing.T) {
	base, err := CanonicalProjection(`SELECT o.id, o.amount + 1 AS adjusted FROM source.orders o WHERE o.amount > 10`)
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := CanonicalProjection("-- descriptive comment\nSELECT o.id, (o.amount + 1) AS adjusted\nFROM source.orders AS o\nWHERE (o.amount > 10)")
	if err != nil {
		t.Fatal(err)
	}
	if base == nil || formatted == nil || *base != *formatted {
		t.Fatal("formatting changed the canonical SQL projection")
	}
	for name, sql := range map[string]string{
		"literal":          `SELECT o.id, o.amount + 2 AS adjusted FROM source.orders o WHERE o.amount > 10`,
		"predicate":        `SELECT o.id, o.amount + 1 AS adjusted FROM source.orders o WHERE o.amount >= 10`,
		"projection order": `SELECT o.amount + 1 AS adjusted, o.id FROM source.orders o WHERE o.amount > 10`,
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := CanonicalProjection(sql)
			if err != nil {
				t.Fatal(err)
			}
			if changed == nil || *changed == *base {
				t.Fatal("result-affecting SQL change disappeared from contract identity")
			}
		})
	}
}

func TestContractSQLProjectionRejectsUnadmittedExecution(t *testing.T) {
	for _, sql := range []string{
		`DELETE FROM source.orders`,
		`SELECT * FROM read_csv('/private/input.csv')`,
		`SELECT * FROM source.orders; SELECT * FROM source.orders`,
	} {
		if projection, err := CanonicalProjection(sql); err == nil || projection != nil {
			t.Fatalf("unadmitted SQL returned a projection: error=%v", err)
		}
	}
}
