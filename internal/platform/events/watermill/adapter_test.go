package watermill

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/stretchr/testify/require"
)

const testEventID = "01900000-0000-7000-8000-000000000001"

type countingAppender struct {
	event eventspostgres.Event
	err   error
	calls int
}

func (f *countingAppender) AppendEvent(_ context.Context, _ eventspostgres.Tx, _ eventspostgres.EventInput) (eventspostgres.Event, error) {
	f.calls++
	return f.event, f.err
}

func testEvent() eventspostgres.Event {
	return eventspostgres.Event{
		EventID: testEventID, ScopeID: "scope", AggregateType: "agent_run",
		AggregateID: "run-1", AggregateVersion: 7, EventType: "agent_run.completed",
		SchemaVersion: 1, OccurredAt: time.Date(2026, 8, 31, 10, 0, 0, 123456000, time.UTC),
		CorrelationID: "01900000-0000-7000-8000-000000000002",
		Payload:       json.RawMessage(`{"z":9007199254740993,"a":true}`),
	}
}

func TestMessageForEventDeterministicRoundTrip(t *testing.T) {
	event := testEvent()
	first, err := MessageForEvent(TopicAgent, event)
	require.NoError(t, err)
	second, err := MessageForEvent(TopicAgent, event)
	require.NoError(t, err)
	require.Equal(t, first.UUID, event.EventID)
	require.Equal(t, first.Payload, second.Payload)
	require.Equal(t, message.Metadata{MetadataTopic: TopicAgent}, first.Metadata)
	decoded, err := DecodeMessage(TopicAgent, first)
	require.NoError(t, err)
	require.Equal(t, EnvelopeVersion, decoded.EnvelopeVersion)
	require.Equal(t, event.EventID, decoded.EventID)
	require.Equal(t, event.AggregateVersion, decoded.AggregateVersion)
	require.Equal(t, event.OccurredAt, decoded.OccurredAt)
	require.JSONEq(t, `{"a":true,"z":9007199254740993}`, string(decoded.Payload))
}

func TestDecodeMessageRejectsMalformedUnknownAndMismatched(t *testing.T) {
	msg, err := MessageForEvent(TopicAgent, testEvent())
	require.NoError(t, err)
	cases := []struct {
		name   string
		mutate func(*message.Message)
		want   error
	}{
		{"uuid mismatch", func(m *message.Message) { m.UUID = "01900000-0000-7000-8000-000000000099" }, ErrUUIDMismatch},
		{"topic mismatch", func(m *message.Message) {}, ErrTopicMismatch},
		{"unknown envelope field", func(m *message.Message) {
			m.Payload = append(m.Payload[:len(m.Payload)-1], []byte(`,"extra":true}`)...)
		}, ErrEnvelope},
		{"unknown metadata", func(m *message.Message) { m.Metadata["caller"] = "secret" }, ErrMetadata},
		{"missing metadata", func(m *message.Message) { delete(m.Metadata, MetadataTopic) }, ErrMetadata},
		{"non object payload", func(m *message.Message) {
			m.Payload = []byte(`{"envelopeVersion":1,"eventId":"` + testEventID + `","scopeId":"scope","aggregateType":"agent_run","aggregateId":"run-1","aggregateVersion":1,"eventType":"agent_run.completed","schemaVersion":1,"occurredAt":"2026-08-31T10:00:00Z","payload":[]}`)
		}, ErrEnvelope},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := msg.Copy()
			copy.Metadata = map[string]string{}
			for k, v := range msg.Metadata {
				copy.Metadata[k] = v
			}
			copy.Payload = append([]byte(nil), msg.Payload...)
			tc.mutate(copy)
			gotTopic := TopicAgent
			if tc.name == "topic mismatch" {
				gotTopic = TopicRelease
				copy.Metadata[MetadataTopic] = gotTopic
			}
			_, err := DecodeMessage(gotTopic, copy)
			require.Error(t, err)
			require.True(t, errors.Is(err, tc.want), "error = %v", err)
		})
	}
}

func TestMessageForEventRejectsUnknownTopicAndMismatch(t *testing.T) {
	event := testEvent()
	_, err := MessageForEvent("unknown", event)
	require.ErrorIs(t, err, ErrUnknownTopic)
	_, err = MessageForEvent(TopicRelease, event)
	require.ErrorIs(t, err, ErrTopicMismatch)
}

