package deployment

import "context"

// DeliveryReader is the read-only use-case boundary for typed delivery
// status APIs. Implementations must return durable control-plane rows only;
// object-store clients and credentials never cross this interface.
type DeliveryReader interface {
	PlanByID(context.Context, string) (DeliveryPlan, error)
	DeliveryBuildAttemptByID(context.Context, string) (DeliveryBuildAttempt, error)
	DeliveryCatalogSealByID(context.Context, string) (CatalogSeal, error)
	DeliveryCandidateByID(context.Context, string) (DeliveryCandidate, error)
	DeliveryGenerationByID(context.Context, string) (DeliveryGeneration, error)
	DeliveryPublicationByID(context.Context, string) (DeliveryPublication, error)
	DeliveryOperatorSnapshot(context.Context, string, string) (DeliveryOperatorSnapshot, error)
}
