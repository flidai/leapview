package access

// Semantic-attribute audit actions are owned by the durable access
// repository. API contracts use these canonical mutation actions; repository
// idempotent replays may append their corresponding replay evidence action.
const (
	SemanticAttributeAuditActionRegister              = "semantic_attribute.register"
	SemanticAttributeAuditActionRegisterReplay        = "semantic_attribute.register_replay"
	SemanticAttributeAuditActionMetadataUpdate        = "semantic_attribute.metadata_update"
	SemanticAttributeAuditActionMetadataReplay        = "semantic_attribute.metadata_replay"
	SemanticAttributeAuditActionDisable               = "semantic_attribute.disable"
	SemanticAttributeAuditActionEnable                = "semantic_attribute.enable"
	SemanticAttributeAuditActionAssignmentSet         = "semantic_attribute.assignment.set"
	SemanticAttributeAuditActionAssignmentReplay      = "semantic_attribute.assignment.replay"
	SemanticAttributeAuditActionAssignmentTombstone   = "semantic_attribute.assignment.tombstone"
	SemanticAttributeAuditActionClaimMappingSet       = "semantic_attribute.claim_mapping.set"
	SemanticAttributeAuditActionClaimMappingReplay    = "semantic_attribute.claim_mapping.replay"
	SemanticAttributeAuditActionClaimMappingTombstone = "semantic_attribute.claim_mapping.tombstone"
)
