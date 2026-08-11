package cli

import (
	"errors"
	"fmt"
	"strings"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

// generatedProblemError turns a declared APIGen problem into a useful CLI
// error while keeping transport and unexpected-problem errors untouched.
func generatedProblemError(operation string, problem apigenclient.ProblemDetails) error {
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

func mapCommitProjectCandidateSynchronizationFailure(err error) error {
	var failure deploymentgen.GenCommitProjectCandidateSynchronizationFailure
	if !errors.As(err, &failure) {
		return err
	}
	handler := func(problem apigenclient.ProblemDetails) error {
		return generatedProblemError("commit candidate synchronization", problem)
	}
	return deploymentgen.MatchGenCommitProjectCandidateSynchronizationFailure(
		failure, handler, handler, handler, handler,
	)
}

func mapUploadProjectCandidateSourceBlobFailure(err error) error {
	var failure deploymentgen.GenUploadProjectCandidateSourceBlobFailure
	if !errors.As(err, &failure) {
		return err
	}
	handler := func(problem apigenclient.ProblemDetails) error {
		return generatedProblemError("upload candidate source blob", problem)
	}
	return deploymentgen.MatchGenUploadProjectCandidateSourceBlobFailure(
		failure, handler, handler, handler, handler, handler,
	)
}

func mapPublishProjectCandidateFailure(err error) error {
	var failure deploymentgen.GenPublishProjectCandidateFailure
	if !errors.As(err, &failure) {
		return err
	}
	handler := func(problem apigenclient.ProblemDetails) error {
		return generatedProblemError("publish candidate", problem)
	}
	return deploymentgen.MatchGenPublishProjectCandidateFailure(
		failure,
		handler, handler, handler, handler, handler, handler, handler,
		handler, handler, handler, handler, handler, handler,
	)
}
