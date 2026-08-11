package composectl

import (
	"errors"
	"fmt"
	"strings"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

func qualificationGeneratedProblemError(operation string, problem apigenclient.ProblemDetails) error {
	detail := strings.TrimSpace(problem.Detail)
	if detail == "" {
		detail = strings.TrimSpace(problem.Title)
	}
	if detail == "" {
		detail = "request failed"
	}
	if code := strings.TrimSpace(problem.Code); code != "" {
		return fmt.Errorf("%s failed (%s): %s", operation, code, detail)
	}
	return fmt.Errorf("%s failed: %s", operation, detail)
}

func mapQualificationCreatePrincipalFailure(err error) error {
	var failure accessgen.GenCreatePrincipalFailure
	if !errors.As(err, &failure) {
		return err
	}
	handler := func(problem apigenclient.ProblemDetails) error {
		return qualificationGeneratedProblemError("create qualification reviewer", problem)
	}
	return accessgen.MatchGenCreatePrincipalFailure(failure, handler)
}

func mapQualificationCreateGrantFailure(err error) error {
	var failure accessgen.GenCreateGrantFailure
	if !errors.As(err, &failure) {
		return err
	}
	handler := func(problem apigenclient.ProblemDetails) error {
		return qualificationGeneratedProblemError("grant qualification reviewer", problem)
	}
	return accessgen.MatchGenCreateGrantFailure(failure, handler)
}

func mapQualificationCreateCurrentAPITokenFailure(err error) error {
	var failure accessgen.GenCreateCurrentAPITokenFailure
	if !errors.As(err, &failure) {
		return err
	}
	handler := func(problem apigenclient.ProblemDetails) error {
		return qualificationGeneratedProblemError("create qualification API token", problem)
	}
	return accessgen.MatchGenCreateCurrentAPITokenFailure(failure, handler)
}

func mapQualificationApproveDeploymentFailure(err error) error {
	var failure deploymentgen.GenApproveDeploymentFailure
	if !errors.As(err, &failure) {
		return err
	}
	handler := func(problem apigenclient.ProblemDetails) error {
		return qualificationGeneratedProblemError("approve qualification deployment", problem)
	}
	return deploymentgen.MatchGenApproveDeploymentFailure(
		failure, handler, handler, handler, handler,
		handler, handler, handler, handler,
	)
}

func mapQualificationActivateDeploymentFailure(err error) error {
	var failure deploymentgen.GenActivateDeploymentFailure
	if !errors.As(err, &failure) {
		return err
	}
	handler := func(problem apigenclient.ProblemDetails) error {
		return qualificationGeneratedProblemError("activate qualification deployment", problem)
	}
	return deploymentgen.MatchGenActivateDeploymentFailure(
		failure,
		handler, handler, handler, handler, handler, handler,
		handler, handler, handler, handler, handler, handler,
	)
}
