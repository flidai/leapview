# Apache Ossie adapter

This package is pinned to Apache Ossie core specification `0.2.0.dev0`,
inspected from `apache/ossie` commit
`88e0011148283302c9a04cd0287e00e0b9d87354`. `schema/osi-schema.json` is the
official schema at that commit and is used for every import and export.
The schema's Apache attribution is retained in `schema/NOTICE`; its pinned
SHA-256 is `8ce9f82aa92080265f9ae119e31cda5bef062f489674d3c467245c2d4c5ff264`.

Ossie datasets are lookup-only references: `Import` requires each `source` to
be present in the caller's project Model map. The adapter never turns a source
string into a connection, source, transform, or materialization.

Ossie's core document does not have native fields for governed filters,
fact-relative dimension bindings and paths, time grain allowlists, empty-value
policies, typed derived/ratio metrics, units/formats, or LeapView relationship
entity endpoints. `Export` retains those result-affecting semantics in one
structured, versioned `LEAPVIEW` custom extension. Import rejects unknown
extension versions and unsupported core executable expressions rather than
silently weakening a model.
