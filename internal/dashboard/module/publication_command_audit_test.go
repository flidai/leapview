package module

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
)

func TestDashboardPublicationCommandsAuditExactlyOnceAndPreserveBestEffortResult(t *testing.T) {
	operations := []struct {
		name        string
		operationID string
		action      publication.Action
		invoke      func(*Module, http.ResponseWriter, *http.Request)
	}{
		{
			name: "suspend", operationID: dashboardgen.GenOperationSuspendDashboardPublication, action: publication.ActionSuspend,
			invoke: func(module *Module, w http.ResponseWriter, r *http.Request) {
				module.SuspendDashboardPublication(w, r, "sales", "executive")
			},
		},
		{
			name: "resume", operationID: dashboardgen.GenOperationResumeDashboardPublication, action: publication.ActionResume,
			invoke: func(module *Module, w http.ResponseWriter, r *http.Request) {
				module.ResumeDashboardPublication(w, r, "sales", "executive")
			},
		},
		{
			name: "rotate", operationID: dashboardgen.GenOperationRotateDashboardPublication, action: publication.ActionRotate,
			invoke: func(module *Module, w http.ResponseWriter, r *http.Request) {
				module.RotateDashboardPublication(w, r, "sales", "executive")
			},
		},
	}

	for _, operation := range operations {
		for _, auditFailure := range []bool{false, true} {
			name := operation.name + "/success"
			if auditFailure {
				name = operation.name + "/audit-failure"
			}
			t.Run(name, func(t *testing.T) {
				var logs bytes.Buffer
				repository := &publicationCommandRepository{row: publication.Publication{
					ID: "publication-a", WorkspaceID: "sales", Name: "executive", ProjectID: "analytics", Dashboard: "executive",
					PublicID: "public-a", Configured: true, ServingStateID: "state-a",
				}}
				attempts := 0
				var persisted []access.AuditEventInput
				recorder, err := buildPublicationCommandAuditRecorder(func(_ context.Context, input access.AuditEventInput) error {
					attempts++
					if auditFailure {
						return errors.New("audit store unavailable")
					}
					persisted = append(persisted, input)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				module := &Module{
					publicationService:            publication.NewService(repository, nil),
					currentActor:                  func(*http.Request) string { return "principal-a" },
					recordPublicationCommandAudit: recorder,
					logger:                        slog.New(slog.NewJSONHandler(&logs, nil)),
				}
				request := httptest.NewRequest(http.MethodPost, "/", nil)
				request.Header.Set("X-Request-ID", "request-a")
				request.Header.Set("X-Correlation-ID", "correlation-a")
				request.Header.Set("X-LeapView-Invocation-Surface", "cli")
				response := httptest.NewRecorder()
				operation.invoke(module, response, request)

				wantStatus, wantPersisted := http.StatusOK, 1
				if auditFailure {
					wantPersisted = 0
				}
				if response.Code != wantStatus || repository.calls != 1 || repository.action != operation.action || attempts != 1 || len(persisted) != wantPersisted {
					t.Fatalf("status=%d mutation=%d/%s audit attempts=%d persisted=%d body=%s", response.Code, repository.calls, repository.action, attempts, len(persisted), response.Body.String())
				}
				if !auditFailure {
					generated, _ := dashboardgen.GetAPIGenOperationContract(operation.operationID)
					event := persisted[0]
					if event.Action != generated.Command.Audit.SuccessAction || event.Privilege != access.Privilege(generated.Command.Privilege) ||
						event.WorkspaceID != "sales" || event.PrincipalID != "principal-a" || event.TargetType != "dashboard_publication" || event.TargetID != "publication-a" ||
						event.Status != "success" || event.RequestID != "request-a" || event.CorrelationID != "correlation-a" {
						t.Fatalf("audit event = %#v", event)
					}
				} else if output := logs.String(); !strings.Contains(output, "best-effort dashboard publication command audit failed") ||
					!strings.Contains(output, operation.operationID) || !strings.Contains(output, "publication-a") {
					t.Fatalf("audit failure log = %s", output)
				}
			})
		}
	}
}

func TestDashboardPublicationCommandsRejectMissingAuditSinkBeforeMutation(t *testing.T) {
	repository := &publicationCommandRepository{row: publication.Publication{ID: "publication-a"}}
	module := &Module{publicationService: publication.NewService(repository, nil)}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	module.SuspendDashboardPublication(response, request, "sales", "executive")
	if response.Code != http.StatusServiceUnavailable || repository.calls != 0 {
		t.Fatalf("status=%d mutations=%d body=%s", response.Code, repository.calls, response.Body.String())
	}
}

func TestDashboardPublicationUIInvocationUsesGeneratedExposureAndRequestIdentity(t *testing.T) {
	repository := &publicationCommandRepository{row: publication.Publication{
		ID: "publication-ui", WorkspaceID: "sales", Name: "executive", Configured: true,
	}}
	var persisted []access.AuditEventInput
	recorder, err := buildPublicationCommandAuditRecorder(func(_ context.Context, input access.AuditEventInput) error {
		persisted = append(persisted, input)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	module := &Module{publicationService: publication.NewService(repository, nil), recordPublicationCommandAudit: recorder}

	_, err = module.MutatePublicationWithInvocation(context.Background(), "sales", "executive", "principal-ui", publication.ActionSuspend, publication.CommandInvocation{
		Surface:        string(apigencommand.SurfaceUI),
		IdempotencyKey: "ui-request-1", RequestID: "ui-request-1", CorrelationID: "ui-correlation-1",
	})
	if err != nil || repository.calls != 1 || len(persisted) != 1 {
		t.Fatalf("ui mutation err=%v calls=%d audits=%d", err, repository.calls, len(persisted))
	}
	if persisted[0].RequestID != "ui-request-1" || persisted[0].CorrelationID != "ui-correlation-1" {
		t.Fatalf("ui audit identity = %#v", persisted[0])
	}

	_, err = module.MutatePublicationWithInvocation(context.Background(), "sales", "executive", "principal-ui", publication.ActionSuspend, publication.CommandInvocation{
		Surface: string(apigencommand.SurfaceUI),
	})
	if !errors.Is(err, apigencommand.ErrIdempotencyRequired) || repository.calls != 1 {
		t.Fatalf("missing UI idempotency err=%v calls=%d", err, repository.calls)
	}
}

func TestDashboardPublicationCommandAuditRejectsMissingSink(t *testing.T) {
	if recorder, err := buildPublicationCommandAuditRecorder(nil); !errors.Is(err, errPublicationCommandAuditUnavailable) || recorder != nil {
		t.Fatalf("recorder nil = %t, err = %v", recorder == nil, err)
	}
}

func TestDashboardBuildRejectsMissingCommandAuditSinkWhenPublicationsEnabled(t *testing.T) {
	if module, err := Build(t.Context(), Config{Database: new(sql.DB)}); !errors.Is(err, errPublicationCommandAuditUnavailable) || module != nil {
		t.Fatalf("module = %v, err = %v", module, err)
	}
}

type publicationCommandRepository struct {
	row    publication.Publication
	calls  int
	action publication.Action
}

func (r *publicationCommandRepository) GetByPublicID(context.Context, string) (publication.Publication, error) {
	return r.row, nil
}

func (r *publicationCommandRepository) Suspend(context.Context, string, string, string) (publication.Publication, error) {
	r.calls++
	r.action = publication.ActionSuspend
	return r.row, nil
}

func (r *publicationCommandRepository) Resume(context.Context, string, string, string) (publication.Publication, error) {
	r.calls++
	r.action = publication.ActionResume
	return r.row, nil
}

func (r *publicationCommandRepository) Rotate(context.Context, string, string, string) (publication.Publication, error) {
	r.calls++
	r.action = publication.ActionRotate
	return r.row, nil
}
