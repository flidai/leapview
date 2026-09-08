package deployment

type DeliveryRollbackClass string

const (
	DeliveryRollbackSafe  DeliveryRollbackClass = "rollback_safe"
	DeliveryServingSafe   DeliveryRollbackClass = "serving_safe"
	DeliveryNonReversible DeliveryRollbackClass = "non_reversible"
)
