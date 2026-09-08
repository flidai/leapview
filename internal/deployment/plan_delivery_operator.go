package deployment

// DeliveryPlanEvidenceView is the deliberately redacted projection exposed by
// the delivery API. The canonical evidence document may contain authored
// paths, intervals, and connector observations; those values stay in the
// control plane. Clients receive the digest and bounded review summaries only.
type DeliveryPlanEvidenceView struct {
	Digest                  string                          `json:"digest"`
	ImpactStatement         string                          `json:"impactStatement,omitempty"`
	PhysicalWorkStatement   string                          `json:"physicalWorkStatement,omitempty"`
	ReuseStatement          string                          `json:"reuseStatement,omitempty"`
	CompatibilityBreaking   bool                            `json:"compatibilityBreaking,omitempty"`
	AddedCount              int                             `json:"addedCount,omitempty"`
	RemovedCount            int                             `json:"removedCount,omitempty"`
	DirectlyModifiedCount   int                             `json:"directlyModifiedCount,omitempty"`
	IndirectlyAffectedCount int                             `json:"indirectlyAffectedCount,omitempty"`
	ReuseCount              int                             `json:"reuseCount,omitempty"`
	QualificationStepCount  int                             `json:"qualificationStepCount,omitempty"`
	RollbackClass           string                          `json:"rollbackClass,omitempty"`
	PlannedInputs           []DeliveryPlannedInputView      `json:"plannedInputs"`
	QualificationPolicy     string                          `json:"qualificationPolicy"`
	QualificationSteps      []DeliveryQualificationStepView `json:"qualificationSteps"`
	StalePolicy             DeliveryStalePolicyView         `json:"stalePolicy"`
	ReuseDecisions          []DeliveryReuseDecisionView     `json:"reuseDecisions"`
}

type DeliveryPlannedInputView struct {
	ID       string                `json:"id"`
	Mode     DeliveryDataInputMode `json:"mode"`
	Revision string                `json:"revision,omitempty"`
	Bound    string                `json:"bound,omitempty"`
}

type DeliveryResolvedInputView struct {
	ID                string                `json:"id"`
	Mode              DeliveryDataInputMode `json:"mode"`
	PlannedRevision   string                `json:"plannedRevision,omitempty"`
	PlannedBound      string                `json:"plannedBound,omitempty"`
	ActualRevision    string                `json:"actualRevision,omitempty"`
	ActualBound       string                `json:"actualBound,omitempty"`
	ObservationDigest string                `json:"observationDigest,omitempty"`
	Explanation       string                `json:"explanation"`
}

type DeliveryQualificationStepView struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Blocking    bool   `json:"blocking"`
}

type DeliveryStalePolicyView struct {
	Mode              string `json:"mode"`
	AllowRetainedBase bool   `json:"allowRetainedBase"`
	Description       string `json:"description,omitempty"`
}

type DeliveryReuseDecisionView struct {
	ResourceID     string `json:"resourceId"`
	Reusable       bool   `json:"reusable"`
	RetainBase     bool   `json:"retainBase"`
	Reason         string `json:"reason"`
	ReuseKeyDigest string `json:"reuseKeyDigest,omitempty"`
}

// RedactedDeliveryPlanEvidence intentionally omits raw observed values,
// connector paths, and restatement intervals from the public read model.
func RedactedDeliveryPlanEvidence(plan DeliveryPlan) DeliveryPlanEvidenceView {
	e := plan.Evidence
	plannedInputs := make([]DeliveryPlannedInputView, 0, len(plan.Execution.DataInputs))
	for _, input := range plan.Execution.DataInputs {
		plannedInputs = append(plannedInputs, DeliveryPlannedInputView{ID: input.ID, Mode: input.Mode, Revision: input.Revision, Bound: input.Bound})
	}
	qualificationSteps := make([]DeliveryQualificationStepView, 0, len(e.Qualification.Steps))
	for _, step := range e.Qualification.Steps {
		qualificationSteps = append(qualificationSteps, DeliveryQualificationStepView{ID: step.ID, Kind: step.Kind, Description: step.Description, Required: step.Required, Blocking: step.Blocking})
	}
	reuseDecisions := make([]DeliveryReuseDecisionView, 0, len(e.Reuse))
	for _, decision := range e.Reuse {
		reuseDecisions = append(reuseDecisions, DeliveryReuseDecisionView{ResourceID: decision.ResourceID, Reusable: decision.Reusable, RetainBase: decision.RetainBase, Reason: decision.Reason, ReuseKeyDigest: decision.ReuseKeyDigest})
	}
	return DeliveryPlanEvidenceView{
		Digest:                plan.EvidenceDigest,
		ImpactStatement:       e.ImpactStatement,
		PhysicalWorkStatement: e.PhysicalWorkStatement,
		ReuseStatement:        e.ReuseStatement,
		CompatibilityBreaking: e.Compatibility.Breaking,
		AddedCount:            len(e.GraphImpact.Added), RemovedCount: len(e.GraphImpact.Removed),
		DirectlyModifiedCount: len(e.GraphImpact.DirectlyModified), IndirectlyAffectedCount: len(e.GraphImpact.IndirectlyAffected),
		ReuseCount: len(e.Reuse), QualificationStepCount: len(e.Qualification.Steps), RollbackClass: string(e.Rollback.Class),
		PlannedInputs: plannedInputs, QualificationPolicy: e.Qualification.Policy, QualificationSteps: qualificationSteps,
		StalePolicy: DeliveryStalePolicyView{Mode: e.StalePolicy.Mode, AllowRetainedBase: e.StalePolicy.AllowRetainedBase, Description: e.StalePolicy.Description}, ReuseDecisions: reuseDecisions,
	}
}
