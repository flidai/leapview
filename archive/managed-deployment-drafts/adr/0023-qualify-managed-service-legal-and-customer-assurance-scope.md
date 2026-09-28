# ADR-0023: Qualify managed-service legal and customer assurance scope

Status: accepted

Decision date: 2026-09-20

Implementation: pending; delivery tracked in Linear

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0022](0022-adopt-a-managed-service-compliance-and-assurance-baseline.md),
legal coverage, customer qualification and assurance deliverables

Related: [managed compliance project](https://linear.app/flid/project/managed-leapview-compliance-readiness-f209990e91f7),
[control map and launch criteria](https://linear.app/flid/document/compliance-scope-control-map-and-launch-criteria-81b631fcf402),
[CIS IG2 verification](https://linear.app/flid/issue/FAI-993),
[ePrivacy qualification](https://linear.app/flid/issue/FAI-994),
[ordinary DORA qualification](https://linear.app/flid/issue/FAI-995),
[critical/important DORA qualification](https://linear.app/flid/issue/FAI-996),
[committed ISO certification](https://linear.app/flid/issue/FAI-992)

## Context and problem statement

ADR-0022 establishes GDPR, applicable NIS2 and CIS Controls v8.1 IG2 as the managed
service baseline, commits to ISO/IEC 27001 certification, and gates relevant
financial-customer onboarding on DORA supplier readiness. Its legal scope and
assurance pack need more explicit boundaries to guide architecture, contracts
and customer qualification.

LeapView is a B2B analytics platform. Priority customer sectors, countries and
permitted data classes have not yet been approved. Business size does not
establish data sensitivity. Website tracking cannot be inferred from the word
analytics. The initial service profile must be approved before sales commitments
or onboarding; regulated and sensitive workloads require explicit qualification.

The existing plan already includes Data Act screening and customer switching,
CRA reporting and product conformity, and customer assurance work. These need
clear decision-level commitments and linkage to delivery. The older optional ISO
task also needs reconciliation with ADR-0022's certification commitment.

## Decision drivers

- Make architecture-affecting legal obligations visible in the baseline.
- Qualify actual customer uses and data flows before committing to an offering.
- Support customers' supplier assessments even outside LeapView's direct scope.
- Tie contract promises to capabilities supported by upstream suppliers.
- Preserve the agreed security baseline and accurate public claims.

## Considered options

- Leave additional legal duties and customer evidence implicit in scoping tasks.
- Add explicit legal and customer qualification requirements while retaining the
  baseline and certification decision.
- Reduce the launch requirement to IG1 plus selected IG2 safeguards based on an
  assumed low-risk initial customer segment.

## Decision outcome

Make the legal and customer qualification requirements below explicit. Retain
ADR-0022's full applicable IG2 launch baseline and committed ISO certification.
A future change to that baseline requires a separate decision supported by an
approved service profile and risk assessment.

### Initial service profile

Management, product, security and privacy/legal owners approve the initial
customer sectors, countries, supported use cases, data classifications, processing
locations and excluded workloads. Record enterprise procurement assumptions and
which offerings require certification before sale or onboarding. Until approved,
these are unresolved scope decisions, not a claim that ordinary business data is
low risk. Customer qualification must enforce the approved profile and trigger
review when a proposed use falls outside it.

ISO/IEC 27001 is the primary independent certification investment for the managed
service. Additional assurance programmes, such as SOC 2 or national schemes,
require a demonstrated customer, procurement or legal need and a separate scope
and resourcing decision.

### Explicit Data Act and ePrivacy coverage

Assess whether each managed offering is a data processing service under the EU
Data Act. Where applicable, implement switching and exit contracts, required
interfaces, machine-readable export of exportable data and digital assets,
documented metadata/formats, retrieval and deletion behaviour, transition support
and the applicable timing and fee rules. Test a representative customer exit;
portable dashboard source alone does not prove data portability.

The Data Act has applied since 12 September 2025. Applicable switching charges,
including switching-related data egress charges, must be removed from
12 January 2027. Review pricing and upstream terms against these obligations.
Also implement applicable safeguards and procedures for unlawful third-country
government access to non-personal data held in the EU. Keep this assessment
distinct from GDPR personal-data transfer safeguards.

Inventory actual terminal storage/access across the managed UI, public site,
embeds, authentication, telemetry and any SDK or pixel. Assess ePrivacy Article
5(3) and relevant national rules by purpose and technology. Where consent is
required, gate collection on valid consent and support withdrawal; where an
exemption is used, retain its jurisdiction-specific rationale. Record customer
and LeapView responsibilities for each collection mode. A cookieless mechanism
is not evidence of exemption, and a BI product does not inherently require
visitor tracking. Test consent-dependent behaviour and exemptions against the
actual deployed configuration.

### NIS2 supplier assurance and DORA qualification

Maintain two NIS2 tracks: LeapView's own applicable legal obligations, and
evidence supporting customers' supply-chain security assessments. Provide
reviewed security measures, vulnerability-handling commitments, incident contacts
and notification terms, continuity evidence and relevant subprocessor information
even when LeapView is outside direct NIS2 scope. Customer requirements become
specific assessed contractual commitments; they do not automatically place
LeapView within direct statutory scope.

DORA supplier qualification has two levels:

1. **Covered ICT arrangements:** assess the applicable service and Article 30(2)
   contract provisions, locations, service levels, incident assistance, authority
   cooperation, data protection, recovery/return and termination. Provide the
   information customers need for their ICT supplier registers.
2. **Critical or important functions:** add applicable Article 30(3) and
   supplementing requirements for measurable service levels, reporting, tested
   continuity, audit/access rights, testing cooperation and exit transition.

Record the financial customer's classification of the supported function and the
resulting qualification level. Unresolved classification blocks that customer's
qualification. The ordinary level can pass without waiting for capabilities
exclusive to the higher level. Verify that upstream supplier contracts and
operating capabilities support each commitment; identify and resolve gaps before
onboarding. These levels do not replace assessment of any direct oversight duties
if LeapView is designated a critical ICT third-party provider.

### CRA product scope and dates

Assess managed functionality, distributed/self-hosted server software, CLI and
desktop distributions, and associated remote processing separately. Record the
economic-operator role and reasoned applicability for each. The managed-service
assurance boundary and open-source licensing do not settle product-law scope.

For applicable manufacturer products, Article 14 reporting has applied since
11 September 2026; reporting readiness is an immediate duty. Main CRA obligations
apply from 11 December 2027. Assess the distinct open-source steward regime and
its commencement separately. Maintain dated product decisions and reporting
rehearsal evidence; future conformity work cannot defer an already-applicable
reporting obligation.

### Customer assurance pack and claim approval

The approved managed offering must have an accessible assurance pack containing:

- A ready-to-sign DPA, technical and organisational measures, subprocessors and
  shared responsibilities.
- Locations for production data, backups, telemetry and support access, plus
  applicable transfer arrangements and change-notification commitments.
- Retention/deletion behaviour, export and switching capabilities, and support
  for customer privacy assessments and data-subject requests.
- A shareable independent penetration-test summary and remediation status.
- Agreed RTO/RPO targets, achieved recovery time and observed data loss from
  dated exercises, including scope, limitations and whether targets were met.
- Contractual incident-notification commitments, contacts and escalation paths.
- Exact approved claims and the scope/validity of any issued certificate.

Detailed exploit reports and personal or secret data stay in restricted evidence
storage. EU hosting commitments must describe the full processing footprint;
GDPR does not universally require EU-only storage. No pack or provider
certificate guarantees a customer's overall compliance.

## Consequences

Legal scope drives export design, pricing, collection behaviour, supplier
contracts and onboarding. Customers can evaluate concrete commitments and
evidence. NIS2 supplier readiness remains useful even where direct duties do not
apply, and DORA qualification reflects the customer's supported function.

The scope review, supplier negotiations, evidence maintenance and full IG2
baseline can delay launch or particular customer deals. These costs must be
resourced. Reducing safeguards based on an unverified low-risk segment is
rejected. Generic legal scoping is also rejected because it can hide product and
contract obligations until late in delivery.

## Confirmation

- The approved service profile is recorded and exercised in customer onboarding.
- Data Act scope, export/switching rehearsal, contracts, pricing and government
  access procedures have reviewed evidence or lawful non-applicability decisions.
- A deployed collection inventory links each purpose to consent behaviour or a
  reviewed exemption, with tests and customer responsibilities.
- NIS2 supplier evidence and both DORA qualification levels have explicit
  acceptance criteria; onboarding verifies function classification and upstream
  support for promised terms.
- Each CRA product/role decision has dates, owners and applicable reporting
  evidence; no broad SaaS or open-source exclusion is assumed.
- Linear reflects committed ISO certification, CIS IG2 assessment and remediation,
  and the applicable privacy, sector and launch gates. Issue closure alone does
  not authorise a public compliance claim.
- The assurance pack is reviewed against operating evidence and current contracts
  before publication and following material scope changes.

## References

Official sources reviewed on 2026-09-20:

- [Data Act explanation](https://digital-strategy.ec.europa.eu/en/factpages/data-act-explained).
- [EDPB Article 5(3) technical-scope guidance](https://www.edpb.europa.eu/system/files/documents/2024-10/edpb_guidelines_202302_technical_scope_art_53_eprivacydirective_v2_en_0.pdf);
  exemptions require separate national analysis.
- [NIS2](https://eur-lex.europa.eu/eli/dir/2022/2555/oj/eng), Article 21.
- [DORA](https://eur-lex.europa.eu/eli/reg/2022/2554/oj/eng), Articles 28–30.
- [CRA scope and timeline](https://digital-strategy.ec.europa.eu/en/policies/cra-summary)
  and [open-source roles](https://digital-strategy.ec.europa.eu/en/policies/cra-open-source).
- [GDPR](https://eur-lex.europa.eu/eli/reg/2016/679/oj/eng), Chapter V.
