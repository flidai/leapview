package watermill

import (
	"errors"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTopicForAggregateAdmitsEveryAggregateExactlyOnce(t *testing.T) {
	seen := make(map[string]string)
	for topic, aggregates := range topicAggregates {
		for _, aggregate := range aggregates {
			canonical, err := TopicForAggregate(aggregate)
			require.NoError(t, err, "%s/%s", topic, aggregate)
			require.Equal(t, topic, canonical, "%s/%s", topic, aggregate)
			if previous, exists := seen[aggregate]; exists {
				t.Fatalf("aggregate %q admitted by both %q and %q", aggregate, previous, topic)
			}
			seen[aggregate] = topic
		}
	}
	require.Len(t, seen, len(aggregateTopics))
	for aggregate, topics := range aggregateTopics {
		require.Len(t, topics, 1, "aggregate %q must map to exactly one topic", aggregate)
		require.Contains(t, seen, aggregate)
	}
}

func TestAggregatesForTopicReturnsSortedDefensiveCopy(t *testing.T) {
	for topic, want := range topicAggregates {
		t.Run(topic, func(t *testing.T) {
			want = append([]string(nil), want...)
			sort.Strings(want)
			got, err := AggregatesForTopic(topic)
			require.NoError(t, err)
			require.Equal(t, want, got)

			got[0] = "mutated"
			again, err := AggregatesForTopic(topic)
			require.NoError(t, err)
			require.Equal(t, want, again)
		})
	}
}

func TestRoutingHelpersRejectUnknownValues(t *testing.T) {
	_, err := TopicForAggregate("unknown_aggregate")
	require.ErrorIs(t, err, ErrUnknownAggregate)

	_, err = TopicForAggregate("")
	require.ErrorIs(t, err, ErrUnknownAggregate)

	_, err = AggregatesForTopic("unknown_topic")
	require.ErrorIs(t, err, ErrUnknownTopic)

	_, err = AggregatesForTopic("")
	require.ErrorIs(t, err, ErrUnknownTopic)
}

func TestTopicForAggregateRejectsAmbiguousMapping(t *testing.T) {
	index := indexAggregateTopics(map[string][]string{
		"topic-a": {"shared"},
		"topic-b": {"shared"},
	})
	topics := index["shared"]
	require.Len(t, topics, 2)
	_, err := topicForAggregate(index, "shared")
	require.ErrorIs(t, err, ErrAmbiguousAggregate)

	_, err = topicForAggregate(index, "missing")
	require.ErrorIs(t, err, ErrUnknownAggregate)
}

func TestAggregatesForTopicRejectsEmptyAllowlistedFamily(t *testing.T) {
	_, err := aggregatesForTopic(map[string][]string{"empty": {}}, "empty")
	require.ErrorIs(t, err, ErrAmbiguousAggregate)
}

func TestRoutingHelperErrorsRemainClassifiable(t *testing.T) {
	_, err := TopicForAggregate("unknown")
	var validationErr *ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.Equal(t, "aggregateType", validationErr.Field)
}
