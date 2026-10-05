package composectl

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestQualificationRecoveryUploadGrantIsExactReadAndUploadForSampleConnection(t *testing.T) {
	const projectID, principalID = "project:evaluation", "principal:author"
	grant, err := qualificationRecoveryUploadGrant(projectID, principalID)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ID != qualificationRecoveryUploadGrantID || grant.Subject.Kind != access.SubjectKindPrincipal || grant.Subject.ID != principalID ||
		grant.Resource.Kind() != "connection" || grant.Resource.ID() != qualificationManagedConnectionID ||
		grant.PermissionProfile != access.PermissionCatalogProfile || grant.Capability != "" {
		t.Fatalf("recovery upload grant identity = %+v", grant)
	}
	if len(grant.Permissions) != 2 {
		t.Fatalf("recovery upload permissions = %+v, want exactly read and upload", grant.Permissions)
	}
	wantActions := []access.Action{access.ActionConnectionRead, access.ActionConnectionUpload}
	for index, permission := range grant.Permissions {
		if permission.Action != wantActions[index] || permission.Target.ProjectID != projectID ||
			permission.Target.ResourceID != qualificationManagedConnectionID ||
			permission.Target.ResourceKind != "connection" || permission.Target.IncludeFuture {
			t.Errorf("permission[%d] = %+v, want exact %s on %s/%s", index, permission, wantActions[index], projectID, qualificationManagedConnectionID)
		}
	}
}

func TestQualificationRecoveryUploadGrantIsPartOfApprovedPolicyDigest(t *testing.T) {
	const projectID, principalID = "project:evaluation", "principal:author"
	pipelineGrant, err := qualificationPipelineRunGrant(projectID, principalID)
	if err != nil {
		t.Fatal(err)
	}
	uploadGrant, err := qualificationRecoveryUploadGrant(projectID, principalID)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := access.NewTypedRoleBinding(
		"qualification-admin", "", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID},
		access.PermissionRoleProjectAdmin, projectgraph.ResourceID(projectID),
	)
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target:evaluation", ProjectID: projectID, Environment: "evaluation"}
	withBothGrants, err := access.AuthorizationPolicyDigest(scope, []access.RoleBinding{admin}, pipelineGrant, uploadGrant)
	if err != nil {
		t.Fatal(err)
	}
	withPipelineOnly, err := access.AuthorizationPolicyDigest(scope, []access.RoleBinding{admin}, pipelineGrant)
	if err != nil {
		t.Fatal(err)
	}
	if withBothGrants == withPipelineOnly {
		t.Fatal("upload grant did not change canonical authorization policy digest")
	}
	policy := qualificationRoleBindingListResponse{
		Items: []qualificationRoleBindingResponse{{
			ID: admin.ID, SubjectType: string(admin.Subject.Kind), SubjectID: admin.Subject.ID,
			Role: string(admin.PermissionRole), PermissionProfile: admin.PermissionProfile,
			Permissions: admin.Permissions, PolicyRevision: 7, PolicyDigest: withBothGrants,
		}},
		TargetID: scope.TargetID, ProjectID: projectID, Environment: scope.Environment,
		PolicyRevision: 7, PolicyDigest: withBothGrants,
	}
	if _, err := validateQualificationRoleBindingPolicy(policy, projectID, scope.Environment, pipelineGrant, uploadGrant); err != nil {
		t.Fatalf("canonical policy with both durable grants rejected: %v", err)
	}
	if _, err := validateQualificationRoleBindingPolicy(policy, projectID, scope.Environment, pipelineGrant); err == nil {
		t.Fatal("policy digest unexpectedly validated without the exact upload grant")
	}
}

func TestQualificationRecoveryDataSyncUsesDedicatedUploadBearer(t *testing.T) {
	options := qualificationRecoveryOptions{
		PublisherToken:      "publisher-secret",
		RecoveryUploadToken: qualificationRecoveryUploadToken("recovery-upload-secret"),
		ProjectID:           "project:evaluation",
	}
	spec := qualificationManagedUploadSyncCommand(options)
	if spec.token != "recovery-upload-secret" || spec.token == options.PublisherToken {
		t.Fatalf("managed data sync token = %q, want dedicated recovery upload credential", spec.token)
	}
	arguments := qualificationClientExecArgumentsWithEnv(
		"recovery-client", spec.token, "https://localhost:43127", spec.environment, spec.arguments...,
	)
	if !strings.Contains(strings.Join(arguments, "\x00"), "LEAPVIEW_API_TOKEN=recovery-upload-secret") ||
		strings.Contains(strings.Join(arguments, "\x00"), options.PublisherToken) {
		t.Fatalf("managed data sync command did not inject only the recovery upload bearer: %v", arguments)
	}
	if !strings.Contains(strings.Join(spec.arguments, "\x00"), "leapview\x00data\x00sync") {
		t.Fatalf("recovery upload credential is not scoped to the managed data sync command: %v", spec.arguments)
	}
}

