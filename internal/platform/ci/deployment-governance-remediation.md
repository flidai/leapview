# Deployment governance remediation

FAI-1066 follows the [repository-wide reassessment](reassessment-2026-10-03.md).
This is a proposed configuration for independent owner review, not a record of
applied settings. The strengthened live read-only audit on 4 October 2026 found eight failures:
required reviewers and administrator bypass in all three governed environments,
plus the hosted demo's missing main-only branch policy and the qualification
environment's temporary branch allowance.

## Proposed owner decision

Designate the existing `flidai/core-team` (GitHub team ID `19217761`) as the
required reviewer for all three environments. Its current roster is `Yacobolo`,
`Ganesh-403`, and `Anand-Bora-0001`. Team existence and membership are verified;
membership alone is not the owner's designation. An independent approving review
of this proposal must explicitly accept that designation before application.

| Environment | Required reviewer | Self-review | Admin bypass | Allowed branch |
| --- | --- | --- | --- | --- |
| `leapview-demo` | Core Team | Prevented | Disabled | `main` only |
| `leapview-ephemeral-qualification` | Core Team | Prevented | Disabled | `main` only |
| `leapview-site-production` | Core Team | Prevented | Disabled | `main` only |

The qualification environment currently also permits
`ganesh/fai-522-replacement-host-rebuild` (branch-policy ID `60796112`). FAI-522 is
explicitly paused in Backlog. Revoke that temporary allowance as part of this
reviewed change; future qualification refs require a new reviewed policy change.
This does not resume or implement the paused recovery project.

## Apply after independent review

Record the approving owner, reviewer, proposal revision, and before/after metadata
on FAI-1066. The [governance contract](../../../docs/articles/security/governance.md)
requires two-person review for environment-policy changes. Preserve environment
secrets; this change does not replace or enumerate them.

For each named environment, the documented REST environment update fields are:

```json
{
  "wait_timer": 0,
  "prevent_self_review": true,
  "reviewers": [{"type": "Team", "id": 19217761}],
  "deployment_branch_policy": {
    "protected_branches": false,
    "custom_branch_policies": true
  }
}
```

Ensure the subordinate deployment-branch policies contain exactly one branch
entry named `main`. Remove the temporary qualification policy above. In each
environment's GitHub settings, disable **Allow administrators to bypass
configured protection rules**. The REST update documentation does not expose a
supported `can_admins_bypass` request field; do not assume an undocumented PUT
field took effect. Verify the GET response explicitly reports
`can_admins_bypass=false`.

## Acceptance

An offline snapshot of the proposed configuration passes the strengthened audit;
this demonstrates the intended policy shape, not live enforcement. Completion
requires the recorded independent review and the live checks below:

```bash
task security:governance
gh api repos/flidai/leapview/environments/leapview-demo/deployment-branch-policies
gh api repos/flidai/leapview/environments/leapview-ephemeral-qualification/deployment-branch-policies
gh api repos/flidai/leapview/environments/leapview-site-production/deployment-branch-policies
```

All three branch-policy responses must contain only `main` with type `branch`.
The audit must pass without findings; reviewer, self-review, and bypass settings
must match the table. No deployment or Terraform apply is needed for validation.
