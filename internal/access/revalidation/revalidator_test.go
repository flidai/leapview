package revalidation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/permissions"
)

func newCallerAuthorityRevalidator(tokens access.APITokenAuthorityEvidenceReader, sessions access.SessionAuthorityEvidenceReader, current func(context.Context, string, access.PermissionPair, string) (bool, error)) jobs.AuthorityRevalidator {
	requirement, err := refreshmodule.CreateRefreshRunTypedOperationRequirement()
	return NewCallerAuthorityRevalidator(tokens, sessions, current, requirement, err)
}

func newDelegatedWorkloadRevalidator(grants ExecutionGrantAuthorityReader, current func(context.Context, string, access.PermissionPair, string) (bool, error), instanceID, environment string) jobs.AuthorityRevalidator {
	return NewDelegatedWorkloadRevalidator(grants, current, instanceID, environment)
}

type jobAuthorityTokenReader struct {
	token access.APIToken
	err   error
}

func (r jobAuthorityTokenReader) APITokenAuthorityEvidence(context.Context, string, string, time.Time) (access.APIToken, error) {
	return r.token, r.err
}

type jobAuthoritySessionReader struct {
	session access.Session
	err     error
}

func (r jobAuthoritySessionReader) SessionAuthorityEvidence(context.Context, string, string, string, time.Time) (access.Session, error) {
	return r.session, r.err
}

type jobAuthorityExecutionGrantReader struct {
	grant access.ExecutionGrant
	err   error
}

func (r jobAuthorityExecutionGrantReader) CurrentExecutionGrant(context.Context, string, string) (access.ExecutionGrant, error) {
	return r.grant, r.err
}

func testCallerAuthority(t *testing.T, expiresAt time.Time) jobs.AuthorityEnvelope {
	t.Helper()
	projectID, err := projectgraph.NewResourceID("project_sales")
	if err != nil {
		t.Fatal(err)
	}
	pipelineID, err := projectgraph.NewResourceID("pipeline_daily")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(pipelineID, projectgraph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionPipelineRun, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	contractPair, err := access.ToContractPermissionPair(pair)
	if err != nil {
		t.Fatal(err)
	}
	return jobs.AuthorityEnvelope{
		Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.CallerAuthorityMode,
		ActorPrincipalID: "principal-1", ExecutionPrincipalID: "principal-1",
		Credential:  &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: "token-1", Fingerprint: "fingerprint-1", ExpiresAt: expiresAt.UTC()},
		Target:      jobs.AuthorityTarget{ProjectID: projectID.String(), Environment: "prod", ResourceKind: string(projectgraph.KindPipeline), ResourceID: pipelineID.String()},
		Permissions: []permissions.Pair{contractPair},
	}
}

func callerConnectionUsePair(t *testing.T, projectID, connectionID string) permissions.Pair {
	t.Helper()
	project, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := projectgraph.NewResourceID(connectionID)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(connection, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionConnectionUse, project, resource)
	if err != nil {
		t.Fatal(err)
	}
	contractPair, err := access.ToContractPermissionPair(pair)
	if err != nil {
		t.Fatal(err)
	}
	return contractPair
}

func testAuthorityToken(t *testing.T, authority jobs.AuthorityEnvelope) access.APIToken {
	t.Helper()
	pairs := make([]access.PermissionPair, len(authority.Permissions))
	for index, pair := range authority.Permissions {
		converted, err := access.FromContractPermissionPair(pair)
		if err != nil {
			t.Fatal(err)
		}
		pairs[index] = converted
	}
	return access.APIToken{
		ID: authority.Credential.ID, PrincipalID: authority.ActorPrincipalID,
		TokenFingerprint:  authority.Credential.Fingerprint,
		PermissionProfile: access.PermissionCatalogProfile,
		Permissions:       pairs,
		ExpiresAt:         authority.Credential.ExpiresAt.Format(time.RFC3339Nano),
	}
}

