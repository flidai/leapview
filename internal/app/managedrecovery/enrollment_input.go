package managedrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/flidai/leapview/internal/app/providerrestore"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/pkg/strictjson"
)

type ManagedEnrollmentInput struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Request       ManagedEnrollmentRequest `json:"request"`
	Source        AuthorityInput           `json:"source"`
	Authority     AuthorityInput           `json:"authority"`
	ReceiptFile   string                   `json:"receiptFile"`
}

func ReadManagedEnrollmentInput(path string) (ManagedEnrollmentInput, error) {
	value, err := readBoundedManagedPrivateFile(path, 64<<10)
	if err != nil {
		return ManagedEnrollmentInput{}, errors.New("bounded private enrollment input unavailable")
	}
	var input ManagedEnrollmentInput
	if strictjson.DecodeWithOptions(value, &input, strictjson.Options{MaxBytes: 64 << 10}) != nil || input.SchemaVersion != 1 || validatePrivateSecretRoot(input.Request.InstanceHome) != nil || !filepath.IsAbs(input.ReceiptFile) || filepath.Clean(input.ReceiptFile) != input.ReceiptFile || validatePrivateSecretRoot(filepath.Dir(input.ReceiptFile)) != nil {
		return ManagedEnrollmentInput{}, errors.New("exact private source enrollment input required")
	}
	for _, privateInput := range []string{path, input.Source.URLFile, input.Source.RootCAFile, input.Authority.URLFile, input.Authority.RootCAFile} {
		if filepath.Clean(privateInput) == input.ReceiptFile {
			return ManagedEnrollmentInput{}, errors.New("enrollment receipt must differ from private authority inputs")
		}
	}
	return input, nil
}

// Enroll authenticates both explicit TLS endpoints. The same bounded operator
// connection policy applies to source and destination; each must be independent
// of the other's retained system identity, then the service verifies exact IDs.
// The command must hold the enrolled instance home lock throughout this call.
func (input ManagedEnrollmentInput) Enroll(ctx context.Context) (ManagedEnrollmentReceipt, error) {
	if input.Source.SystemIdentifier == "" || input.Source.SystemIdentifier != input.Request.SourceSystemID || input.Authority.SystemIdentifier == "" || input.Authority.SystemIdentifier != input.Request.AuthoritySystemID {
		return ManagedEnrollmentReceipt{}, errors.New("source and authority inputs differ from exact enrolled system identities")
	}
	source, err := OpenManagedAuthority(ctx, input.Source, []providerrestore.PrimaryEnrollment{{SystemIdentifier: input.Request.AuthoritySystemID}})
	if err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	defer source.Close()
	authority, err := OpenManagedAuthority(ctx, input.Authority, []providerrestore.PrimaryEnrollment{{SystemIdentifier: input.Request.SourceSystemID}})
	if err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	defer authority.Close()
	receipt, err := EnrollManagedRecovery(ctx, source, authority, input.Request)
	if err != nil {
		return ManagedEnrollmentReceipt{}, err
	}
	if err := writeManagedEnrollmentReceipt(input.ReceiptFile, receipt); err != nil {
		return ManagedEnrollmentReceipt{}, errors.New("enrollment committed; retry exact input to retain its private receipt")
	}
	return receipt, nil
}

func writeManagedEnrollmentReceipt(path string, receipt ManagedEnrollmentReceipt) error {
	value, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || validatePrivateSecretRoot(filepath.Dir(path)) != nil {
		return errors.New("private canonical receipt destination required")
	}
	if err := securefs.WritePrivateFileAtomicOnce(path, value, 0600); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		retained, readErr := readBoundedManagedPrivateFile(path, 64<<10)
		if readErr != nil || !bytes.Equal(retained, value) {
			return errors.New("retained managed enrollment receipt differs")
		}
	}
	return nil
}
