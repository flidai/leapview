package authoring

// ResourceCreateReceipt is the bounded result of creating a dashboard draft.
// Consumers must perform a separately authorized read to obtain any other
// resource fields.
type ResourceCreateReceipt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
