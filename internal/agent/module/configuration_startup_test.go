package module

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

type startupClosedProviderAdmission struct{}

func (startupClosedProviderAdmission) Acquire(context.Context) (context.Context, func(), error) {
	return nil, nil, errors.New("startup closed")
}
func (startupClosedProviderAdmission) Wait(context.Context) (context.Context, func(), error) {
	return nil, nil, errors.New("startup closed")
}

func TestGatedStartupRetainsDurableAgentOwnershipBeforeDecryption(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "customer version", true: "unsupported legacy"}[legacy], func(t *testing.T) {
			pool := postgrestest.Open(t, agentpostgres.ApplySchema)
			repository, err := agentpostgres.NewProduction(pool, agentpostgres.Options{Workflow: moduleWorkflowStub{}, Jobs: moduleJobsStub{}, Audit: moduleAuditStub{}, Domain: moduleDomainStub{}})
			if err != nil {
				t.Fatal(err)
			}
			record := agent.ConfigurationRevision{ActorID: "admin", CredentialVersionID: uuid.NewString()}
			if legacy {
				record.CredentialVersionID = ""
				record.Credential = []byte("retained legacy ciphertext")
			}
			if _, err = repository.SaveConfiguration(t.Context(), 0, record); err != nil {
				t.Fatal(err)
			}
			persistence, err := NewPostgresPersistence(repository)
			if err != nil {
				t.Fatal(err)
			}
			service := agent.NewService(repository, agent.Config{})
			credentials := &routeConfigurationCredentials{store: repository}
			built, err := Build(t.Context(), Config{Service: service, Persistence: &persistence, ConfigurationCredentials: credentials, ProviderAdmission: startupClosedProviderAdmission{}, ProjectID: projectgraph.ResourceID("project:startup"), ModelConfigFile: filepath.Join(t.TempDir(), "obsolete-missing-file.json"), RecordAudit: func(context.Context, access.AuditEventInput) error { return nil }})
			if err != nil {
				t.Fatalf("durable ownership was overridden by unused deployment file: %v", err)
			}
			if built.service != service || service.ConfigurationManager() == nil || service.AdminManaged() || service.Enabled() {
				t.Fatal("startup must expose recovery without decrypting or installing before reconciliation")
			}
		})
	}
}
