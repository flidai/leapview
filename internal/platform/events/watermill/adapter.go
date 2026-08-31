// Package watermill adapts LeapView's canonical PostgreSQL event authority to
// Watermill's in-process message boundary.  The PostgreSQL event log remains
// authoritative: this package never starts or finishes a transaction and does
// not create Watermill SQL tables.
package watermill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

const (
	// EnvelopeVersion is the only envelope version currently admitted by the
	// adapter.  A future incompatible change must add a new version explicitly.
	EnvelopeVersion = 1

	// MaxPayloadBytes mirrors the canonical event authority's payload check.
	MaxPayloadBytes int64 = 65536
	// MaxEnvelopeBytes bounds the complete Watermill payload, including its
	// envelope fields.  It leaves room for the payload's JSON object and the
	// bounded event identity fields without allowing unbounded transport data.
	MaxEnvelopeBytes int64 = 131072

	// MetadataTopic is the sole transport metadata key.  Event data belongs in
	// the versioned envelope, never in Watermill metadata.
	MetadataTopic = "topic"
)

const (
	// TopicAgent accepts agent_conversation and agent_run aggregates.
	TopicAgent = "agent"
	// TopicDashboard accepts dashboard_appearance, dashboard_authoring, and
	// dashboard_publication aggregates.
	TopicDashboard = "dashboard"
	// TopicDelivery accepts the bounded deployment delivery aggregate family.
	TopicDelivery = "delivery"
	// TopicRelease accepts the release aggregate.
	TopicRelease = "release"
)

// eventAppender is the narrow canonical authority used by Adapter. Production
// construction accepts only the concrete canonical PostgreSQL repository;
// the interface remains private so tests can observe transaction ownership.
type eventAppender interface {
	AppendEvent(context.Context, eventspostgres.Tx, eventspostgres.EventInput) (eventspostgres.Event, error)
}

// EventInput is intentionally the authority's input type.  No Watermill-only
// metadata or transport offset can enter the durable event write.
type EventInput = eventspostgres.EventInput

// Adapter is stateless apart from the canonical event authority supplied by
// composition and is safe for concurrent callers.
type Adapter struct {
	events eventAppender
}

// New binds an adapter to LeapView's canonical PostgreSQL event authority. No
// transaction or connection is acquired here.
func New(appender *eventspostgres.Repository) (*Adapter, error) {
	if appender == nil {
		return nil, ErrNotConfigured
	}
	return &Adapter{events: appender}, nil
}

func newAdapter(appender eventAppender) (*Adapter, error) {
	if appender == nil {
		return nil, ErrNotConfigured
	}
	return &Adapter{events: appender}, nil
}

// Matches proves that production composition bound this adapter to the exact
// canonical repository supplied by the application authority graph.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	if a == nil || events == nil {
		return false
	}
	configured, ok := a.events.(*eventspostgres.Repository)
	return ok && configured == events
}

// ErrNotConfigured indicates that an adapter has no canonical event authority.
var ErrNotConfigured = errors.New("watermill event adapter is not configured")

// Typed validation sentinels permit callers to classify malformed messages
// without parsing human-readable error text.
var (
	ErrInvalid       = errors.New("invalid watermill event message")
	ErrUnknownTopic  = errors.New("unknown watermill topic")
	ErrTopicMismatch = errors.New("watermill topic and aggregate type mismatch")
	ErrEnvelope      = errors.New("invalid watermill event envelope")
	ErrUUIDMismatch  = errors.New("watermill message UUID and envelope event ID differ")
	ErrMetadata      = errors.New("invalid watermill event metadata")
	ErrSizeLimit     = errors.New("watermill event size limit exceeded")
)

// ValidationError identifies the field rejected by the strict boundary.
type ValidationError struct {
	Field  string
	Reason string
	Kind   error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "invalid watermill event"
	}
	if e.Field == "" {
		return e.Reason
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}

