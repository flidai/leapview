# Deployment decision reconciliation — 9 October 2026

The project owner explicitly accepted the current application update design on
9 October 2026: briefly stop the old version, start and verify the new version,
then reopen service. This records the decision for D01 and its D11/D12 consumers.
It does not reinstate the removed proposal documents or change the reference-only
status of [#921](https://github.com/flidai/leapview/pull/921).

The implementation contract is bounded maintenance with a single active process
owning the application home. Preflight the exact admitted candidate, compatibility
and retained rollback inputs while the predecessor serves. Close admission, drain
provider work and workers, stop the predecessor and confirm exit and lock release.
Start the candidate privately and verify the exact generation, credentials and
runtime readiness before reopening service. A failed preflight leaves the current
release serving; an interrupted or uncertain handoff stays closed until the
retained operation is reconciled. No simultaneous application-process overlap is
required by this accepted choice.

Credential activation and retirement share provider admission, draining and
serialization with this lifecycle. A replacement image cannot claim readiness
while another process owns the home or while a credential operation needs
reconciliation. Logical credential versions and retained recovery keys remain
separate from encrypted-envelope rotation and upstream provider revocation.

An incompatible schema, catalog or credential format must be rejected before
ordinary update/rollback mutation. Its explicit maintenance and recovery procedure
must retain the corresponding database, files, configuration and usable key
material. Host rollback is not database recovery. NixOS/deploy-rs owns host changes,
PostgreSQL maintenance owns database changes, and each installation has exactly
one application deployment owner: the managed controller or public Compose.

This is owner acceptance of the update design, not a new claim of independent
ADR approval or production qualification. ADR-0027 and Anand's foundation scope
are unchanged. The concrete implementation remains subject to PR review and
normal merge checks. Qualification must still measure the interruption bound and
prove startup failure, runner loss, compatible offline rollback, current data and
credential recovery. Exact-profile adoption, backup/key custody, observation and
retirement of replaced operational paths require their own evidence.

Related implementation and evidence:

- [Managed maintenance controller and recovery](maintenance.md)
- [Customer credential lifecycle](../credentials.md)
- [D01 decision issue](https://linear.app/flid/issue/FAI-1018)
- [D11 lifecycle issue](https://linear.app/flid/issue/FAI-1028)
- [D12 credential completion](https://github.com/flidai/leapview/pull/964)