func TestCallerAuthorityRevalidatorRejectsQueuedCredentialExpiry(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour)
	authority := testCallerAuthority(t, expiresAt)
	revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{err: access.ErrForbidden}, nil, func(context.Context, string, access.PermissionPair, string) (bool, error) {
		return true, nil
	})
	err := revalidator.Revalidate(t.Context(), authority)
	if !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("expiry revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorRejectsRevokedQueuedCredential(t *testing.T) {
	authority := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
	revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: testAuthorityToken(t, authority), err: access.ErrForbidden}, nil, func(context.Context, string, access.PermissionPair, string) (bool, error) {
		return true, nil
	})
	err := revalidator.Revalidate(t.Context(), authority)
	if !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("revocation revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorRejectsChangedCapturedCredentialEvidence(t *testing.T) {
	current := func(context.Context, string, access.PermissionPair, string) (bool, error) { return true, nil }
	t.Run("API token", func(t *testing.T) {
		authority := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
		base := testAuthorityToken(t, authority)
		mutations := map[string]func(*access.APIToken){
			"id":          func(token *access.APIToken) { token.ID = "token-other" },
			"fingerprint": func(token *access.APIToken) { token.TokenFingerprint = "fingerprint-other" },
			"expiry": func(token *access.APIToken) {
				token.ExpiresAt = time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339Nano)
			},
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				token := base
				mutate(&token)
				revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: token}, nil, current)
				if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
					t.Fatalf("changed token evidence error = %v, want authority invalid", err)
				}
			})
		}
	})
	t.Run("browser session", func(t *testing.T) {
		authority := browserSessionAuthority(testCallerAuthority(t, time.Now().UTC().Add(time.Hour)))
		base := testAuthoritySession(authority)
		mutations := map[string]func(*access.Session){
			"id":          func(session *access.Session) { session.ID = "session-other" },
			"fingerprint": func(session *access.Session) { session.TokenFingerprint = "fingerprint-other" },
			"expiry": func(session *access.Session) {
				session.ExpiresAt = time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339Nano)
			},
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				session := base
				mutate(&session)
				revalidator := newCallerAuthorityRevalidator(nil, jobAuthoritySessionReader{session: session}, current)
				if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
					t.Fatalf("changed session evidence error = %v, want authority invalid", err)
				}
			})
		}
	})
}