func (e *ValidationError) Unwrap() error {
	if e == nil || e.Kind == nil {
		return ErrInvalid
	}
	return e.Kind
}

func (e *ValidationError) Is(target error) bool {
	return target == ErrInvalid || (e != nil && target == e.Kind)
}

// Envelope is the deterministic, versioned Watermill payload.  The field
// order is intentional: encoding/json emits struct fields in declaration
// order, so equivalent events produce byte-identical messages.
type Envelope struct {
	EnvelopeVersion  int             `json:"envelopeVersion"`
	EventID          string          `json:"eventId"`
	ScopeID          string          `json:"scopeId"`
	AggregateType    string          `json:"aggregateType"`
	AggregateID      string          `json:"aggregateId"`
	AggregateVersion int64           `json:"aggregateVersion"`
	EventType        string          `json:"eventType"`
	SchemaVersion    int64           `json:"schemaVersion"`
	OccurredAt       time.Time       `json:"occurredAt"`
	CorrelationID    string          `json:"correlationId,omitempty"`
	Payload          json.RawMessage `json:"payload"`
}

// AppendEvent appends exactly once to the supplied transaction and returns the
// finalized database projection. It deliberately does not begin, commit, roll
// back, publish, dispatch, or create a framework-owned transport row.
// MessageForEvent deterministically presents the finalized row to Watermill.
func (a *Adapter) AppendEvent(ctx context.Context, tx eventspostgres.Tx, topic string, input EventInput) (eventspostgres.Event, error) {
	if a == nil || a.events == nil {
		return eventspostgres.Event{}, ErrNotConfigured
	}
	if err := validateTopic(topic); err != nil {
		return eventspostgres.Event{}, err
	}
	if err := validateInput(topic, input); err != nil {
		return eventspostgres.Event{}, err
	}
	finalized, err := a.events.AppendEvent(ctx, tx, input)
	if err != nil {
		return eventspostgres.Event{}, err
	}
	if err := validateFinalized(input, finalized); err != nil {
		return eventspostgres.Event{}, err
	}
	// Prove that every admitted canonical row can be reconstructed as the
	// strict Watermill envelope before the caller commits. This performs no
	// dispatch or durable write; the Subscriber rebuilds the same bytes later.
	if _, err := MessageForEvent(topic, finalized); err != nil {
		return eventspostgres.Event{}, err
	}
	return finalized, nil
}

func validateFinalized(input EventInput, event eventspostgres.Event) error {
	if input.EventID != "" && event.EventID != input.EventID {
		return validation("eventId", "canonical authority returned a different identity", ErrUUIDMismatch)
	}
	if event.ScopeID != input.ScopeID || event.AggregateType != input.AggregateType ||
		event.AggregateID != input.AggregateID || event.EventType != input.EventType ||
		event.SchemaVersion != input.SchemaVersion || event.CorrelationID != input.CorrelationID {
		return validation("event", "canonical authority returned a different immutable identity", ErrEnvelope)
	}
	// The concrete authority returns PostgreSQL's stored payload::text. JSONB
	// intentionally normalizes equivalent numeric spellings (for example 1e3
	// and 1000), so byte comparison with the producer input would reject a
	// valid append. MessageForEvent validates and encodes the stored projection.
	return nil
}

