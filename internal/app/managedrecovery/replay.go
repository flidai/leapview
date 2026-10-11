package managedrecovery

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/app/providerrestore"
)

// ReplayManagedRecovery recovers a lost completion acknowledgement only after
// fresh admission checks of the existing providers, credentials and original
// writer fence. It does not recreate missing data or reopen service. The caller
// must hold exclusive ownership of the enrolled instance home.
func ReplayManagedRecovery(ctx context.Context, config ManagedConfig, authority ManagedAuthorities) (providerrestore.Report, error) {
	evidence, err := AdmitManagedRecovery(ctx, config, authority)
	if err != nil {
		return providerrestore.Report{}, err
	}
	current, err := readManagedAdmission(ctx, config, authority)
	if err != nil || current.occurrence.Evidence[0].SHA256 != evidence.ReportSHA256 {
		return providerrestore.Report{}, errors.New("managed recovery acknowledgement changed after verification")
	}
	return current.report, nil
}
