// Package strictjson provides bounded, ambiguity-free JSON decoding.
//
// The package owns generic decoding mechanics only. It does not define a
// schema, authorize fields, canonicalize application values, or turn JSON
// metadata into an allowlist. Callers remain responsible for validating the
// decoded value against their domain contract.
package strictjson