// MessageForEvent is a pure conversion from a finalized canonical event.  It
// performs no I/O and never mutates the event or payload supplied by the
// caller.
func MessageForEvent(topic string, event eventspostgres.Event) (*message.Message, error) {
	if err := validateTopic(topic); err != nil {
		return nil, err
	}
	if !aggregateAllowed(topic, event.AggregateType) {
		return nil, validation("aggregateType", "is not accepted by topic", ErrTopicMismatch)
	}
	if err := validateIdentity("eventId", event.EventID, 36); err != nil {
		return nil, err
	}
	if err := validateUUIDv7(event.EventID); err != nil {
		return nil, validation("eventId", "must be a canonical UUIDv7", ErrEnvelope)
	}
	if err := validateIdentity("scopeId", event.ScopeID, 255); err != nil {
		return nil, err
	}
	if err := validateIdentity("aggregateType", event.AggregateType, 255); err != nil {
		return nil, err
	}
	if err := validateIdentity("aggregateId", event.AggregateID, 255); err != nil {
		return nil, err
	}
	if event.AggregateVersion <= 0 {
		return nil, validation("aggregateVersion", "must be positive", ErrEnvelope)
	}
	if err := validateIdentity("eventType", event.EventType, 255); err != nil {
		return nil, err
	}
	if event.SchemaVersion <= 0 {
		return nil, validation("schemaVersion", "must be positive", ErrEnvelope)
	}
	if event.OccurredAt.IsZero() || !isUTC(event.OccurredAt) {
		return nil, validation("occurredAt", "must be a UTC timestamp", ErrEnvelope)
	}
	if event.CorrelationID != "" {
		if err := validateUUID(event.CorrelationID); err != nil {
			return nil, validation("correlationId", "must be a canonical UUID", ErrEnvelope)
		}
	}
	payload, err := canonicalObject(event.Payload, MaxPayloadBytes)
	if err != nil {
		kind := ErrEnvelope
		if errors.Is(err, strictjson.ErrSizeLimit) {
			kind = ErrSizeLimit
		}
		return nil, validation("payload", err.Error(), kind)
	}
	envelope := Envelope{
		EnvelopeVersion: EnvelopeVersion,
		EventID:         event.EventID, ScopeID: event.ScopeID,
		AggregateType: event.AggregateType, AggregateID: event.AggregateID,
		AggregateVersion: event.AggregateVersion, EventType: event.EventType,
		SchemaVersion: event.SchemaVersion, OccurredAt: event.OccurredAt.UTC(),
		CorrelationID: event.CorrelationID, Payload: payload,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal envelope: %v", ErrEnvelope, err)
	}
	if int64(len(encoded)) > MaxEnvelopeBytes {
		return nil, validation("envelope", fmt.Sprintf("exceeds %d bytes", MaxEnvelopeBytes), ErrSizeLimit)
	}
	msg := message.NewMessage(event.EventID, encoded)
	msg.Metadata.Set(MetadataTopic, topic)
	return msg, nil
}

// MessageForEvent exposes the pure conversion through an Adapter as well as
// the package-level helper.  It intentionally does not consult the bound
// repository, so callers can use it after a commit or while replaying rows.
func (a *Adapter) MessageForEvent(topic string, event eventspostgres.Event) (*message.Message, error) {
	return MessageForEvent(topic, event)
}

