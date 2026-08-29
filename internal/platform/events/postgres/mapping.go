package postgres

import (
	"encoding/json"
	"time"

	eventsdb "github.com/flidai/leapview/internal/platform/events/postgres/internal/db"
)

func eventFromRow(row eventsdb.GetEventByIDRow) Event {
	return Event{EventID: row.EventID, ScopeID: row.ScopeID, AggregateType: row.AggregateType, AggregateID: row.AggregateID, AggregateVersion: row.AggregateVersion, EventType: row.EventType, SchemaVersion: row.SchemaVersion, OccurredAt: row.OccurredAt.Time.UTC(), CorrelationID: row.CorrelationID, Payload: json.RawMessage(row.Payload)}
}

func deliveryFromValues(consumerID, eventID, status string, attempts, generation int64, availableAt time.Time, claimedBy string, claimedUntil, terminalAt time.Time, evidence string) Delivery {
	return Delivery{ConsumerID: consumerID, EventID: eventID, Status: status, Attempts: attempts, ClaimGeneration: generation, AvailableAt: availableAt.UTC(), ClaimedBy: claimedBy, ClaimedUntil: claimedUntil.UTC(), TerminalAt: terminalAt.UTC(), Evidence: json.RawMessage(evidence)}
}
