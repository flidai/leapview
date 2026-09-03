package http

// The API deliberately has closed value envelopes. In particular, these are
// not map[string]any decoders: accepting an object here would make the
// canonical semantic-value profile and its audit identity ambiguous.

type semanticAttributeValueWire struct {
	Type           string  `json:"type"`
	StringValue    *string `json:"stringValue"`
	BooleanValue   *bool   `json:"booleanValue"`
	IntegerValue   *string `json:"integerValue"`
	DecimalValue   *string `json:"decimalValue"`
	DateValue      *string `json:"dateValue"`
	TimestampValue *string `json:"timestampValue"`
}

type semanticAttributeMetadataWire struct {
	OwnerKind        string  `json:"ownerKind"`
	OwnerID          *string `json:"ownerId"`
	DisplayName      string  `json:"displayName"`
	Description      string  `json:"description"`
	DocumentationURL *string `json:"documentationUrl"`
}

type semanticAttributeRegisterWire struct {
	Name     string                        `json:"name"`
	Type     string                        `json:"type"`
	Shape    string                        `json:"shape"`
	Metadata semanticAttributeMetadataWire `json:"metadata"`
}

type semanticAttributeAssignmentWire struct {
	Values []semanticAttributeValueWire `json:"values"`
}

type semanticAttributeClaimMappingWire struct {
	SourceKind string `json:"sourceKind"`
	Provider   string `json:"provider"`
	Issuer     string `json:"issuer"`
	Audience   string `json:"audience"`
	Claim      string `json:"claim"`
}

type semanticAttributeImpactWire struct {
	TargetKind string                       `json:"targetKind"`
	TargetID   string                       `json:"targetId"`
	Values     []semanticAttributeValueWire `json:"values"`
}
