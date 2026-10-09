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

type ManagedAuthorityInitializationInput struct {
	SchemaVersion            int            `json:"schemaVersion"`
	Bootstrap                AuthorityInput `json:"bootstrap"`
	Operator                 AuthorityInput `json:"operator"`
	OriginalSystemIdentifier string         `json:"originalSystemIdentifier"`
	OwnerRole                string         `json:"ownerRole"`
	ReceiptFile              string         `json:"receiptFile"`
}

func ReadManagedAuthorityInitializationInput(path string) (ManagedAuthorityInitializationInput, error) {
	value, err := readBoundedManagedPrivateFile(path, 64<<10)
	if err != nil {
		return ManagedAuthorityInitializationInput{}, errors.New("private managed authority initialization input unavailable")
	}
	var input ManagedAuthorityInitializationInput
	if strictjson.DecodeWithOptions(value, &input, strictjson.Options{MaxBytes: 64 << 10}) != nil || input.validate() != nil {
		return ManagedAuthorityInitializationInput{}, errors.New("exact private independent authority initialization input required")
	}
	for _, protected := range []string{path, input.Bootstrap.URLFile, input.Bootstrap.RootCAFile, input.Operator.URLFile, input.Operator.RootCAFile} {
		if input.ReceiptFile == protected {
			return ManagedAuthorityInitializationInput{}, errors.New("authority receipt cannot overwrite private input or credentials")
		}
	}
	return input, nil
}

func (input ManagedAuthorityInitializationInput) validate() error {
	if input.SchemaVersion != 1 || input.OriginalSystemIdentifier == "" || input.Bootstrap.SystemIdentifier == "" || input.Operator.SystemIdentifier != input.Bootstrap.SystemIdentifier || input.OriginalSystemIdentifier == input.Bootstrap.SystemIdentifier || !dedicatedRecoveryRole(input.OwnerRole, "leapview_recovery_owner") || !dedicatedRecoveryRole(input.Operator.Role, "leapview_recovery_operator") || !filepath.IsAbs(input.ReceiptFile) || filepath.Clean(input.ReceiptFile) != input.ReceiptFile || validatePrivateSecretRoot(filepath.Dir(input.ReceiptFile)) != nil {
		return errors.New("exact private independent authority initialization input required")
	}
	for _, protected := range []string{input.Bootstrap.URLFile, input.Bootstrap.RootCAFile, input.Operator.URLFile, input.Operator.RootCAFile} {
		if input.ReceiptFile == protected {
			return errors.New("authority receipt cannot overwrite credentials")
		}
	}
	return nil
}

func (input ManagedAuthorityInitializationInput) Initialize(ctx context.Context) (ManagedAuthorityInitializationReceipt, error) {
	if err := input.validate(); err != nil {
		return ManagedAuthorityInitializationReceipt{}, err
	}
	bootstrapURL, err := readBoundedManagedPrivateFile(input.Bootstrap.URLFile, maxManagedCredentialsBytes)
	if err != nil {
		return ManagedAuthorityInitializationReceipt{}, errors.New("private authority bootstrap URL unavailable")
	}
	operatorURL, err := readBoundedManagedPrivateFile(input.Operator.URLFile, maxManagedCredentialsBytes)
	if err != nil {
		return ManagedAuthorityInitializationReceipt{}, errors.New("private authority operator URL unavailable")
	}
	bootstrapCA, err := readBoundedManagedPrivateFile(input.Bootstrap.RootCAFile, maxManagedCredentialsBytes)
	if err != nil {
		return ManagedAuthorityInitializationReceipt{}, errors.New("private authority bootstrap CA unavailable")
	}
	operatorCA, err := readBoundedManagedPrivateFile(input.Operator.RootCAFile, maxManagedCredentialsBytes)
	if err != nil || !bytes.Equal(bootstrapCA, operatorCA) {
		return ManagedAuthorityInitializationReceipt{}, errors.New("authority operator must retain exact bootstrap TLS trust")
	}
	bootstrap, err := managedConnectionConfig(string(bootstrapURL), input.Bootstrap.Role, string(bootstrapCA))
	if err != nil {
		return ManagedAuthorityInitializationReceipt{}, errors.New("exact authority bootstrap connection required")
	}
	operator, err := managedConnectionConfig(string(operatorURL), input.Operator.Role, string(operatorCA))
	if err != nil || operator.Host != bootstrap.Host || operator.Port != bootstrap.Port || operator.Database != bootstrap.Database {
		return ManagedAuthorityInitializationReceipt{}, errors.New("operator must bind the exact initialized authority endpoint/database")
	}
	request := ManagedAuthorityInitializationRequest{SystemIdentifier: input.Bootstrap.SystemIdentifier, Database: operator.Database, OwnerRole: input.OwnerRole, OperatorRole: operator.User, OperatorPassword: operator.Password}
	expected := authorityInitializationReceipt(request)
	// Refuse conflicting or linked receipts before database/role effects.
	if stored, err := readBoundedManagedPrivateFile(input.ReceiptFile, 64<<10); err == nil {
		var receipt ManagedAuthorityInitializationReceipt
		if strictjson.DecodeWithOptions(stored, &receipt, strictjson.Options{MaxBytes: 64 << 10}) != nil || receipt != expected {
			return expected, errors.New("existing authority receipt differs from exact initialization")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return expected, errors.New("authority receipt is inaccessible or untrusted")
	}
	pool, err := OpenManagedAuthority(ctx, input.Bootstrap, []providerrestore.PrimaryEnrollment{{SystemIdentifier: input.OriginalSystemIdentifier}})
	if err != nil {
		return expected, err
	}
	defer pool.Close()
	receipt, err := InitializeManagedAuthority(ctx, pool, request)
	if err != nil {
		return receipt, err
	}
	// Verify the real operator login and exact system identity after commit. A
	// lost acknowledgement is retried with the same initialized database identity.
	operatorPool, err := OpenManagedAuthority(ctx, input.Operator, []providerrestore.PrimaryEnrollment{{SystemIdentifier: input.OriginalSystemIdentifier}})
	if err != nil {
		return receipt, errors.New("initialized authority operator login could not be verified")
	}
	operatorPool.Close()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return receipt, err
	}
	if stored, err := readBoundedManagedPrivateFile(input.ReceiptFile, 64<<10); err == nil {
		if !bytes.Equal(stored, encoded) {
			return receipt, errors.New("authority receipt changed during initialization")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return receipt, errors.New("authority receipt is unavailable")
	} else if err := securefs.WritePrivateFileAtomicOnce(input.ReceiptFile, encoded, 0600); err != nil {
		return receipt, errors.New("authority initialized; exact receipt persistence unconfirmed, retry exact input")
	}
	return receipt, nil
}
