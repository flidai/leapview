package manageddata

import (
	"fmt"
	"regexp"
	"strings"
)

// Operational IDs identify durable managed-data records. They are deliberately
// distinct from project-graph ResourceID values: revisions and upload protocol
// records are not graph resources and must never be used as graph edges.
type RevisionID string
type UploadID string
type MultipartUploadID string

func (id RevisionID) String() string        { return string(id) }
func (id UploadID) String() string          { return string(id) }
func (id MultipartUploadID) String() string { return string(id) }

var operationalIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$`)

func parseOperationalID(kind, value string) (string, error) {
	if value != strings.TrimSpace(value) || !operationalIDPattern.MatchString(value) {
		return "", fmt.Errorf("%s must be a canonical operational id", kind)
	}
	return value, nil
}

func ParseRevisionID(value string) (RevisionID, error) {
	id, err := parseOperationalID("revision id", value)
	return RevisionID(id), err
}

func ParseUploadID(value string) (UploadID, error) {
	id, err := parseOperationalID("upload id", value)
	return UploadID(id), err
}

func ParseMultipartUploadID(value string) (MultipartUploadID, error) {
	id, err := parseOperationalID("multipart upload id", value)
	return MultipartUploadID(id), err
}
