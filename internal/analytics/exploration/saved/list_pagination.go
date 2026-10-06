package saved

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	// DefaultListLimit is the public API default. A zero ListRequest limit is
	// reserved for internal/browser callers that need the complete visible
	// list; the repository still reads it in bounded SQL batches.
	DefaultListLimit = 50
	MaxListLimit     = 200
	maxListCursorLen = 512
)

type listCursor struct {
	ProjectID       string `json:"projectId"`
	IncludeArchived bool   `json:"includeArchived"`
	ExplorationID   string `json:"explorationId"`
}

// EncodeListCursor creates the opaque continuation token returned after a
// visible item. Its scope binding prevents a token from one project or
// includeArchived view from being reused against another list.
func EncodeListCursor(projectID projectgraph.ResourceID, includeArchived bool, id ExplorationID) (string, error) {
	if err := projectID.Validate(); err != nil {
		return "", fmt.Errorf("%w: list cursor project id: %v", ErrInvalid, err)
	}
	if err := id.Validate(); err != nil {
		return "", fmt.Errorf("%w: list cursor exploration id: %v", ErrInvalid, err)
	}
	raw, err := json.Marshal(listCursor{ProjectID: projectID.String(), IncludeArchived: includeArchived, ExplorationID: id.String()})
	if err != nil {
		return "", fmt.Errorf("%w: encode list cursor: %v", ErrInvalid, err)
	}
	token := "s1." + base64.RawURLEncoding.EncodeToString(raw)
	if len(token) > maxListCursorLen {
		return "", fmt.Errorf("%w: list cursor is too long", ErrInvalid)
	}
	return token, nil
}

// DecodeListCursor validates the continuation token's project and filter
// scope before returning the internal SQL key. Empty tokens mean the first
// page. No repository key from an unauthorized row is ever exposed here:
// application service callers only encode a key after authorization.
func DecodeListCursor(token string, projectID projectgraph.ResourceID, includeArchived bool) (ExplorationID, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	if len(token) > maxListCursorLen || !strings.HasPrefix(token, "s1.") {
		return "", fmt.Errorf("%w: invalid list cursor", ErrInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "s1."))
	if err != nil {
		return "", fmt.Errorf("%w: invalid list cursor", ErrInvalid)
	}
	var cursor listCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.ProjectID == "" || cursor.ExplorationID == "" {
		return "", fmt.Errorf("%w: invalid list cursor", ErrInvalid)
	}
	if cursor.ProjectID != projectID.String() || cursor.IncludeArchived != includeArchived {
		return "", fmt.Errorf("%w: list cursor scope does not match request", ErrInvalid)
	}
	id := ExplorationID(cursor.ExplorationID)
	if err := id.Validate(); err != nil {
		return "", fmt.Errorf("%w: invalid list cursor", ErrInvalid)
	}
	return id, nil
}