func TestCallerAuthorityRevalidatorChecksExactPipelineResource(t *testing.T) {
	authority := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
	revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: testAuthorityToken(t, authority)}, nil, func(_ context.Context, _ string, pair access.PermissionPair, environment string) (bool, error) {
		return pair.Target.ProjectID.String() == authority.Target.ProjectID && pair.Target.ResourceID.String() == authority.Target.ResourceID && pair.Target.ResourceKind == projectgraph.KindPipeline && pair.Action == access.ActionPipelineRun && environment == authority.Target.Environment, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); err != nil {
		t.Fatalf("exact resource revalidation error = %v", err)
	}
	denying := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: testAuthorityToken(t, authority)}, nil, func(context.Context, string, access.PermissionPair, string) (bool, error) {
		return false, nil
	})
	if err := denying.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("unrelated-resource revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorChecksRefreshConnectionDependencies(t *testing.T) {
	authority := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
	authority.Permissions = append(authority.Permissions,
		callerConnectionUsePair(t, authority.Target.ProjectID, "connection_orders"),
		callerConnectionUsePair(t, authority.Target.ProjectID, "connection_inventory"),
	)
	token := testAuthorityToken(t, authority)
	checked := make(map[string]bool, len(authority.Permissions))
	revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: token}, nil, func(_ context.Context, principalID string, pair access.PermissionPair, environment string) (bool, error) {
		if principalID != authority.ActorPrincipalID || environment != authority.Target.Environment {
			return false, nil
		}
		checked[pair.Key()] = true
		return true, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); err != nil {
		t.Fatalf("live pipeline and connection authority revalidation error = %v", err)
	}
	if len(checked) != len(authority.Permissions) {
		t.Fatalf("checked %d permission pairs, want all %d", len(checked), len(authority.Permissions))
	}

	denying := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: token}, nil, func(_ context.Context, _ string, pair access.PermissionPair, _ string) (bool, error) {
		return pair.Target.ResourceID.String() != "connection_inventory", nil
	})
	if err := denying.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("denied connection authority error = %v, want authority invalid", err)
	}

	withoutConnectionCeiling := token
	withoutConnectionCeiling.Permissions = withoutConnectionCeiling.Permissions[:1]
	ceiling := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: withoutConnectionCeiling}, nil, func(context.Context, string, access.PermissionPair, string) (bool, error) {
		return true, nil
	})
	if err := ceiling.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("missing connection token ceiling error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorRejectsInvalidRefreshConnectionDependencies(t *testing.T) {
	base := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
	baseToken := testAuthorityToken(t, base)
	connection := callerConnectionUsePair(t, base.Target.ProjectID, "connection_orders")
	otherProjectConnection := callerConnectionUsePair(t, "project_inventory", "connection_orders")
	wrongActionResource, err := projectgraph.NewResourceID(base.Target.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	wrongActionRef, err := access.NewResourceRef(wrongActionResource, projectgraph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	wrongAction, err := access.NewExactPermissionPair(access.ActionPipelineRead, projectgraph.ResourceID(base.Target.ProjectID), wrongActionRef)
	if err != nil {
		t.Fatal(err)
	}
	wrongActionContract, err := access.ToContractPermissionPair(wrongAction)
	if err != nil {
		t.Fatal(err)
	}
	wrongKind := permissions.Pair{
		Action: permissions.Action(access.ActionConnectionUse), Profile: access.PermissionCatalogProfile,
		Target: permissions.Target{Scope: permissions.ScopeResource, ProjectID: base.Target.ProjectID, ResourceKind: permissions.Kind(projectgraph.KindPipeline), ResourceID: base.Target.ResourceID},
	}
	wildcard := permissions.Pair{
		Action: permissions.Action(access.ActionConnectionUse), Profile: access.PermissionCatalogProfile,
		Target: permissions.Target{Scope: permissions.ScopeProject, ProjectID: base.Target.ProjectID, ResourceKind: permissions.Kind(projectgraph.KindConnection), IncludeFuture: true},
	}
	tests := map[string]func(*jobs.AuthorityEnvelope){
		"missing pipeline.run root": func(authority *jobs.AuthorityEnvelope) { authority.Permissions = []permissions.Pair{connection} },
		"arbitrary extra action": func(authority *jobs.AuthorityEnvelope) {
			authority.Permissions = append(authority.Permissions, wrongActionContract)
		},
		"wrong connection kind": func(authority *jobs.AuthorityEnvelope) {
			authority.Permissions = append(authority.Permissions, wrongKind)
		},
		"cross project": func(authority *jobs.AuthorityEnvelope) {
			authority.Permissions = append(authority.Permissions, otherProjectConnection)
		},
		"future connection wildcard": func(authority *jobs.AuthorityEnvelope) {
			authority.Permissions = append(authority.Permissions, wildcard)
		},
		"duplicate connection pair": func(authority *jobs.AuthorityEnvelope) {
			authority.Permissions = append(authority.Permissions, connection, connection)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			authority := base
			authority.Permissions = append([]permissions.Pair(nil), base.Permissions...)
			mutate(&authority)
			revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: baseToken}, nil, func(context.Context, string, access.PermissionPair, string) (bool, error) {
				return true, nil
			})
			if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
				t.Fatalf("invalid permission set revalidation error = %v, want authority invalid", err)
			}
		})
	}
}

