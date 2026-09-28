# ADR-0022: Adopt a managed-service compliance and assurance baseline

Status: accepted

Decision date: 2026-09-20

Implementation: pending; delivery tracked in Linear

Deciders: LeapView maintainers

Supersedes: none

Amends: none

Amended by: [ADR-0023](0023-qualify-managed-service-legal-and-customer-assurance-scope.md),
legal coverage, customer qualification and assurance deliverables

Related: [ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md),
[ADR-0020](0020-adopt-a-postgresql-centered-target-data-architecture.md),
[managed compliance project](https://linear.app/flid/project/managed-leapview-compliance-readiness-f209990e91f7),
[control map and launch criteria](https://linear.app/flid/document/compliance-scope-control-map-and-launch-criteria-81b631fcf402)

## Context and problem statement

LeapView must support a managed deployment whose privacy, security and operational
commitments can be demonstrated to customers. Application features alone cannot
establish this: contracts, infrastructure configuration, staff access, incident
response, recovery and continuing oversight also determine the outcome.

GDPR and NIS2 establish legal obligations with different applicability rules.
DORA introduces financial-sector obligations and requirements for ICT supplier
arrangements. ISO/IEC 27001 provides a certifiable information security management
system (ISMS). The 18 CIS Controls provide security safeguards organised into
implementation groups. These frameworks overlap, but completion of one does not
establish compliance with the others.

The existing delivery plan covers GDPR, applicable NIS2 and CRA duties, screens
DORA applicability, and treats ISO certification as optional. It does not yet
contain a complete CIS safeguard assessment or dedicated DORA supplier readiness
workstream. This decision establishes the intended baseline and assurance target;
its acceptance is not evidence that the service already meets them.

## Decision drivers

- Make the managed service suitable for customers with privacy, security and
  supplier-assurance requirements.
- Define claims that are traceable to a specific service scope and current
  evidence.
- Reuse controls across frameworks while retaining each requirement's legal and
  assessment criteria.
- Include operating practices and the organisation alongside product features.
- Support financial customers through explicit supplier commitments.
- Keep legal launch eligibility, certification and customer-specific obligations
  independently visible.

## Considered options

- Implement individual security features and describe the product broadly as
  compliant with all five frameworks.
- Implement only the minimum applicable legal requirements, without a common
  security baseline or committed certification.
- Require all five framework claims and ISO certification before any managed
  service launch, regardless of customer or legal scope.
- Adopt a shared compliance baseline, commit to ISO certification, and qualify
  DORA supplier readiness before serving financial customers.

## Decision outcome

Adopt the shared baseline: applicable GDPR obligations, applicable national NIS2
requirements and CIS Controls v8.1 Implementation Group 2 (IG2). Operate an ISMS
and commit to ISO/IEC 27001:2022 certification for the managed-service scope.
Require DORA supplier readiness before onboarding financial customers whose use
of LeapView falls within DORA ICT third-party arrangements.

### Scope and responsibility

The scope identifies the operating legal entity, service offering, deployment
topology, regions, data flows, subprocessors, supporting systems and workforce.
It includes development and delivery systems, operator endpoints and support
access where they affect the managed service. Legal review determines
controller/processor roles, jurisdiction, registrations and sector obligations.

Document the responsibilities of LeapView, infrastructure providers and
customers. A provider certificate supports assessment of that provider's scope;
it does not certify LeapView. A managed-service claim does not extend
automatically to self-hosted installations or guarantee a customer's lawful use
of analytics data.

### Framework commitments and permitted claims

The wording below describes claims that may be approved after verification.
It is not current marketing copy.

| Framework | Commitment | Evidence required before a claim |
|---|---|---|
| GDPR | Meet applicable processor and controller obligations for the defined processing activities. | Reviewed applicability, contracts, processing records, transfer safeguards, rights handling, retention/deletion and security evidence. A compliance statement must name its scope; a certification claim requires an actual applicable certification. |
| NIS2 | Meet applicable national implementing law and Regulation (EU) 2024/2690 where applicable. | Reviewed jurisdiction and entity/service classification, plus evidence for governance, risk management, reporting and all applicable measures. If outside scope, describe alignment with specified controls. Do not present a generic NIS2 certification claim as proof of legal compliance. |
| CIS Controls | Implement and assess CIS Controls v8.1 IG2, including the IG1 safeguards it incorporates. | A safeguard-level applicability and assessment record for the managed-service scope. Claim implementation of the specified version and group only when supported; disclose exclusions and partial coverage. Do not substitute the phrase "CIS 18 certified" for this assessment. |
| ISO/IEC 27001 | Obtain and maintain ISO/IEC 27001:2022 certification, including applicable amendments, for the ISMS supporting the managed service. | An issued certificate from an accredited certification body, its exact scope and validity, and continuing surveillance. Certification applies to the ISMS in that scope; it is not a blanket product certification. |
| DORA | Support financial customers' applicable ICT third-party obligations through qualified service capabilities and contractual commitments. | A DORA supplier assessment covering the relevant services, function criticality, contracts and operating evidence. Describe the supported obligations and service scope; do not guarantee the financial entity's overall DORA compliance. |

For CIS, an omitted safeguard requires a documented applicability rationale and
review. Risk acceptance does not count as safeguard implementation or excuse a
legal requirement. IG3 safeguards may be selected for specific risks or customer
commitments without expanding the public claim to all of IG3.

DORA qualification includes Article 30 contractual provisions and relevant
supplementing rules: service and processing locations, subcontracting conditions,
service levels, incident assistance, authority cooperation, data recovery/return
and termination. Services supporting critical or important functions also require
the relevant audit/access rights, testing cooperation, continuity evidence and
exit transition arrangements. Provide information customers need for their ICT
supplier registers. Assess any direct oversight duties separately if LeapView is
designated a critical ICT third-party provider.

CRA and other applicable legal duties remain in the applicability register and
delivery plan. Choosing these five frameworks does not narrow existing statutory
obligations or defer reporting duties that are already applicable.

### One control register and an evidence-based claim gate

Maintain a shared control register mapping legal provisions, CIS safeguards,
ISO requirements and contractual commitments to implementation owners and
evidence. Each requirement records applicability, implementation status,
assessment method, evidence date, findings and review triggers. Shared evidence
may support multiple requirements, but a mapping alone does not demonstrate
conformance.

Use explicit states such as planned, implemented but unverified, verified and
not applicable with reviewed rationale. Repository tests establish bounded
product behaviour; provider configuration, operational exercises, contracts and
management records establish the corresponding service-level facts.

Before publishing or renewing a claim, management and the responsible security
and privacy/legal owners approve its wording, scope, evidence and review date.
Unresolved statutory requirements cannot be waived by management risk acceptance.
Withdraw or qualify claims when scope changes, evidence becomes invalid or a
certificate expires or is suspended.

### Launch and delivery boundaries

Managed-service launch requires verified applicable legal obligations and the
defined CIS IG2 baseline. ISO certification is a committed delivery milestone;
certificate issuance is a launch or onboarding gate when required by a customer
contract or the offering being sold. Before issuance, describe only the ISMS
practices that can actually be demonstrated. Do not claim certification.

DORA qualification gates the relevant financial-customer offering. Other managed
customers do not inherit this gate unless their contracts or applicable law
require it. Additional customer requirements must be assessed before commitment.

Linear remains the delivery authority. Reconcile the plan with this ADR by making
the ISO certification outcome committed, adding explicit CIS IG2 mapping and
assessment, and planning the DORA supplier workstream and its onboarding gate.
Named owners, estimates, dates and implementation findings belong there. Closing
an issue with a decision not to certify would not satisfy this ADR's ISO outcome.

## Consequences

Customers receive specific, reviewable assurance backed by a common evidence
system. Engineering and operations can reuse safeguards and evidence across
frameworks while maintaining clear responsibility for privacy and legal duties.

The commitment adds recurring costs for programme ownership, legal review,
operational exercises, independent assessments, certification and surveillance.
CIS assessment extends beyond the application to the supporting organisation.
Financial-customer contracts can require additional operating capabilities and
supplier negotiations. These activities need funded owners and continuing
attention after initial delivery.

Feature-only claims are rejected because they omit the service and organisational
controls customers rely on. A legal-minimum-only approach would leave the security
baseline and assurance quality inconsistent. Requiring every claim before every
launch would couple unrelated customer and certification timelines; explicit
launch, certification and sector gates preserve the relevant obligations.

## Confirmation

- A reviewed scope and applicability register identifies every relevant entity,
  service, jurisdiction and additional legal duty, with reasons for exclusions.
- The control register covers all IG1/IG2 safeguards, applicable legal provisions,
  ISO requirements and selected controls in the statement of applicability.
  Every claimed outcome has current evidence and an accountable owner.
- Verification includes access/deprovisioning, privacy requests, deletion across
  retained copies and restored backups, incident notification exercises, and a
  complete recovery of the approved managed deployment against agreed objectives.
- Internal audit, management review and independent technical assessment close
  findings that prevent the intended claims. Certification is confirmed through
  certificate issuance, scope and validity, not completion of readiness tasks.
- Financial-customer onboarding checks the approved DORA supplier assessment,
  contract and supporting operational evidence for that customer's service use.
- The customer assurance pack states the service boundary, shared responsibility,
  exact claims and supporting assessment/certificate details. Periodic reviews
  and material changes trigger revalidation.

## References

Official sources consulted on 2026-09-20:

- [GDPR](https://eur-lex.europa.eu/eli/reg/2016/679/oj/eng), including Articles 28,
  32 and 42 on processors, security and certification.
- [NIS2](https://eur-lex.europa.eu/eli/dir/2022/2555/oj/eng), including scope,
  governance, risk management, reporting and jurisdiction;
  [ENISA implementation guidance](https://www.enisa.europa.eu/publications/nis2-technical-implementation-guidance)
  for Regulation (EU) 2024/2690. Applicable national law must also be reviewed.
- [DORA](https://eur-lex.europa.eu/eli/reg/2022/2554/oj/eng), especially Articles
  28–31 on third-party risk, contractual provisions and critical providers.
- [ISO/IEC 27001:2022](https://www.iso.org/standard/27001), including the
  distinction between implementation and certification.
- [CIS implementation groups](https://www.cisecurity.org/controls/implementation-groups)
  and [CIS assessment specification](https://cas.docs.cisecurity.org/en/latest/).
