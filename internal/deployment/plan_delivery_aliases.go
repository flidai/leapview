package deployment

// Short aliases keep the control-plane vocabulary convenient for adapters
// while the Delivery-prefixed names remain unambiguous beside legacy APIs.
type Plan = DeliveryPlan
type BuildAttempt = DeliveryBuildAttempt
type PublicationIntent = DeliveryPublication
type CatalogRoot = DeliveryGeneration
type Generation = DeliveryGeneration
type QueryLease = DeliveryQueryLease
type WriterLease = DeliveryWriterLease
type GCCycle = DeliveryGCCycle
type RetentionRoot = DeliveryRetentionException

func NewPublicationIntent(value PublicationIntent) (PublicationIntent, error) {
	return NewDeliveryPublication(value)
}

func NewCatalogRoot(value CatalogRoot) (CatalogRoot, error) {
	return NewDeliveryGeneration(value)
}

func NewLease(value QueryLease) (QueryLease, error) {
	return NewDeliveryQueryLease(value)
}

func NewGCCycle(value GCCycle) (GCCycle, error) {
	return NewDeliveryGCCycle(value)
}