func TestCallerAuthorityRevalidatorDoesNotAllowConnectionDependenciesForOtherRequirements(t *testing.T) {
	authority := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
	pipelineID, err := projectgraph.NewResourceID(authority.Target.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := access.NewResourceRef(pipelineID, projectgraph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	read, err := access.NewExactPermissionPair(access.ActionPipelineRead, projectgraph.ResourceID(authority.Target.ProjectID), pipeline)
	if err != nil {
		t.Fatal(err)
	}
	readContract, err := access.ToContractPermissionPair(read)
	if err != nil {
		t.Fatal(err)
	}
	authority.Permissions = []permissions.Pair{readContract, callerConnectionUsePair(t, authority.Target.ProjectID, "connection_orders")}
	token := testAuthorityToken(t, authority)
	requirement, err := access.NewTypedOperationRequirementService().Requirement(access.ActionPipelineRead, string(access.TypedOperationResolverPipeline))
	if err != nil {
		t.Fatal(err)
	}
	revalidator := NewCallerAuthorityRevalidator(jobAuthorityTokenReader{token: token}, nil, func(context.Context, string, access.PermissionPair, string) (bool, error) {
		return true, nil
	}, requirement, nil)
	if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("unrelated typed requirement accepted connection dependency: %v", err)
	}
}

func TestCallerAuthorityRevalidatorRejectsWrongTypedAction(t *testing.T) {
	authority := testCallerAuthority(t, time.Now().UTC().Add(time.Hour))
	authority.Permissions[0].Action = permissions.Action("pipeline.read")
	revalidator := newCallerAuthorityRevalidator(jobAuthorityTokenReader{token: testAuthorityToken(t, authority)}, nil, func(context.Context, string, access.PermissionPair, string) (bool, error) {
		return true, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("wrong typed action revalidation error = %v, want authority invalid", err)
	}
}

func browserSessionAuthority(authority jobs.AuthorityEnvelope) jobs.AuthorityEnvelope {
	authority.Credential.Class = "session"
	return authority
}

func testAuthoritySession(authority jobs.AuthorityEnvelope) access.Session {
	return access.Session{ID: authority.Credential.ID, PrincipalID: authority.ActorPrincipalID, Kind: access.SessionKindBrowser, TokenFingerprint: authority.Credential.Fingerprint, ExpiresAt: authority.Credential.ExpiresAt.Format(time.RFC3339Nano)}
}

func TestCallerAuthorityRevalidatorRejectsExpiredBrowserSession(t *testing.T) {
	authority := browserSessionAuthority(testCallerAuthority(t, time.Now().UTC().Add(time.Hour)))
	session := testAuthoritySession(authority)
	session.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	revalidator := newCallerAuthorityRevalidator(nil, jobAuthoritySessionReader{session: session}, func(context.Context, string, access.PermissionPair, string) (bool, error) {
		return true, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("expired browser-session revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorRejectsRevokedBrowserSession(t *testing.T) {
	authority := browserSessionAuthority(testCallerAuthority(t, time.Now().UTC().Add(time.Hour)))
	revalidator := newCallerAuthorityRevalidator(nil, jobAuthoritySessionReader{session: testAuthoritySession(authority), err: access.ErrForbidden}, func(context.Context, string, access.PermissionPair, string) (bool, error) {
		return true, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("revoked browser-session revalidation error = %v, want authority invalid", err)
	}
}

func TestCallerAuthorityRevalidatorAcceptsLiveBrowserSession(t *testing.T) {
	authority := browserSessionAuthority(testCallerAuthority(t, time.Now().UTC().Add(time.Hour)))
	revalidator := newCallerAuthorityRevalidator(nil, jobAuthoritySessionReader{session: testAuthoritySession(authority)}, func(_ context.Context, _ string, pair access.PermissionPair, environment string) (bool, error) {
		return pair.Target.ProjectID.String() == authority.Target.ProjectID && environment == authority.Target.Environment && pair.Target.ResourceID.String() == authority.Target.ResourceID && pair.Action == access.ActionPipelineRun, nil
	})
	if err := revalidator.Revalidate(t.Context(), authority); err != nil {
		t.Fatalf("live browser-session revalidation error = %v", err)
	}
}

func testDelegatedAuthority(t *testing.T, expiresAt time.Time) (jobs.AuthorityEnvelope, access.ExecutionGrant) {
	t.Helper()
	projectID, err := projectgraph.NewResourceID("project_sales")
	if err != nil {
		t.Fatal(err)
	}
	pipelineID, err := projectgraph.NewResourceID("pipeline_daily")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(pipelineID, projectgraph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionPipelineRun, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	grant := access.ExecutionGrant{
		ID: "grant-1", Profile: access.DurableGrantProfile,
		Target:               access.DurableGrantTarget{InstanceID: "instance-1", ProjectID: projectID, ResourceUID: "00000000-0000-7000-8000-000000000001", ResourceID: pipelineID, ResourceKind: projectgraph.KindPipeline},
		Issuer:               access.GrantIssuerEvidence{PrincipalID: "00000000-0000-7000-8000-000000000010", Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: "00000000-0000-7000-8000-000000000011", Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
		ExecutionPrincipalID: "00000000-0000-7000-8000-000000000012", Permissions: []access.PermissionPair{pair},
		WorkflowID: "workflow-1", WorkflowRevision: "revision-1", ClosureDigest: "closure-1", BindingDigest: "binding-1", DestinationDigest: "destination-1", TriggerDigest: "trigger-1",
		IssuedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: expiresAt.UTC(), Fingerprint: "grant-fingerprint-1",
	}
	contractPair, err := access.ToContractPermissionPair(pair)
	if err != nil {
		t.Fatal(err)
	}
	authority := jobs.AuthorityEnvelope{
		Profile: jobs.AuthorityEnvelopeProfile, Mode: jobs.DelegatedWorkloadMode,
		// The actor is the grant issuer. Any schedule/trigger identity is not
		// substituted for it; the workload recipient is separate.
		ActorPrincipalID: grant.Issuer.PrincipalID, ExecutionPrincipalID: grant.ExecutionPrincipalID,
		Target:         jobs.AuthorityTarget{InstanceID: grant.Target.InstanceID, ProjectID: grant.Target.ProjectID.String(), Environment: "production", ResourceUID: grant.Target.ResourceUID, ResourceKind: string(grant.Target.ResourceKind), ResourceID: grant.Target.ResourceID.String()},
		Permissions:    []permissions.Pair{contractPair},
		ExecutionGrant: &jobs.ExecutionGrantEvidence{ID: grant.ID, Fingerprint: grant.Fingerprint, ExpiresAt: grant.ExpiresAt, WorkflowID: grant.WorkflowID, WorkflowRevision: grant.WorkflowRevision, ClosureDigest: grant.ClosureDigest, BindingDigest: grant.BindingDigest, DestinationDigest: grant.DestinationDigest, TriggerDigest: grant.TriggerDigest},
	}
	return authority, grant
}

func allowDelegatedPermission(context.Context, string, access.PermissionPair, string) (bool, error) {
	return true, nil
}

func TestDelegatedWorkloadRevalidatorRejectsRevokedExpiredAndDisabledGrant(t *testing.T) {
	authority, _ := testDelegatedAuthority(t, time.Now().UTC().Add(time.Hour))
	for name, grantErr := range map[string]error{"revoked": access.ErrGrantRevoked, "disabled workload principal": access.ErrGrantPrincipalInactive} {
		t.Run(name, func(t *testing.T) {
			revalidator := newDelegatedWorkloadRevalidator(jobAuthorityExecutionGrantReader{err: grantErr}, allowDelegatedPermission, authority.Target.InstanceID, authority.Target.Environment)
			if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
				t.Fatalf("revalidation error = %v, want authority invalid", err)
			}
		})
	}
	expiredAuthority, _ := testDelegatedAuthority(t, time.Now().UTC().Add(-time.Minute))
	revalidator := newDelegatedWorkloadRevalidator(jobAuthorityExecutionGrantReader{err: access.ErrGrantExpired}, allowDelegatedPermission, expiredAuthority.Target.InstanceID, expiredAuthority.Target.Environment)
	if err := revalidator.Revalidate(t.Context(), expiredAuthority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("expired revalidation error = %v, want authority invalid", err)
	}
}

func TestDelegatedWorkloadRevalidatorRejectsTargetPairAndFingerprintMismatch(t *testing.T) {
	authority, grant := testDelegatedAuthority(t, time.Now().UTC().Add(time.Hour))
	tests := map[string]func(*jobs.AuthorityEnvelope){
		"target": func(value *jobs.AuthorityEnvelope) { value.Target.ResourceID = "pipeline_other" },
		"pair":   func(value *jobs.AuthorityEnvelope) { value.Permissions[0].Target.ResourceID = "pipeline_other" },
		"actor": func(value *jobs.AuthorityEnvelope) {
			value.ActorPrincipalID = "00000000-0000-7000-8000-000000000013"
		},
		"execution principal": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionPrincipalID = "00000000-0000-7000-8000-000000000014"
		},
		"run-as": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionPrincipalID = "00000000-0000-7000-8000-000000000014"
		},
		"fingerprint": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.Fingerprint = "different-fingerprint"
		},
		"workflow": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.WorkflowID = "different-workflow"
		},
		"revision": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.WorkflowRevision = "different-revision"
		},
		"sql": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.ClosureDigest = "different-sql-closure"
		},
		"model": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.ClosureDigest = "different-model-closure"
		},
		"closure": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.ClosureDigest = "different-closure"
		},
		"binding": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.BindingDigest = "different-binding"
		},
		"destination": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.DestinationDigest = "different-destination"
		},
		"trigger": func(value *jobs.AuthorityEnvelope) {
			value.ExecutionGrant.TriggerDigest = "different-trigger"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := authority
			mutated.Permissions = append([]permissions.Pair(nil), authority.Permissions...)
			mutated.ExecutionGrant = &jobs.ExecutionGrantEvidence{ID: authority.ExecutionGrant.ID, Fingerprint: authority.ExecutionGrant.Fingerprint, ExpiresAt: authority.ExecutionGrant.ExpiresAt, WorkflowID: authority.ExecutionGrant.WorkflowID, WorkflowRevision: authority.ExecutionGrant.WorkflowRevision, ClosureDigest: authority.ExecutionGrant.ClosureDigest, BindingDigest: authority.ExecutionGrant.BindingDigest, DestinationDigest: authority.ExecutionGrant.DestinationDigest, TriggerDigest: authority.ExecutionGrant.TriggerDigest}
			mutate(&mutated)
			revalidator := newDelegatedWorkloadRevalidator(jobAuthorityExecutionGrantReader{grant: grant}, allowDelegatedPermission, authority.Target.InstanceID, authority.Target.Environment)
			if err := revalidator.Revalidate(t.Context(), mutated); !errors.Is(err, jobs.ErrAuthorityInvalid) {
				t.Fatalf("revalidation error = %v, want authority invalid", err)
			}
		})
	}
}

