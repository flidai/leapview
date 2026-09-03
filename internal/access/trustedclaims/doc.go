// Package trustedclaims defines the narrow trust boundary between
// cryptographic authentication adapters and semantic-attribute admission.
//
// Callers may present raw evidence only to Verify, together with a verifier
// owned by the evidence source. Verify is the only operation that can produce
// an Envelope. The envelope intentionally exposes no raw evidence and keeps
// its claim values private; accessors return copies suitable for a downstream
// admission repository to canonicalize.
package trustedclaims
