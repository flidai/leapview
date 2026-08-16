package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/manageddata"
	apigenapi "github.com/flidai/leapview/internal/manageddata/api"
	"github.com/flidai/leapview/internal/manageddata/control"
	managedhttp "github.com/flidai/leapview/internal/manageddata/http"
	"github.com/flidai/leapview/internal/manageddata/s3multipart"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	digestA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	digestC   = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	revisionA = "sha256:" + digestA
	revisionB = "sha256:" + digestB
	revisionC = "sha256:" + digestC
)

func TestS3MultipartServiceWiresDirectlyIntoHTTPHandler(t *testing.T) {
	_ = managedhttp.Options{Multipart: (*s3multipart.Service)(nil)}
}

func TestRevisionOperationsAreScopedAndPaginated(t *testing.T) {
	repo := metadataFixture()
	handler := newHandler(repo, nil, nil)

	recorder := call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.GetActiveManagedDataRevision(w, r, "project-a", "orders")
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("environment status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var current apigenapi.ManagedDataActiveRevisionResponse
	decodeResponse(t, recorder, &current)
	if current.Revision == nil || current.Revision.Id != revisionA || current.Revision.UploadSessionId != "upload-a" || current.DeploymentId == nil || *current.DeploymentId != "deployment-a" {
		t.Fatalf("environment response = %#v", current)
	}

	limit := int32(1)
	recorder = call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.ListManagedDataRevisions(w, r, "project-a", "orders", apigenapi.GenListManagedDataRevisionsParams{Limit: &limit})
	})
	var first apigenapi.ManagedDataRevisionListResponse
	decodeResponse(t, recorder, &first)
	if len(first.Items) != 1 || first.Items[0].Id != revisionB || first.Page.NextCursor == nil || *first.Page.NextCursor == "" {
		t.Fatalf("first revision page = %#v", first)
	}
	recorder = call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.ListManagedDataRevisions(w, r, "project-a", "orders", apigenapi.GenListManagedDataRevisionsParams{Limit: &limit, PageToken: first.Page.NextCursor})
	})
	var second apigenapi.ManagedDataRevisionListResponse
	decodeResponse(t, recorder, &second)
	if len(second.Items) != 1 || second.Items[0].Id != revisionA {
		t.Fatalf("second revision page = %#v", second)
	}

	recorder = call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.GetManagedDataRevision(w, r, "project-a", "orders", revisionA)
	})
	var revision apigenapi.ManagedDataRevisionResponse
	decodeResponse(t, recorder, &revision)
	if revision.Id != revisionA || len(revision.Manifest.Files) != 1 || revision.Manifest.Files[0].Path != "orders.csv" {
		t.Fatalf("revision response = %#v", revision)
	}

	recorder = call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.GetManagedDataRevision(w, r, "project-a", "orders", revisionC)
	})
	assertPublicError(t, recorder, http.StatusNotFound, "unrelated-secret")
}

func TestRevisionResponseRejectsMissingPublicID(t *testing.T) {
	repo := metadataFixture()
	missingID := "sha256:" + strings.Repeat("d", 64)
	row := repo.revisions[revisionA]
	row.PublicID = ""
	row.Revision.Digest = missingID
	repo.revisions[missingID] = row
	handler := newHandler(repo, nil, nil)

	recorder := call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.GetManagedDataRevision(w, r, "project-a", "orders", missingID)
	})
	assertPublicError(t, recorder, http.StatusInternalServerError, "invalid revision metadata")
}

