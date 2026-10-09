package app

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

func TestSourceCredentialPolicyMatchesDurableActiveGeneration(t *testing.T) {
	snapshot := tusSnapshot(t, "principal", "connection_sales", false)
	target := deploymentpostgres.DeliveryTarget{ProjectID: "project_demo", Environment: "prod", ActiveGenerationID: "generation_1", ActivePublicationID: "publication_1"}
	if err := validateSourceCredentialSnapshot(target, snapshot, "project_demo", "prod"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name   string
		mutate func(*deploymentpostgres.DeliveryTarget)
	}{
		{"retired runtime", func(t *deploymentpostgres.DeliveryTarget) { t.ActiveGenerationID = "generation_2" }},
		{"unpublished", func(t *deploymentpostgres.DeliveryTarget) { t.ActivePublicationID = "" }},
		{"other project", func(t *deploymentpostgres.DeliveryTarget) { t.ProjectID = "project_other" }},
		{"other environment", func(t *deploymentpostgres.DeliveryTarget) { t.Environment = "test" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			other := target
			change.mutate(&other)
			if err := validateSourceCredentialSnapshot(other, snapshot, "project_demo", "prod"); !errors.Is(err, access.ErrForbidden) {
				t.Fatal(err)
			}
		})
	}
	if err := validateSourceCredentialSnapshot(target, accesssnapshot.AuthorizationSnapshot{}, "project_demo", "prod"); !errors.Is(err, access.ErrForbidden) {
		t.Fatal(err)
	}
}
