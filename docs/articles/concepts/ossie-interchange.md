# Apache Ossie interchange

LeapView supports the pinned Apache Ossie core specification
`0.2.0.dev0` as a semantic-model interchange boundary. The native
LeapView contract remains authoritative for execution; Ossie documents are
validated, imported, and exported through the project compiler.

Use the local CLI with a project that declares the referenced Model resources:

```sh
leapview semantic-model ossie export --project dashboards/leapview.yaml --semantic-model sales > sales.ossie.json
leapview semantic-model ossie import --project dashboards/leapview.yaml sales.ossie.json > sales.yaml
```

Import resolves each Ossie dataset `source` to an existing project Model. It
never creates a connection, source, transformation, or materialization from
that string. Imported documents run the same semantic graph validation as
native resources. Export retains LeapView-only executable semantics (such as
governed filters, conformed-dimension paths, and typed derived or ratio
metrics) in the versioned `LEAPVIEW` custom extension.

Unknown extension fields, contradictory metric tag fields, unsupported
executable expressions, and unresolved project Model references are errors.
In particular, `COUNT(field)` is preserved as a field count; it is never
silently rewritten as `COUNT(*)`.
