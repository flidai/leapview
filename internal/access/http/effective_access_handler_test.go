package http

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestListEffectiveCapabilitiesRetainsDirectAndInheritedEvidence(t *testing.T) {
	handler := Handler{
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal_alice", Kind: access.PrincipalKindUser}, true
		},
		EffectiveAccess: func(context.Context, string) ([]access.AuthorizationDecision, error) {
			return []access.AuthorizationDecision{
				{Allowed: true, Capability: access.CapabilityResourceRead, Reason: "direct grant", ResourceKind: "dashboard", ResourceID: "dashboard_sales", GrantID: "grant-direct", SubjectType: "principal", SubjectID: "principal_alice"},
				{Allowed: true, Capability: access.CapabilityResourceRead, Reason: "group-inherited grant", ResourceKind: "dashboard", ResourceID: "dashboard_sales", GrantID: "grant-group", SubjectType: "group", SubjectID: "group_sales", Inherited: true},
			}, nil
		},
	}
	request := withProjectRoute(httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project_demo/effective-capabilities?resourceKind=dashboard&resourceId=dashboard_sales", nil), "project_demo")
	response := httptest.NewRecorder()
	handler.ListEffectiveCapabilities(response, request)
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		ResourceID   string   `json:"resourceId"`
		Capabilities []string `json:"capabilities"`
		Evidence     []struct {
			GrantID   string `json:"grantId"`
			Inherited bool   `json:"inherited"`
			Reason    string `json:"reason"`
		} `json:"effectiveGrants"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ResourceID != "dashboard_sales" || len(body.Capabilities) != 1 || body.Capabilities[0] != string(access.CapabilityResourceRead) {
		t.Fatalf("projection=%#v", body)
	}
	if len(body.Evidence) != 2 || body.Evidence[0].GrantID != "grant-direct" || body.Evidence[0].Inherited || body.Evidence[1].GrantID != "grant-group" || !body.Evidence[1].Inherited {
		t.Fatalf("evidence=%#v", body.Evidence)
	}
}

func TestCheckAuthorizationBatchReturnsAllowedAndDeniedReasons(t *testing.T) {
	handler := Handler{
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal_alice", Kind: access.PrincipalKindUser}, true
		},
		EffectiveAccess: func(context.Context, string) ([]access.AuthorizationDecision, error) {
			return []access.AuthorizationDecision{{
				Allowed: true, Capability: access.CapabilityResourceRead,
				Reason: "direct grant", ResourceKind: "dashboard", ResourceID: "dashboard_sales",
				GrantID: "grant-direct", SubjectType: "principal", SubjectID: "principal_alice",
			}}, nil
		},
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/project_demo/authorization-checks", bytes.NewBufferString(`{"checks":[{"resourceKind":"dashboard","resourceId":"dashboard_sales","capability":"RESOURCE_READ"},{"resourceKind":"dashboard","resourceId":"dashboard_sales","capability":"RESOURCE_EDIT"}]}`))
	response := httptest.NewRecorder()
	handler.CheckAuthorizationBatch(response, request)
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Decisions []struct {
			Allowed bool   `json:"allowed"`
			GrantID string `json:"grantId"`
			Reason  string `json:"reason"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Decisions) != 2 || !body.Decisions[0].Allowed || body.Decisions[0].GrantID != "grant-direct" || body.Decisions[1].Allowed || body.Decisions[1].Reason != "no direct, inherited, owner, or platform authority" {
		t.Fatalf("decisions=%#v", body.Decisions)
	}
}

func TestCheckAuthorizationBatchHonorsRestrictedToken(t *testing.T) {
	handler := Handler{
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal_alice", Kind: access.PrincipalKindUser}, true
		},
		CurrentCredential: func(*stdhttp.Request) (access.APICredential, bool) {
			return access.APICredential{
				Principal: access.Principal{ID: "principal_alice"},
				Token:     access.APIToken{ID: "restricted", PrincipalID: "principal_alice", Capabilities: []access.Capability{access.CapabilityResourceRead}},
			}, true
		},
		EffectiveAccess: func(context.Context, string) ([]access.AuthorizationDecision, error) {
			return []access.AuthorizationDecision{{Allowed: true, Capability: access.CapabilityResourceEdit, ResourceKind: "dashboard", ResourceID: "dashboard_sales", Reason: "direct role binding"}}, nil
		},
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/project_demo/authorization-checks", bytes.NewBufferString(`{"checks":[{"resourceKind":"dashboard","resourceId":"dashboard_sales","capability":"RESOURCE_EDIT"}]}`))
	response := httptest.NewRecorder()
	handler.CheckAuthorizationBatch(response, request)
	var body struct {
		Decisions []struct {
			Allowed bool   `json:"allowed"`
			Reason  string `json:"reason"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != stdhttp.StatusOK || len(body.Decisions) != 1 || body.Decisions[0].Allowed || body.Decisions[0].Reason != "credential does not grant requested capability" {
		t.Fatalf("restricted token decision: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWriteOffboardingErrorPreservesTypedConflictAndObjectEvidence(t *testing.T) {
	response := httptest.NewRecorder()
	err := &access.OwnershipConflictError{Report: access.OwnershipReport{
		PrincipalID: "principal_alice",
		Objects:     []access.OwnedObject{{Kind: "semantic_attribute", ID: "attribute_region", Name: "region", OwnerPrincipalID: "principal_alice", Lifecycle: "disabled", Transferable: true}},
	}}
	if !writeOffboardingError(response, err, "PRINCIPAL_OWNS_OBJECTS") {
		t.Fatal("ownership error was not handled")
	}
	if response.Code != stdhttp.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Code        string               `json:"code"`
		PrincipalID string               `json:"principalId"`
		Objects     []access.OwnedObject `json:"ownedObjects"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "PRINCIPAL_OWNS_OBJECTS" || body.PrincipalID != "principal_alice" || len(body.Objects) != 1 || body.Objects[0].Lifecycle != "disabled" {
		t.Fatalf("conflict body=%#v", body)
	}
}