// DecodeMessage strictly validates and decodes a message produced by
// MessageForEvent. The topic comes from the caller's Watermill delivery path;
// the sole required metadata key must agree with it but is never trusted as
// event identity.
func DecodeMessage(topic string, msg *message.Message) (Envelope, error) {
	if err := validateTopic(topic); err != nil {
		return Envelope{}, err
	}
	if msg == nil {
		return Envelope{}, validation("message", "is required", ErrInvalid)
	}
	if int64(len(msg.Payload)) > MaxEnvelopeBytes {
		return Envelope{}, validation("payload", fmt.Sprintf("envelope exceeds %d bytes", MaxEnvelopeBytes), ErrSizeLimit)
	}
	if err := validateUUIDv7(msg.UUID); err != nil {
		return Envelope{}, validation("message.UUID", "must be a canonical UUIDv7", ErrEnvelope)
	}
	if len(msg.Metadata) != 1 {
		return Envelope{}, validation("metadata", "must contain exactly the topic key", ErrMetadata)
	}
	for key := range msg.Metadata {
		if key != MetadataTopic {
			return Envelope{}, validation("metadata", fmt.Sprintf("unsupported key %q", key), ErrMetadata)
		}
	}
	if got, ok := msg.Metadata[MetadataTopic]; ok && got != topic {
		return Envelope{}, validation("metadata.topic", "must equal topic", ErrMetadata)
	}
	if err := validateEnvelopeKeys(msg.Payload); err != nil {
		return Envelope{}, err
	}
	var envelope Envelope
	if err := strictjson.DecodeWithOptions(msg.Payload, &envelope, strictjson.Options{
		MaxBytes:           MaxEnvelopeBytes,
		MaxDepth:           100,
		DuplicateKeys:      strictjson.CaseSensitiveKeys,
		AllowUnknownFields: false,
	}); err != nil {
		return Envelope{}, validation("envelope", err.Error(), ErrEnvelope)
	}
	if envelope.EventID != msg.UUID {
		return Envelope{}, &ValidationError{Field: "eventId", Reason: "does not equal message.UUID", Kind: ErrUUIDMismatch}
	}
	if envelope.EnvelopeVersion != EnvelopeVersion {
		return Envelope{}, validation("envelopeVersion", fmt.Sprintf("must equal %d", EnvelopeVersion), ErrEnvelope)
	}
	if !aggregateAllowed(topic, envelope.AggregateType) {
		return Envelope{}, validation("aggregateType", "is not accepted by topic", ErrTopicMismatch)
	}
	if err := validateEnvelopeFields(envelope); err != nil {
		return Envelope{}, err
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(canonical, msg.Payload) {
		return Envelope{}, validation("envelope", "must use deterministic canonical JSON", ErrEnvelope)
	}
	return envelope, nil
}

func validateEnvelopeKeys(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return validation("envelope", err.Error(), ErrEnvelope)
	}
	allowed := map[string]struct{}{
		"envelopeVersion": {}, "eventId": {}, "scopeId": {}, "aggregateType": {},
		"aggregateId": {}, "aggregateVersion": {}, "eventType": {}, "schemaVersion": {},
		"occurredAt": {}, "correlationId": {}, "payload": {},
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return validation("envelope", fmt.Sprintf("unknown field %q", key), ErrEnvelope)
		}
	}
	return nil
}

// DecodeMessage is also available as an Adapter method for composition code
// that keeps all event-boundary operations behind one value.
func (a *Adapter) DecodeMessage(topic string, msg *message.Message) (Envelope, error) {
	return DecodeMessage(topic, msg)
}

func validateEnvelopeFields(envelope Envelope) error {
	if err := validateUUIDv7(envelope.EventID); err != nil {
		return validation("eventId", "must be a canonical UUIDv7", ErrEnvelope)
	}
	for _, field := range []struct {
		name, value string
		max         int
	}{
		{"scopeId", envelope.ScopeID, 255},
		{"aggregateType", envelope.AggregateType, 255},
		{"aggregateId", envelope.AggregateID, 255},
		{"eventType", envelope.EventType, 255},
	} {
		if err := validateIdentity(field.name, field.value, field.max); err != nil {
			return err
		}
	}
	if envelope.AggregateVersion <= 0 {
		return validation("aggregateVersion", "must be positive", ErrEnvelope)
	}
	if envelope.SchemaVersion <= 0 {
		return validation("schemaVersion", "must be positive", ErrEnvelope)
	}
	if envelope.OccurredAt.IsZero() || !isUTC(envelope.OccurredAt) {
		return validation("occurredAt", "must be a UTC timestamp", ErrEnvelope)
	}
	if envelope.CorrelationID != "" {
		if err := validateUUID(envelope.CorrelationID); err != nil {
			return validation("correlationId", "must be a canonical UUID", ErrEnvelope)
		}
	}
	canonical, err := canonicalObject(envelope.Payload, MaxPayloadBytes)
	if err != nil {
		kind := ErrEnvelope
		if errors.Is(err, strictjson.ErrSizeLimit) {
			kind = ErrSizeLimit
		}
		return validation("payload", err.Error(), kind)
	}
	if !bytes.Equal(canonical, envelope.Payload) {
		return validation("payload", "must use deterministic canonical JSON", ErrEnvelope)
	}
	return nil
}