func TestUploadSessionOperationsUseControlServiceAndPrincipal(t *testing.T) {
	uploads := &fakeUploads{result: uploadFixture()}
	handler := newHandler(metadataFixture(), uploads, nil)

	created := call(t, `{"manifest":{"files":[{"path":"orders.csv","size":3,"sha256":"`+digestA+`"}]}}`, func(w http.ResponseWriter, r *http.Request) {
		handler.CreateManagedDataUploadSession(w, r, "project-a", "orders", apigenapi.GenCreateManagedDataUploadSessionHeaders{IdempotencyKey: "create-key"})
	})
	if created.Code != http.StatusCreated || uploads.begin.Actor != "principal-a" || uploads.begin.IdempotencyKey != "create-key" {
		t.Fatalf("create = %d %s, request = %#v", created.Code, created.Body.String(), uploads.begin)
	}
	var session apigenapi.ManagedDataUploadSessionResponse
	decodeResponse(t, created, &session)
	if session.RevisionId != uploads.result.Manifest.RevisionID() || session.Files[0].Negotiation.Tus == nil || session.Files[0].Negotiation.Tus.Endpoint != "/upload-protocols/tus" {
		t.Fatalf("created session = %#v", session)
	}

	tests := []struct {
		name string
		want int
		call func(http.ResponseWriter, *http.Request)
	}{
		{"get", http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
			handler.GetManagedDataUploadSession(w, r, "project-a", "orders", "upload-a")
		}},
		{"cancel", http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
			handler.CancelManagedDataUploadSession(w, r, "project-a", "orders", "upload-a", apigenapi.GenCancelManagedDataUploadSessionHeaders{IdempotencyKey: "cancel-key"})
		}},
		{"finalize", http.StatusAccepted, func(w http.ResponseWriter, r *http.Request) {
			handler.FinalizeManagedDataUploadSession(w, r, "project-a", "orders", "upload-a", apigenapi.GenFinalizeManagedDataUploadSessionHeaders{IdempotencyKey: "finalize-key"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := call(t, ``, test.call)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if uploads.recoverCalls != 1 || uploads.abortCalls != 1 || uploads.finalizeCalls != 1 {
		t.Fatalf("upload calls = recover %d, abort %d, finalize %d", uploads.recoverCalls, uploads.abortCalls, uploads.finalizeCalls)
	}
}

func TestDevelopmentBypassOnlySkipsConnectionSnapshotAuthorization(t *testing.T) {
	deny := func(context.Context, string, string, string, access.Capability) (bool, error) { return false, nil }
	for _, test := range []struct {
		name       string
		principal  managedhttp.Principal
		wantStatus int
	}{
		{name: "development bypass", principal: managedhttp.Principal{ID: "dev", DevBypass: true}, wantStatus: http.StatusOK},
		{name: "ordinary principal denied", principal: managedhttp.Principal{ID: "principal-a"}, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := managedhttp.NewHandler(managedhttp.Options{
				Uploads: &fakeUploads{result: uploadFixture()}, Environment: "prod",
				CurrentPrincipal:    func(*http.Request) (managedhttp.Principal, bool) { return test.principal, true },
				AuthorizeConnection: deny,
			})
			recorder := call(t, "", func(w http.ResponseWriter, r *http.Request) {
				handler.GetManagedDataUploadSession(w, r, "project-a", "orders", "upload-a")
			})
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestUploadSessionsAreListedFromCollectionMetadata(t *testing.T) {
	repo := metadataFixture()
	repo.uploadSessions = []manageddata.UploadSession{{ID: "upload-a", CollectionID: "collection-a", CreatedAt: "2026-01-01T00:00:00Z"}}
	uploads := &fakeUploads{result: uploadFixture()}
	handler := newHandler(repo, uploads, nil)

	recorder := call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.ListManagedDataUploadSessions(w, r, "project-a", "orders", apigenapi.GenListManagedDataUploadSessionsParams{})
	})
	var response apigenapi.ManagedDataUploadSessionListResponse
	decodeResponse(t, recorder, &response)
	if len(response.Items) != 1 || response.Items[0].Id != "upload-a" || uploads.recoverCalls != 1 {
		t.Fatalf("upload sessions = %#v, recover calls = %d", response.Items, uploads.recoverCalls)
	}
}

func TestCancelledUploadSessionResponseDoesNotRequireActiveNegotiation(t *testing.T) {
	repo := metadataFixture()
	repo.uploadSessions = []manageddata.UploadSession{{ID: "upload-a", CollectionID: "collection-a", CreatedAt: "2026-01-01T00:00:00Z"}}
	result := uploadFixture()
	result.Status = manageddata.UploadStatusAborted
	result.Files[0].Transport = control.TransportDescription{}
	handler := newHandler(repo, &fakeUploads{result: result}, nil)

	recorder := call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.ListManagedDataUploadSessions(w, r, "project-a", "orders", apigenapi.GenListManagedDataUploadSessionsParams{})
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	decodeResponse(t, recorder, &response)
	items := response["items"].([]any)
	files := items[0].(map[string]any)["files"].([]any)
	file := files[0].(map[string]any)
	if _, present := file["negotiation"]; present {
		t.Fatalf("terminal file retained upload negotiation: %#v", file)
	}
}

func TestMultipartOperationsAreSDKFreeAndScopedToUpload(t *testing.T) {
	uploadResult := uploadFixture()
	uploadResult.Files[0].Transport = control.TransportDescription{Protocol: control.ProtocolS3Multipart, S3Multipart: &control.S3MultipartDescription{CreateEndpoint: "/multipart", MinimumPartSize: 1, MaximumPartSize: 1024, MaximumParts: 100}}
	uploads := &fakeUploads{result: uploadResult}
	multipart := &fakeMultipart{upload: s3multipart.UploadResult{
		ID: "multipart-a", UploadSessionID: "upload-a", File: manageddata.File{Path: "orders.csv", Size: 3, SHA256: digestA},
		Status: s3multipart.StatusOpen, CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-01T01:00:00Z",
	}}
	handler := newHandler(metadataFixture(), uploads, multipart)

	tests := []struct {
		name string
		body string
		want int
		call func(http.ResponseWriter, *http.Request)
	}{
		{"create", `{"path":"orders.csv"}`, http.StatusCreated, func(w http.ResponseWriter, r *http.Request) {
			handler.CreateManagedDataS3MultipartUpload(w, r, "project-a", "orders", "upload-a", apigenapi.GenCreateManagedDataS3MultipartUploadHeaders{IdempotencyKey: "create-key"})
		}},
		{"sign", `{"size":3,"sha256":"` + digestA + `"}`, http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
			handler.SignManagedDataS3MultipartPart(w, r, "project-a", "orders", "upload-a", "multipart-a", 1)
		}},
		{"complete", `{"parts":[{"partNumber":1,"etag":"etag-a","sha256":"` + digestA + `"}]}`, http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
			handler.CompleteManagedDataS3MultipartUpload(w, r, "project-a", "orders", "upload-a", "multipart-a", apigenapi.GenCompleteManagedDataS3MultipartUploadHeaders{IdempotencyKey: "complete-key"})
		}},
		{"abort", ``, http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
			handler.AbortManagedDataS3MultipartUpload(w, r, "project-a", "orders", "upload-a", "multipart-a", apigenapi.GenAbortManagedDataS3MultipartUploadHeaders{IdempotencyKey: "abort-key"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := call(t, test.body, test.call)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if multipart.create.Path != "orders.csv" || multipart.sign.PartNumber != 1 || multipart.complete.Parts[0].ETag != "etag-a" {
		t.Fatalf("multipart requests = create %#v sign %#v complete %#v", multipart.create, multipart.sign, multipart.complete)
	}

	var signed apigenapi.ManagedDataS3MultipartSignedPartResponse
	signedRecorder := call(t, `{"size":3}`, func(w http.ResponseWriter, r *http.Request) {
		handler.SignManagedDataS3MultipartPart(w, r, "project-a", "orders", "upload-a", "multipart-a", 2)
	})
	decodeResponse(t, signedRecorder, &signed)
	if signed.Url != "https://signed.example/part" || len(signed.Headers) != 1 || signed.Headers[0].Name != "x-checksum" {
		t.Fatalf("signed response = %#v", signed)
	}
}

func TestStrictDecodingErrorMappingAndSanitization(t *testing.T) {
	t.Run("unknown JSON field", func(t *testing.T) {
		handler := newHandler(metadataFixture(), &fakeUploads{result: uploadFixture()}, nil)
		recorder := call(t, `{"manifest":{"files":[]},"secret":"credential"}`, func(w http.ResponseWriter, r *http.Request) {
			handler.CreateManagedDataUploadSession(w, r, "project-a", "orders", apigenapi.GenCreateManagedDataUploadSessionHeaders{IdempotencyKey: "key"})
		})
		assertPublicError(t, recorder, http.StatusBadRequest, "credential")
	})

	t.Run("oversized JSON", func(t *testing.T) {
		options := handlerOptions(metadataFixture(), &fakeUploads{result: uploadFixture()}, nil)
		options.MaxJSONBodyBytes = 32
		handler := managedhttp.NewHandler(options)
		recorder := call(t, `{"manifest":{"files":[{"path":"orders.csv"}]}}`, func(w http.ResponseWriter, r *http.Request) {
			handler.CreateManagedDataUploadSession(w, r, "project-a", "orders", apigenapi.GenCreateManagedDataUploadSessionHeaders{IdempotencyKey: "key"})
		})
		assertPublicError(t, recorder, http.StatusRequestEntityTooLarge, "orders.csv")
	})

	tests := []struct {
		err  error
		want int
	}{
		{managedhttp.ErrInvalid, http.StatusBadRequest},
		{manageddata.ErrNotFound, http.StatusNotFound},
		{control.ErrConflict, http.StatusConflict},
		{managedhttp.ErrTooLarge, http.StatusRequestEntityTooLarge},
		{errors.New("storage key s3://private credentials=secret signed=https://signed.example"), http.StatusInternalServerError},
		{control.ErrBackend, http.StatusBadGateway},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.want), func(t *testing.T) {
			repo := metadataFixture()
			repo.revisionErr = test.err
			handler := newHandler(repo, nil, nil)
			recorder := call(t, ``, func(w http.ResponseWriter, r *http.Request) {
				handler.GetManagedDataRevision(w, r, "project-a", "orders", revisionA)
			})
			assertPublicError(t, recorder, test.want, "secret")
		})
	}

	handler := newHandler(metadataFixture(), nil, nil)
	recorder := call(t, ``, func(w http.ResponseWriter, r *http.Request) {
		handler.CreateManagedDataS3MultipartUpload(w, r, "project-a", "orders", "upload-a", apigenapi.GenCreateManagedDataS3MultipartUploadHeaders{IdempotencyKey: "key"})
	})
	assertPublicError(t, recorder, http.StatusServiceUnavailable, "secret")
}

func TestMutationResponsesCannotCrossRequestedIDs(t *testing.T) {
	t.Run("upload session", func(t *testing.T) {
		uploads := &fakeUploads{result: uploadFixture()}
		uploads.result.ID = "upload-other"
		handler := newHandler(metadataFixture(), uploads, nil)
		recorder := call(t, ``, func(w http.ResponseWriter, r *http.Request) {
			handler.CancelManagedDataUploadSession(w, r, "project-a", "orders", "upload-a", apigenapi.GenCancelManagedDataUploadSessionHeaders{IdempotencyKey: "key"})
		})
		assertPublicError(t, recorder, http.StatusNotFound, "orders.csv")
	})

	t.Run("multipart upload", func(t *testing.T) {
		uploads := &fakeUploads{result: uploadFixture()}
		multipart := &fakeMultipart{upload: s3multipart.UploadResult{ID: "multipart-other", UploadSessionID: "upload-a", File: manageddata.File{Path: "orders.csv", Size: 3, SHA256: digestA}, Status: s3multipart.StatusCompleted, CreatedAt: "2026-01-01T00:00:00Z"}}
		handler := newHandler(metadataFixture(), uploads, multipart)
		recorder := call(t, `{"parts":[{"partNumber":1,"etag":"etag-a"}]}`, func(w http.ResponseWriter, r *http.Request) {
			handler.CompleteManagedDataS3MultipartUpload(w, r, "project-a", "orders", "upload-a", "multipart-a", apigenapi.GenCompleteManagedDataS3MultipartUploadHeaders{IdempotencyKey: "key"})
		})
		assertPublicError(t, recorder, http.StatusNotFound, "orders.csv")
	})

}

type fakeRepository struct {
	collection     manageddata.Collection
	revisions      map[string]managedhttp.RevisionMetadata
	uploadSessions []manageddata.UploadSession
	pointer        manageddata.EnvironmentPointer
	revisionErr    error
}

func metadataFixture() *fakeRepository {
	collection := manageddata.Collection{ID: projectgraph.ResourceID("collection-a"), ProjectID: projectgraph.ResourceID("project-a"), ConnectionID: projectgraph.ResourceID("orders"), Status: manageddata.CollectionStatusActive}
	return &fakeRepository{
		collection: collection,
		revisions: map[string]managedhttp.RevisionMetadata{
			revisionA: {Revision: manageddata.Revision{ID: manageddata.RevisionID("revision-a"), CollectionID: collection.ID, Status: manageddata.RevisionStatusReady, ManifestJSON: `{"files":[{"path":"orders.csv","size":3,"sha256":"` + digestA + `"}]}`, FileCount: 1, SizeBytes: 3, CreatedAt: "2026-01-01T00:00:00Z"}, PublicID: revisionA, UploadSessionID: "upload-a"},
			revisionB: {Revision: manageddata.Revision{ID: manageddata.RevisionID("revision-b"), CollectionID: collection.ID, Status: manageddata.RevisionStatusReady, ManifestJSON: `{"files":[{"path":"customers.csv","size":4,"sha256":"` + digestB + `"}]}`, FileCount: 1, SizeBytes: 4, CreatedAt: "2026-01-02T00:00:00Z"}, PublicID: revisionB, UploadSessionID: "upload-b"},
			revisionC: {Revision: manageddata.Revision{ID: manageddata.RevisionID("revision-c"), CollectionID: projectgraph.ResourceID("collection-other"), Status: manageddata.RevisionStatusReady, ManifestJSON: `{"files":[{"path":"unrelated-secret.csv","size":4,"sha256":"` + digestC + `"}]}`}, PublicID: revisionC, UploadSessionID: "upload-secret"},
		},
		pointer: manageddata.EnvironmentPointer{CollectionID: collection.ID, Environment: "prod", RevisionID: manageddata.RevisionID("revision-a"), RevisionDigest: revisionA, DeploymentID: "deployment-a", UpdatedAt: "2026-01-03T00:00:00Z"},
	}
}

func (r *fakeRepository) CollectionByProjectConnection(_ context.Context, project, connection string) (manageddata.Collection, error) {
	if project != r.collection.ProjectID.String() || connection != r.collection.ConnectionID.String() {
		return manageddata.Collection{}, manageddata.ErrNotFound
	}
	return r.collection, nil
}

func (r *fakeRepository) RevisionByID(_ context.Context, collectionID, id string) (managedhttp.RevisionMetadata, error) {
	if r.revisionErr != nil {
		return managedhttp.RevisionMetadata{}, r.revisionErr
	}
	revision, ok := r.revisions[id]
	if !ok || revision.Revision.CollectionID.String() != collectionID {
		return managedhttp.RevisionMetadata{}, manageddata.ErrNotFound
	}
	return revision, nil
}

func (r *fakeRepository) ListRevisions(context.Context, string) ([]managedhttp.RevisionMetadata, error) {
	return []managedhttp.RevisionMetadata{r.revisions[revisionA], r.revisions[revisionB]}, nil
}

func (r *fakeRepository) ListUploadSessions(context.Context, string) ([]manageddata.UploadSession, error) {
	return append([]manageddata.UploadSession(nil), r.uploadSessions...), nil
}

func (r *fakeRepository) EnvironmentPointer(context.Context, string, manageddata.Environment) (manageddata.EnvironmentPointer, error) {
	return r.pointer, nil
}

type fakeUploads struct {
	result        control.UploadResult
	begin         control.BeginUploadRequest
	recoverCalls  int
	abortCalls    int
	finalizeCalls int
}

func (u *fakeUploads) BeginUpload(_ context.Context, request control.BeginUploadRequest) (control.UploadResult, error) {
	u.begin = request
	return u.result, nil
}

func (u *fakeUploads) RecoverUpload(context.Context, control.UploadRequest) (control.UploadResult, error) {
	u.recoverCalls++
	return u.result, nil
}

func (u *fakeUploads) AbortUpload(context.Context, control.UploadRequest) (control.UploadResult, error) {
	u.abortCalls++
	return u.result, nil
}

func (u *fakeUploads) FinalizeUpload(context.Context, control.UploadRequest) (control.FinalizeResult, error) {
	u.finalizeCalls++
	return control.FinalizeResult{Upload: u.result}, nil
}

func (u *fakeUploads) BeginFinalizeUpload(context.Context, control.UploadRequest) (control.UploadResult, error) {
	u.finalizeCalls++
	result := u.result
	result.Status = manageddata.UploadStatusCommitting
	return result, nil
}

func (u *fakeUploads) CompleteFinalizeUpload(context.Context, control.UploadRequest) (control.FinalizeResult, error) {
	return control.FinalizeResult{Upload: u.result}, nil
}

func uploadFixture() control.UploadResult {
	file := manageddata.File{Path: "orders.csv", Size: 3, SHA256: digestA}
	return control.UploadResult{
		ID: "upload-a", RevisionID: "internal-revision-a", Collection: control.CollectionResult{ID: "collection-a", Project: "project-a", Connection: "orders"},
		Status: manageddata.UploadStatusOpen, Manifest: manageddata.Manifest{Files: []manageddata.File{file}}, CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-01T01:00:00Z",
		Files: []control.UploadFile{{File: file, Status: control.FileStatusPending, Transport: control.TransportDescription{Protocol: control.ProtocolTus, Tus: &control.TusDescription{Endpoint: "/upload-protocols/tus", UploadID: "tus-a", Offset: 0, ExpiresAt: "2026-01-01T01:00:00Z", Metadata: map[string]string{"secret": "do-not-return"}}}}},
	}
}

type fakeMultipart struct {
	upload   s3multipart.UploadResult
	create   s3multipart.CreateRequest
	sign     s3multipart.SignPartRequest
	complete s3multipart.CompleteRequest
	abort    s3multipart.AbortRequest
}

func (m *fakeMultipart) Create(_ context.Context, request s3multipart.CreateRequest) (s3multipart.UploadResult, error) {
	m.create = request
	return m.upload, nil
}

func (m *fakeMultipart) SignPart(_ context.Context, request s3multipart.SignPartRequest) (s3multipart.SignedPartResult, error) {
	m.sign = request
	return s3multipart.SignedPartResult{UploadSessionID: request.UploadSessionID, MultipartUploadID: request.MultipartUploadID, PartNumber: request.PartNumber, URL: "https://signed.example/part", Headers: []s3multipart.Header{{Name: "x-checksum", Value: "secret-signature"}}, ExpiresAt: "2026-01-01T00:15:00Z"}, nil
}

func (m *fakeMultipart) Complete(_ context.Context, request s3multipart.CompleteRequest) (s3multipart.UploadResult, error) {
	m.complete = request
	result := m.upload
	result.Status = s3multipart.StatusCompleted
	return result, nil
}

func (m *fakeMultipart) Abort(_ context.Context, request s3multipart.AbortRequest) (s3multipart.UploadResult, error) {
	m.abort = request
	result := m.upload
	result.Status = s3multipart.StatusAborted
	return result, nil
}

func newHandler(repo managedhttp.Repository, uploads managedhttp.UploadCoordinator, multipart s3multipart.Coordinator) *managedhttp.Handler {
	return managedhttp.NewHandler(handlerOptions(repo, uploads, multipart))
}

func handlerOptions(repo managedhttp.Repository, uploads managedhttp.UploadCoordinator, multipart s3multipart.Coordinator) managedhttp.Options {
	return managedhttp.Options{
		Repository: repo, Uploads: uploads, Multipart: multipart, Environment: "prod",
		AuthorizeConnection: func(context.Context, string, string, string, access.Capability) (bool, error) { return true, nil },
		EnqueueFinalize:     func(context.Context, control.UploadRequest) error { return nil },
		RecordCommandAudit:  func(context.Context, managedhttp.CommandAuditInput) error { return nil },
		CurrentPrincipal: func(*http.Request) (managedhttp.Principal, bool) {
			return managedhttp.Principal{ID: "principal-a"}, true
		},
	}
}

func call(t *testing.T, body string, invoke func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	invoke(recorder, request)
	return recorder
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if recorder.Code < 200 || recorder.Code >= 300 {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, recorder.Body.String())
	}
}

func assertPublicError(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, forbidden string) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), forbidden) || strings.Contains(recorder.Body.String(), "signed.example") || strings.Contains(recorder.Body.String(), "s3://") {
		t.Fatalf("error response leaked sensitive value: %s", recorder.Body.String())
	}
	var response apigenapi.ProblemDetails
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Status != int32(wantStatus) || response.Code == "" || response.Detail == "" {
		t.Fatalf("error response = %#v, error = %v", response, err)
	}
}
