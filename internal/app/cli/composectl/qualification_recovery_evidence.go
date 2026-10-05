package composectl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func qualificationActiveManagedRevision(
	ctx context.Context,
	client *http.Client,
	apiRoot, projectID string,
	token qualificationConnectionEvidenceToken,
) (string, error) {
	if client == nil || strings.TrimSpace(apiRoot) == "" || strings.TrimSpace(projectID) == "" {
		return "", errors.New("active managed revision request inputs are required")
	}
	credential := strings.TrimSpace(string(token))
	if credential == "" {
		return "", errors.New("dedicated qualification connection-evidence token is required")
	}
	var active struct {
		Revision struct {
			ID string `json:"id"`
		} `json:"revision"`
	}
	endpoint := strings.TrimRight(apiRoot, "/") + qualificationManagedConnectionPath(projectID) + "/active-revision"
	if err := qualificationAPI(
		ctx, client, http.MethodGet, endpoint, credential, nil, "", &active,
	); err != nil {
		return "", fmt.Errorf("read active managed-data revision: %w", err)
	}
	return active.Revision.ID, nil
}