func validateTopic(topic string) error {
	if _, ok := topicAggregates[topic]; !ok {
		return validation("topic", fmt.Sprintf("%q is not allowlisted", topic), ErrUnknownTopic)
	}
	return nil
}

func validateInput(topic string, input EventInput) error {
	if !aggregateAllowed(topic, input.AggregateType) {
		return validation("aggregateType", "is not accepted by topic", ErrTopicMismatch)
	}
	if err := validateIdentity("scopeId", input.ScopeID, 255); err != nil {
		return err
	}
	if err := validateIdentity("aggregateType", input.AggregateType, 255); err != nil {
		return err
	}
	if err := validateIdentity("aggregateId", input.AggregateID, 255); err != nil {
		return err
	}
	if err := validateIdentity("eventType", input.EventType, 255); err != nil {
		return err
	}
	if input.SchemaVersion <= 0 {
		return validation("schemaVersion", "must be positive", ErrEnvelope)
	}
	if input.EventID != "" {
		if err := validateIdentity("eventId", input.EventID, 36); err != nil {
			return err
		}
		if err := validateUUIDv7(input.EventID); err != nil {
			return validation("eventId", "must be a canonical UUIDv7", ErrEnvelope)
		}
	}
	if input.CorrelationID != "" {
		if err := validateUUID(input.CorrelationID); err != nil {
			return validation("correlationId", "must be a canonical UUID", ErrEnvelope)
		}
	}
	if _, err := canonicalObject(input.Payload, MaxPayloadBytes); err != nil {
		kind := ErrEnvelope
		if errors.Is(err, strictjson.ErrSizeLimit) {
			kind = ErrSizeLimit
		}
		return validation("payload", err.Error(), kind)
	}
	return nil
}

var topicAggregates = map[string]map[string]struct{}{
	TopicAgent: {
		"agent_conversation": {}, "agent_run": {},
	},
	TopicDashboard: {
		"dashboard_appearance": {}, "dashboard_authoring": {}, "dashboard_publication": {},
	},
	TopicDelivery: {
		"delivery_approval": {}, "delivery_build": {}, "delivery_plan": {},
		"delivery_publication": {}, "delivery_target": {},
	},
	TopicRelease: {
		"release": {},
	},
}

func aggregateAllowed(topic, aggregate string) bool {
	aggregates, ok := topicAggregates[topic]
	if !ok {
		return false
	}
	_, ok = aggregates[aggregate]
	return ok
}

func validateIdentity(field, value string, max int) error {
	if value == "" {
		return validation(field, "is required", ErrEnvelope)
	}
	if value != strings.TrimSpace(value) {
		return validation(field, "must not contain surrounding whitespace", ErrEnvelope)
	}
	if len(value) > max {
		return validation(field, fmt.Sprintf("exceeds %d bytes", max), ErrSizeLimit)
	}
	return nil
}

func validateUUID(value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return errors.New("not a canonical UUID")
	}
	return nil
}

func validateUUIDv7(value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 7 {
		return errors.New("not a canonical UUIDv7")
	}
	return nil
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}

func validation(field, reason string, kind error) error {
	return &ValidationError{Field: field, Reason: reason, Kind: kind}
}

func canonicalObject(raw json.RawMessage, maxBytes int64) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var validated json.RawMessage
	if err := strictjson.DecodeWithOptions(raw, &validated, strictjson.Options{
		MaxBytes: maxBytes, MaxDepth: 100,
		DuplicateKeys: strictjson.CaseSensitiveKeys, AllowUnknownFields: true,
	}); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("must be a JSON object")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	if int64(len(canonical)) > maxBytes {
		return nil, fmt.Errorf("exceeds %d bytes after canonicalization", maxBytes)
	}
	return canonical, nil
}