func TestTopicAggregateAllowlist(t *testing.T) {
	cases := []struct {
		topic, aggregate string
	}{
		{TopicAgent, "agent_conversation"}, {TopicAgent, "agent_run"},
		{TopicDashboard, "dashboard_appearance"}, {TopicDashboard, "dashboard_authoring"}, {TopicDashboard, "dashboard_publication"},
		{TopicDelivery, "delivery_approval"}, {TopicDelivery, "delivery_build"},
		{TopicDelivery, "delivery_plan"}, {TopicDelivery, "delivery_publication"},
		{TopicDelivery, "delivery_target"},
		{TopicRelease, "release"},
	}
	for _, tc := range cases {
		event := testEvent()
		event.AggregateType = tc.aggregate
		_, err := MessageForEvent(tc.topic, event)
		require.NoError(t, err, "%s/%s", tc.topic, tc.aggregate)
	}
	for _, tc := range []struct{ topic, aggregate string }{
		{TopicAgent, "dashboard_appearance"}, {TopicDashboard, "agent_run"}, {TopicDelivery, "release"}, {TopicRelease, "delivery_target"},
	} {
		event := testEvent()
		event.AggregateType = tc.aggregate
		_, err := MessageForEvent(tc.topic, event)
		require.ErrorIs(t, err, ErrTopicMismatch, "%s/%s", tc.topic, tc.aggregate)
	}
}

func TestMessageForEventAndDecodeEnforceSizeBounds(t *testing.T) {
	event := testEvent()
	event.Payload = json.RawMessage(`{"value":"` + strings.Repeat("x", int(MaxPayloadBytes)) + `"}`)
	_, err := MessageForEvent(TopicAgent, event)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSizeLimit)

	msg, err := MessageForEvent(TopicAgent, testEvent())
	require.NoError(t, err)
	msg.Payload = append(msg.Payload, bytesForSize(MaxEnvelopeBytes+1-int64(len(msg.Payload)))...)
	_, err = DecodeMessage(TopicAgent, msg)
	require.ErrorIs(t, err, ErrSizeLimit)
}

func bytesForSize(n int64) []byte {
	if n <= 0 {
		return nil
	}
	return make([]byte, n)
}

func TestAppendEventCallsAppenderExactlyOnce(t *testing.T) {
	appender := &countingAppender{event: testEvent()}
	adapter, err := newAdapter(appender)
	require.NoError(t, err)
	input := EventInput{ScopeID: "scope", AggregateType: "agent_run", AggregateID: "run-1", EventType: "agent_run.completed", SchemaVersion: 1, CorrelationID: testEvent().CorrelationID, Payload: []byte(`{"z":9007199254740993,"a":true}`)}
	finalized, err := adapter.AppendEvent(context.Background(), nil, TopicAgent, input)
	require.NoError(t, err)
	require.Equal(t, 1, appender.calls)
	require.Equal(t, testEvent(), finalized)
}

func TestAppendEventPreflightsInvalidExplicitEventID(t *testing.T) {
	appender := &countingAppender{event: testEvent()}
	adapter, err := newAdapter(appender)
	require.NoError(t, err)
	_, err = adapter.AppendEvent(context.Background(), nil, TopicAgent, EventInput{
		EventID: "01900000-0000-4000-8000-000000000001", ScopeID: "scope",
		AggregateType: "agent_run", AggregateID: "run-1", EventType: "agent_run.completed",
		SchemaVersion: 1, Payload: []byte(`{"ok":true}`),
	})
	require.Error(t, err)
	require.Equal(t, 0, appender.calls, "invalid input must not reach the canonical authority")
}

func TestAppendEventRejectsMismatchedFinalizedProjection(t *testing.T) {
	event := testEvent()
	event.EventType = "agent_run.failed"
	appender := &countingAppender{event: event}
	adapter, err := newAdapter(appender)
	require.NoError(t, err)
	_, err = adapter.AppendEvent(context.Background(), nil, TopicAgent, EventInput{
		ScopeID: "scope", AggregateType: "agent_run", AggregateID: "run-1",
		EventType: "agent_run.completed", SchemaVersion: 1, Payload: testEvent().Payload,
		CorrelationID: testEvent().CorrelationID,
	})
	require.ErrorIs(t, err, ErrEnvelope)
	require.Equal(t, 1, appender.calls)
}

func TestAppendEventRejectsUnprojectableFinalizedEvent(t *testing.T) {
	event := testEvent()
	event.AggregateVersion = 0
	appender := &countingAppender{event: event}
	adapter, err := newAdapter(appender)
	require.NoError(t, err)
	_, err = adapter.AppendEvent(context.Background(), nil, TopicAgent, EventInput{
		ScopeID: "scope", AggregateType: "agent_run", AggregateID: "run-1",
		EventType: "agent_run.completed", SchemaVersion: 1, Payload: testEvent().Payload,
		CorrelationID: testEvent().CorrelationID,
	})
	require.ErrorIs(t, err, ErrEnvelope)
	require.Equal(t, 1, appender.calls)
}

func TestNewRejectsNilAppender(t *testing.T) {
	_, err := New(nil)
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = newAdapter(nil)
	require.ErrorIs(t, err, ErrNotConfigured)
}
