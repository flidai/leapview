package authoring

import "testing"

func TestCreateOperationLookupKeyDoesNotRelaxWriteValidation(t *testing.T) {
	operation := CreateOperation{ProjectID: "project", ActorID: "actor", Kind: "fork", IdempotencyKey: "command"}
	if err := operation.ValidateKey(); err != nil {
		t.Fatalf("lookup key: %v", err)
	}
	if err := operation.Validate(); err == nil {
		t.Fatal("mutation accepted a missing payload fingerprint")
	}
	operation.Fingerprint = "original-request"
	if err := operation.Validate(); err != nil {
		t.Fatalf("mutation: %v", err)
	}
	for name, mutate := range map[string]func(*CreateOperation){
		"project": func(o *CreateOperation) { o.ProjectID = "" },
		"actor":   func(o *CreateOperation) { o.ActorID = "" },
		"kind":    func(o *CreateOperation) { o.Kind = "unknown" },
		"key":     func(o *CreateOperation) { o.IdempotencyKey = "" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := operation
			mutate(&invalid)
			if err := invalid.ValidateKey(); err == nil {
				t.Fatal("lookup accepted an invalid key")
			}
			if err := invalid.Validate(); err == nil {
				t.Fatal("mutation accepted an invalid key")
			}
		})
	}
}
