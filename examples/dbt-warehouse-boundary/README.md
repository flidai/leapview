# dbt warehouse-boundary showcase

This example is maintained by the repository's
[dbt warehouse-boundary guide](../../docs/articles/integrate/dbt-warehouse-boundary.md).
Run it from the repository root with:

```sh
task dbt:warehouse
```

The dbt project owns the transformations and writes the two selected marts to
`published/` with dbt-duckdb's `external` materialization. LeapView consumes
those physical Parquet files through its ordinary managed Connection, Sources,
thin Models, SemanticModel, and Dashboard. Neither `target/`, `manifest.json`,
nor `run_results.json` is part of the LeapView serving contract.
