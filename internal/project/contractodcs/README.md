# ODCS 3.1 export adapter

This package exports immutable `leapview.contract/v1` Source and Model
publications as ODCS `v3.1.0` JSON. It consumes only FAI-622 publication
evidence and strictly decodes its FAI-620 canonical projection bytes into the
generated projection DTOs. Internal compiler, graph, runtime, artifact, and
release models are not adapter inputs.

Admission reuses the publication authority to verify canonical bytes, digest,
identity, version baseline, and retained validation/policy evidence, and requires
a publication timestamp. Invalid evidence produces no document. The caller still
owns authorized publication selection; this adapter does not query the ledger.

The upstream schema is pinned to Bitol's `v3.1.0` tag at commit
`b9d3ffc5aabe9e058afe4469cabe5a218fe9946d`. Its byte checksum is
`2cb7dd6fe43344d2233e0406438622681dc3ebadcf8f0d606a15b40c8f6752c0`.
Every export passes the embedded schema and the adapter's stricter extension
validator.

`mapping.json` is the reviewed mapping authority. An export reports only the
entries it applied, plus a deterministic loss report. Unsupported mappings
return the loss report and no partial ODCS document. Passing schema validation
proves the **document/export** level only; this profile does not claim import,
round-trip, or execution conformance.

## Extension governance

The sole extension is root `customProperties[property=leapviewContract]` with
namespace `leapview.dev/odcs-extension/v1`. LeapView's contract interoperability
maintainers own it. Its closed value contains only the resource kind,
`leapview.contract/v1` profile, and existing publication digest. It cannot carry
SQL, credentials, connection details, target bindings, access conditions, or
quality implementations. Unknown members and namespace versions are rejected.
Any shape change requires a new namespace and compatibility review.

SemanticModel remains an Apache Ossie interchange concern. Opaque datatypes or
quality rules without a safe ODCS representation reject export. Executable SQL,
physical bindings, and other intentionally excluded information are never
serialized; the loss report records the dropped semantic category without
copying sensitive values.

The independent `datacontract-cli` oracle is pinned to 1.1.3 and run only by GitHub CI through
`task odcs:oracle`. It is not a Go dependency and is not linked into LeapView
production binaries.

Relationship shorthand resolves only within the single exported object's
properties. Missing or cross-contract targets reject export with an unsupported
loss report and no partial document. The adapter cannot manufacture an external
contract reference from a resource name without that contract's authority.

The former Rust CLI 0.9.1's stable-ID character rule and object-only SLA lookup
rejected the profile's IDs and column-level SLA references, although ODCS permits
those values. The replacement runs document lint against the same checksum-checked
vendored schema, with hash-locked dependencies and no connection extras. Its
environment is cleared, dotenv/config loading is disabled, and external
definition inlining is disabled. Go validation retains ownership of sealed
LeapView extensions and safe relationship mapping. See the
[conformance evidence](../../../adr/specifications/odcs-export-conformance.md#hosted-oracle-investigation)
for the distinction between exporter defects and upstream-tool limitations.