func TestDelegatedWorkloadRevalidatorAcceptsLiveGrant(t *testing.T) {
	authority, grant := testDelegatedAuthority(t, time.Now().UTC().Add(time.Hour))
	// A delegated job may retain initiating credential evidence for audit, but
	// that credential is not a delegated execution ceiling.
	authority.Credential = &jobs.CredentialEvidence{Class: jobs.CredentialClassAPIToken, ID: "initiating-token", Fingerprint: "initiating-fingerprint", ExpiresAt: time.Now().UTC().Add(-time.Hour)}
	revalidator := newDelegatedWorkloadRevalidator(jobAuthorityExecutionGrantReader{grant: grant}, allowDelegatedPermission, authority.Target.InstanceID, authority.Target.Environment)
	if err := revalidator.Revalidate(t.Context(), authority); err != nil {
		t.Fatalf("live delegated grant revalidation error = %v", err)
	}
}

func TestDelegatedWorkloadRevalidatorRejectsRemovedWorkloadPermission(t *testing.T) {
	authority, grant := testDelegatedAuthority(t, time.Now().UTC().Add(time.Hour))
	revalidator := newDelegatedWorkloadRevalidator(
		jobAuthorityExecutionGrantReader{grant: grant},
		func(context.Context, string, access.PermissionPair, string) (bool, error) { return false, nil },
		authority.Target.InstanceID,
		authority.Target.Environment,
	)
	if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("removed workload permission error = %v, want authority invalid", err)
	}
}

func TestDelegatedWorkloadRevalidatorRejectsBroadenedIssuedCeiling(t *testing.T) {
	authority, grant := testDelegatedAuthority(t, time.Now().UTC().Add(time.Hour))
	resource, err := access.NewResourceRef(grant.Target.ResourceID, grant.Target.ResourceKind)
	if err != nil {
		t.Fatal(err)
	}
	readPair, err := access.NewExactPermissionPair(access.ActionPipelineRead, grant.Target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	contractPair, err := access.ToContractPermissionPair(readPair)
	if err != nil {
		t.Fatal(err)
	}
	authority.Permissions = append(authority.Permissions, contractPair)
	revalidator := newDelegatedWorkloadRevalidator(jobAuthorityExecutionGrantReader{grant: grant}, allowDelegatedPermission, authority.Target.InstanceID, authority.Target.Environment)
	if err := revalidator.Revalidate(t.Context(), authority); !errors.Is(err, jobs.ErrAuthorityInvalid) {
		t.Fatalf("broadened issued-ceiling error = %v, want authority invalid", err)
	}
}
