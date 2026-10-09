package credential

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCommittedActivationRequiresSwitchAndCannotBeAborted(t *testing.T) {
	input := testActivationPreparation()
	now := time.Now().UTC().Truncate(time.Microsecond)
	prepared := PreparedActivation{Preparation: input, CreatedAt: now, SwitchingAt: now, CommittedAt: now}
	if err := prepared.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.AbortAuditIntent(uuid.NewString(), input.Receipt.ActorID); err == nil {
		t.Fatal("committed operation can be aborted")
	}
	for name, mutate := range map[string]func(*PreparedActivation){
		"without switch":  func(p *PreparedActivation) { p.SwitchingAt = time.Time{} },
		"before switch":   func(p *PreparedActivation) { p.CommittedAt = now.Add(-time.Microsecond) },
		"timestamp alias": func(p *PreparedActivation) { p.CommittedAt = now.Add(time.Nanosecond) },
		"also aborted":    func(p *PreparedActivation) { p.AbortedAt = now; p.AbortedBy = input.Receipt.ActorID },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := prepared
			mutate(&invalid)
			if invalid.Validate() == nil {
				t.Fatal("invalid committed state accepted")
			}
		})
	}
}

func TestCommitAuditDescribesPublicationWithoutReadiness(t *testing.T) {
	input := testActivationPreparation()
	now := time.Now().UTC().Truncate(time.Microsecond)
	prepared := PreparedActivation{Preparation: input, CreatedAt: now, SwitchingAt: now}
	intent, err := prepared.CommitAuditIntent(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if intent.Action != "credential.activation.committed" || intent.AggregateSequence != 3 || intent.ActorID != input.Receipt.ActorID {
		t.Fatalf("incorrect commit audit: %#v", intent)
	}
	prepared.CommittedAt = now
	if _, err := prepared.CommitAuditIntent(uuid.NewString()); err == nil {
		t.Fatal("repeated commit audit accepted")
	}
}
