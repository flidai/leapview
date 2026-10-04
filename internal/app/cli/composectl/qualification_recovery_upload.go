package composectl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	manageddataqualificationbarrier "github.com/flidai/leapview/internal/manageddata/qualificationbarrier"
)

type qualificationClientCommandSpec struct {
	token       string
	environment map[string]string
	arguments   []string
}

type qualificationManagedUploadSessionStatus struct {
	Status string `json:"status"`
	Files  []struct {
		Negotiation struct {
			TUS struct {
				Offset int64 `json:"offset"`
			} `json:"tus"`
		} `json:"negotiation"`
	} `json:"files"`
}

func qualificationManagedUploadSyncCommand(options qualificationRecoveryOptions) qualificationClientCommandSpec {
	return qualificationClientCommandSpec{
		token: string(options.RecoveryUploadToken),
		environment: map[string]string{
			manageddataqualificationbarrier.EnabledEnv:   manageddataqualificationbarrier.EnabledValue,
			manageddataqualificationbarrier.PathEnv:      "/client-home",
			manageddataqualificationbarrier.ProjectIDEnv: options.ProjectID,
		},
		arguments: []string{
			"leapview", "data", "sync",
			"--source-root", "/work/project-a",
			"--project-id", options.ProjectID,
			"--connection", "sample",
			"--from", "/work/input",
			"--format", "json",
		},
	}
}

func readQualificationManagedUploadSessionStatus(
	ctx context.Context,
	client *http.Client,
	apiRoot string,
	options qualificationRecoveryOptions,
	sessionID string,
) (qualificationManagedUploadSessionStatus, error) {
	var result qualificationManagedUploadSessionStatus
	endpoint := fmt.Sprintf(
		"%s%s/upload-sessions/%s",
		apiRoot, qualificationManagedConnectionPath(options.ProjectID), urlPath(sessionID),
	)
	err := qualificationAPI(
		ctx, client, http.MethodGet, endpoint,
		string(options.ConnectionEvidenceToken), nil, "", &result,
	)
	return result, err
}

func waitForQualificationManagedUploadEvents(
	ctx context.Context,
	client *http.Client,
	apiRoot string,
	options qualificationRecoveryOptions,
	sessionID string,
) (json.RawMessage, error) {
	endpoint := apiRoot + fmt.Sprintf(
		"%s/upload-sessions/%s/events?limit=100",
		qualificationManagedConnectionPath(options.ProjectID), urlPath(sessionID),
	)
	return waitForQualificationEvents(
		ctx, client, endpoint, string(options.ConnectionEvidenceToken),
		[]string{"upload_session.created", "upload_session.finalizing", "upload_session.completed"},
	)
}
