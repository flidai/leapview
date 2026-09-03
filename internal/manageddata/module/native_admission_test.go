package module

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	jobspkg "github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// admissionDB only exercises module construction. Native repositories retain
// this DBTX and validate it when a lifecycle operation is actually invoked.
type admissionDB struct{}

func (admissionDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (admissionDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }

func (admissionDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type admissionWorkflow struct{}

func (admissionWorkflow) RecordWorkflow(context.Context, manageddatapostgres.Tx, jobspkg.WorkflowIntent) error {
	return nil
}

type admissionAudit struct{}

func (admissionAudit) RecordAuditIntent(context.Context, manageddatapostgres.Tx, access.AuditIntent) error {
	return nil
}

func TestBuildProductionNativePersistenceDoesNotRequireLegacyAuditRecorder(t *testing.T) {
	repository := manageddatapostgres.NewWithOptions(admissionDB{}, manageddatapostgres.Options{
		Workflow: admissionWorkflow{}, Audit: admissionAudit{},
	})
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	module, err := Build(t.Context(), Config{
		Persistence:  &persistence,
		Production:   true,
		CleanupAcker: manageddatapostgres.NewMaintenance(admissionDB{}),
		Product: ProductConfig{
			Backend:          "local",
			Dir:              t.TempDir(),
			UploadSessionTTL: time.Hour,
			GCGracePeriod:    time.Hour,
		},
	})
	if err != nil {
		t.Fatalf("native production build without generic recorder: %v", err)
	}
	if module == nil || module.HTTP() == nil {
		t.Fatal("native production build did not expose HTTP handler")
	}
}

func TestBuildProductionNativePersistenceRequiresMaintenanceCleanupAuthority(t *testing.T) {
	repository := manageddatapostgres.NewWithOptions(admissionDB{}, manageddatapostgres.Options{
		Workflow: admissionWorkflow{}, Audit: admissionAudit{},
	})
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(t.Context(), Config{Persistence: &persistence, Production: true})
	if err == nil || !strings.Contains(err.Error(), "maintenance cleanup authority") {
		t.Fatalf("production build error = %v, want maintenance cleanup authority requirement", err)
	}
	var typedNil *manageddatapostgres.Maintenance
	_, err = Build(t.Context(), Config{Persistence: &persistence, Production: true, CleanupAcker: typedNil})
	if err == nil || !strings.Contains(err.Error(), "maintenance cleanup authority") {
		t.Fatalf("production typed-nil build error = %v, want maintenance cleanup authority requirement", err)
	}
}
