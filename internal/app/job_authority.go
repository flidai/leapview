package app

import (
	"context"

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
