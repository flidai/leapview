package module

import "github.com/flidai/leapview/internal/credential"

type ActivationService = credential.ActivationService
type ActivationRequest = credential.ActivationRequest
type ActivationStatus = credential.ActivationStatus
type RuntimeCredentials = credential.RuntimeCredentials
type ActivationRecord = credential.ActivationRecord
type ActivationRequestRecord = credential.ActivationRequestRecord
type ActivationAuthority = credential.ActivationAuthority
type ActivationRuntime = credential.ActivationRuntime
type ProviderAdmission = credential.ProviderAdmission
type ActivationCoordinator = credential.ActivationCoordinator

var NewProviderAdmission = credential.NewProviderAdmission
var NewActivationCoordinator = credential.NewActivationCoordinator

type ValidationReceipt = credential.ValidationReceipt