func TestQualificationManagedUploadListSessionAndEventsUseConnectionReadBearer(t *testing.T) {
	const (
		readToken      = "connection-read-secret"
		publisherToken = "publisher-secret"
		uploadToken    = "recovery-upload-secret"
		projectID      = "project:evaluation"
		sessionID      = "session-7"
	)
	basePath := qualificationManagedConnectionPath(projectID)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Errorf("request method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+readToken || got == "Bearer "+publisherToken || got == "Bearer "+uploadToken {
			t.Errorf("Authorization = %q, want dedicated connection read bearer", got)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case basePath + "/upload-sessions":
			if r.URL.Query().Get("limit") != "100" {
				t.Errorf("upload session list limit = %q, want 100", r.URL.Query().Get("limit"))
			}
			_, _ = io.WriteString(w, `{"items":[{"id":"session-7","status":"open","files":[{"file":{"size":50000010},"negotiation":{"tus":{"offset":17}}}]}]}`)
		case basePath + "/upload-sessions/session-7":
			_, _ = io.WriteString(w, `{"status":"open","files":[{"negotiation":{"tus":{"offset":17}}}]}`)
		case basePath + "/upload-sessions/session-7/events":
			_, _ = io.WriteString(w, `{"items":[{"event":"upload_session.created"},{"event":"upload_session.finalizing"},{"event":"upload_session.completed"}]}`)
		default:
			t.Errorf("unexpected upload evidence path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	options := qualificationRecoveryOptions{
		ProjectID:               projectID,
		PublisherToken:          publisherToken,
		ConnectionEvidenceToken: qualificationConnectionEvidenceToken(readToken),
		RecoveryUploadToken:     qualificationRecoveryUploadToken(uploadToken),
	}

	gotID, gotOffset, err := qualificationManagedUploadPartialOffset(
		t.Context(), server.Client(), server.URL, options,
	)
	if err != nil || gotID != sessionID || gotOffset != 17 {
		t.Fatalf("partial upload list = (%q, %d, %v), want (%q, 17, nil)", gotID, gotOffset, err, sessionID)
	}
	session, err := readQualificationManagedUploadSessionStatus(
		t.Context(), server.Client(), server.URL, options, sessionID,
	)
	if err != nil || session.Status != "open" || session.Files[0].Negotiation.TUS.Offset != 17 {
		t.Fatalf("session status = (%+v, %v), want open session at offset 17", session, err)
	}
	if _, err := waitForQualificationManagedUploadEvents(
		t.Context(), server.Client(), server.URL, options, sessionID,
	); err != nil {
		t.Fatalf("managed upload events: %v", err)
	}
	if requests != 3 {
		t.Fatalf("managed upload read requests = %d, want list, session, and events GETs", requests)
	}
}

func TestReadQualificationRecoveryOwnerGrantsRequiresExactPipelineAndUploadSet(t *testing.T) {
	const (
		projectID   = "project:evaluation"
		environment = "evaluation"
		principalID = "principal:author"
		targetID    = "target:evaluation"
		revision    = int64(7)
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	pipelineGrant, err := qualificationPipelineRunGrant(projectID, principalID)
	if err != nil {
		t.Fatal(err)
	}
	uploadGrant, err := qualificationRecoveryUploadGrant(projectID, principalID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{
		"valid", "missing upload action", "extra upload action", "wrong upload resource",
		"wrong recipient", "stale grant", "wrong grant digest", "duplicate grant ID",
		"extra grant", "paginated", "wrong project", "wrong environment", "wrong target",
		"wrong policy digest", "wrong bearer",
	} {
		t.Run(mode, func(t *testing.T) {
			items := []map[string]any{
				qualificationOwnerGrantItem(pipelineGrant, revision, digest),
				qualificationOwnerGrantItem(uploadGrant, revision, digest),
			}
			response := map[string]any{
				"items": items, "targetId": targetID, "projectId": projectID,
				"environment": environment, "policyRevision": revision,
				"policyDigest": digest, "page": map[string]string{},
			}
			switch mode {
			case "missing upload action":
				items[1]["permissions"] = uploadGrant.Permissions[:1]
			case "extra upload action":
				permissions := append([]access.PermissionPair(nil), uploadGrant.Permissions...)
				extra := permissions[0]
				extra.Action = access.ActionConnectionManage
				permissions = append(permissions, extra)
				items[1]["permissions"] = permissions
			case "wrong upload resource":
				permissions := append([]access.PermissionPair(nil), uploadGrant.Permissions...)
				permissions[1].Target.ResourceID = "connection:other"
				items[1]["permissions"] = permissions
			case "wrong recipient":
				items[1]["subjectId"] = "principal:other"
			case "stale grant":
				items[1]["policyRevision"] = revision - 1
			case "wrong grant digest":
				items[1]["policyDigest"] = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			case "duplicate grant ID":
				items[1]["id"] = pipelineGrant.ID
			case "extra grant":
				extra := qualificationOwnerGrantItem(uploadGrant, revision, digest)
				extra["id"] = "unexpected-grant"
				items = append(items, extra)
				response["items"] = items
			case "paginated":
				response["page"] = map[string]string{"nextCursor": "more"}
			case "wrong project":
				response["projectId"] = "project:other"
			case "wrong environment":
				response["environment"] = "production"
			case "wrong target":
				response["targetId"] = "target:other"
			case "wrong policy digest":
				response["policyDigest"] = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/"+projectID+"/grants" || r.URL.Query().Get("limit") != "200" {
					t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
				}
				if r.Header.Get("Authorization") != "Bearer policy-reader" || mode == "wrong bearer" {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			err := readQualificationRecoveryOwnerGrants(
				t.Context(), server.Client(), server.URL, projectID, environment,
				"policy-reader", targetID, revision, digest, pipelineGrant, uploadGrant,
			)
			if mode == "valid" {
				if err != nil {
					t.Fatalf("valid exact owner grants rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("accepted changed recovery owner grant evidence")
			}
		})
	}
}

func qualificationOwnerGrantItem(grant access.AuthorizationGrant, revision int64, digest string) map[string]any {
	return map[string]any{
		"id": grant.ID, "name": grant.Name,
		"subjectType": string(grant.Subject.Kind), "subjectId": grant.Subject.ID,
		"resourceKind": string(grant.Resource.Kind()), "resourceId": string(grant.Resource.ID()),
		"capability": string(grant.Capability), "permissionProfile": grant.PermissionProfile,
		"permissions": grant.Permissions, "policyRevision": revision, "policyDigest": digest,
	}
}
