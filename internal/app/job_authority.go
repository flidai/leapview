package app

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/revalidation"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	"github.com/flidai/leapview/pkg/jobs"
)

// Keep operation selection in composition. The revalidator receives a closed
// requirement rather than importing a particular producer or job kind.
type executionGrantAuthorityReader = revalidation.ExecutionGrantAuthorityReader

func newAuthorityRevalidator(tokens access.APITokenAuthorityEvidenceReader, sessions access.SessionAuthorityEvidenceReader, grants executionGrantAuthorityReader, current func(context.Context, string, access.PermissionPair, string) (bool, error), delegatedCurrent func(context.Context, string, access.PermissionPair, string) (bool, error), instanceID, environment string) jobs.AuthorityRevalidator {
	requirement, err := refreshmodule.CreateRefreshRunTypedOperationRequirement()
	return revalidation.NewAuthorityRevalidator(tokens, sessions, grants, current, delegatedCurrent, requirement, err, instanceID, environment)
}

func postgresJobAuthority(production bool, repository access.Repository, current func(context.Context, string, access.PermissionPair, string) (bool, error), instanceID, environment string) (jobs.AuthorityRevalidator, map[string]struct{}, error) {
	// Development can enqueue a real PAT/session envelope too. Every nonzero
	// envelope needs the same live credential and serving-permission checks;
	// production alone requires an envelope from every migrated producer.
	tokens, ok := repository.(access.APITokenAuthorityEvidenceReader)
	if !ok {
		return nil, nil, errors.New("PostgreSQL access repository does not support async token authority evidence")
	}
	sessions, ok := repository.(access.SessionAuthorityEvidenceReader)
	if !ok {
		return nil, nil, errors.New("PostgreSQL access repository does not support browser-session authority evidence")
	}
	grants, ok := repository.(executionGrantAuthorityReader)
	if !ok {
		return nil, nil, errors.New("PostgreSQL access repository does not support execution-grant authority evidence")
	}
	var requiredKinds map[string]struct{}
	if production {
		requiredKinds = map[string]struct{}{"refresh_pipeline": {}}
	}
	return newAuthorityRevalidator(tokens, sessions, grants, current, current, instanceID, environment), requiredKinds, nil
}
