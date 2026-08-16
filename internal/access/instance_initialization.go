package access

import (
	"context"
	"errors"
	"time"
)

var ErrInstanceAlreadyInitialized = errors.New("LeapView instance is already initialized")

const InstanceInitializedSetting = "instance.initialized"

type InstanceInitializationInput struct {
	Email                string
	Environment          string
	Now                  time.Time
	EvaluationDataIngest bool
}

type InitialInstanceCredentials struct {
	Email                   string
	TemporaryPassword       string
	PublisherToken          string
	PublisherTokenExpiresAt time.Time
}

// InstanceInitializer owns the atomic, audited Access mutation used by
// offline Admin initialization.
type InstanceInitializer interface {
	InitializeInstance(context.Context, InstanceInitializationInput, func(InitialInstanceCredentials) error) (InitialInstanceCredentials, error)
}

func InitialPublisherCapabilities() []Capability {
	return []Capability{
		CapabilityResourceUse,
		CapabilityResourceRead,
		CapabilityResourceEdit,
		CapabilityResourcePublish,
	}
}

// LocalEvaluationPublisherCapabilities extends the one-time publisher only
// far enough to stage the bundled synthetic dataset.
func LocalEvaluationPublisherCapabilities() []Capability {
	capabilities := InitialPublisherCapabilities()
	return append(capabilities, CapabilityResourceManage)
}
