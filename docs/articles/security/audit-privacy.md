# Audit privacy and retention

Auditability and privacy are complementary requirements. LeapView records enough
context to explain a security-sensitive or administrative action, but an audit
record is not a copy of the data that the action touched. Treat audit events,
query events, application logs, identity-provider logs, exports, and source data
as separate information sets with separate owners and retention decisions.

## Minimize sensitive fields

Record the stable identifiers and outcome needed to answer who did what, to
which resource, when, and under which request or deployment context. Prefer
bounded reason codes and references to a ticket or incident over free-form
descriptions that repeat customer data.

Do not put secrets in an event, query annotation, reason, or export. In
particular, audit records must not contain passwords, bearer tokens, raw OIDC
tokens, provider credential values, or service-principal secret material.
Credential-rotation events can identify the binding, target, actor, operation,
timestamp, outcome, and a bounded reason code without recording the value being
rotated.

Names, resource paths, query text, target metadata, and principal identifiers
can still be personal or commercially sensitive. Limit those fields to the
smallest useful scope, avoid selecting row data for an audit export, and grant
audit and query-history access only to roles that need it. If a downstream
system needs a longer-lived record, consider a reviewed projection or
pseudonymous identifier rather than forwarding the entire platform database.

## Keep retention categories distinct

The `admin maintenance` command applies independent, policy-driven windows to
operational categories:

| Category | What it answers | Lifecycle owner |
| --- | --- | --- |
| Audit events | Which principal or service performed an administrative/security action? | Security and operations |
| Query events | Which governed analytical operation ran, with what status and context? | Data platform and operations |
| Archived agent conversations | What was retained from an explicitly archived agent run? | Product owner and privacy/security |
| Authentication state | Which expired or revoked sessions, OAuth states, API tokens, and service-principal secrets can be removed? | Identity/platform operations |

These windows do not define retention for project history, managed-data
revisions, source records, external identity-provider logs, application logs,
incident evidence, or legal holds. Those stores have their own policy and
backup controls. A value of zero disables pruning for that category; it is not
an assertion that unlimited retention is necessary or that a compliance
requirement has been met. Run maintenance as a dry run, review the result and
any preservation requirement, and only then apply deletion.

## Protection at rest is not encryption

LeapView protects local fixture state with private filesystem boundaries: files
are created with mode `0600`, private directories with mode `0700`, and SQLite
WAL/SHM sidecars are tightened when present. Production PostgreSQL and
object-store protection is owned by the corresponding provider. Filesystem
permissions reduce accidental access by other local users; they do not encrypt
the bytes. A host administrator, a compromised process with equivalent
privileges, an unprotected snapshot, or a copied archive can still read them.

Use disk or volume encryption, encrypted object-storage or backup destinations,
and managed key custody when your threat model or policy requires encryption at
rest. Keep recovery keys and secret-manager procedures separate from the data
they unlock, and test that an isolated restore can obtain the intended key
without broadening application access.

## Backups, exports, and operator duties

Production backup and recovery use PostgreSQL-native backup/PITR together with
the native protection mechanisms for the DuckLake catalog, Parquet files, and
managed-data objects. LeapView does not provide a local SQLite/file archive
that substitutes for a PostgreSQL target recovery point. Development and
evaluation fixtures may use their own SQLite harness; external source systems
and S3-backed objects remain under native backup, versioning, and retention
controls. A complete production procedure belongs in a separate native
runbook and ADR.

Operators are responsible for the destination and the policy around it:

1. Write backups and audit exports to a private destination, encrypt them
   before off-host storage when required, and restrict restore/export access to
   named operators.
2. Record a checksum, creation time, software/version context, and the
   corresponding external-store recovery points. Protect the checksum and
   manifest from unauthorized replacement.
3. Apply a lifecycle that preserves required incident or legal-hold evidence
   without retaining routine copies indefinitely. Destroy expired copies and
   their temporary staging files through the approved disposal process.
4. Rehearse an isolated restore and review who can read the resulting database
   and archive. A restore creates another copy subject to the same privacy and
   retention rules.

Before exporting, define the question the export must answer, select only the
fields and time range needed, redact or pseudonymize where possible, and record
the recipient and purpose. Treat an export as a new controlled copy: it does
not inherit protection merely because the source database was private.

## NIST and GDPR principle crosswalk

The table is a conversation starter for control owners, not a certification,
legal opinion, or statement that a deployment conforms to NIST or GDPR. Map
the actual organization, jurisdiction, contracts, and evidence before making a
compliance claim.

| LeapView practice | NIST SP 800-53 Rev. 5 themes | GDPR principles or safeguards to discuss |
| --- | --- | --- |
| Define event classes, required fields, and outcomes; omit secrets | AU-2 Event Logging, AU-3 Content of Audit Records | Purpose limitation, data minimization, and accountability (Art. 5(1)(b), (c), (f), (2)) |
| Restrict audit/query visibility and review access | AU-6 Audit Record Review, Analysis, and Reporting; AC least privilege | Integrity and confidentiality; access governance (Art. 5(1)(f), Art. 32) |
| Keep separate, justified windows and honor holds | AU-4 Audit Record Storage Capacity; AU-11 Audit Record Retention | Storage limitation and documented retention criteria (Art. 5(1)(e)) |
| Protect database, sidecars, archives, and exports; preserve integrity | AU-9 Protection of Audit Information; SC-28 Protection of Information at Rest | Integrity/confidentiality, resilience, and restoration testing (Art. 5(1)(f), Art. 32) |
| Correlate actor, time, request, and system context; monitor and investigate | AU-8 Time Stamps; AU-12 Audit Record Generation; AU-6 | Accountability and breach-detection support (Art. 5(2), Art. 33–34 where applicable) |

Review the [audit events guide](/docs/security/audit), [backup and restore
guide](/docs/guides/operate/backup-restore), and the generated maintenance
reference for operational commands. For legal basis, data-subject rights,
cross-border transfer, and sector-specific rules, involve the organization's
privacy and legal owners.
