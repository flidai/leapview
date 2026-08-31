package watermill

import (
	"fmt"
	"sort"
)

// topicAggregates is the single source of truth for the durable event topic
// families. Keep each family sorted so its representation remains stable when
// inspected or copied for a subscriber.
var topicAggregates = map[string][]string{
	TopicAgent: {
		"agent_conversation", "agent_run",
	},
	TopicDashboard: {
		"dashboard_appearance", "dashboard_authoring", "dashboard_publication",
	},
	TopicDelivery: {
		"delivery_approval", "delivery_build", "delivery_plan",
		"delivery_publication", "delivery_target",
	},
	TopicRelease: {
		"release",
	},
}

// aggregateTopics is derived once from topicAggregates. An aggregate with
// zero topics is unknown, while one with more than one topic is ambiguous;
// both cases are rejected by TopicForAggregate instead of guessing a route.
var aggregateTopics = indexAggregateTopics(topicAggregates)

func indexAggregateTopics(families map[string][]string) map[string][]string {
	indexed := make(map[string][]string)
	for topic, aggregates := range families {
		for _, aggregate := range aggregates {
			indexed[aggregate] = append(indexed[aggregate], topic)
		}
	}
	for aggregate := range indexed {
		sort.Strings(indexed[aggregate])
	}
	return indexed
}

// TopicForAggregate returns the one canonical topic admitted for a durable
// aggregate type. Unknown and multiply-admitted aggregate types fail closed.
func TopicForAggregate(aggregateType string) (string, error) {
	return topicForAggregate(aggregateTopics, aggregateType)
}

func topicForAggregate(index map[string][]string, aggregateType string) (string, error) {
	if aggregateType == "" {
		return "", validation("aggregateType", "is not allowlisted", ErrUnknownAggregate)
	}
	topics, ok := index[aggregateType]
	if !ok {
		return "", validation("aggregateType", fmt.Sprintf("%q is not allowlisted", aggregateType), ErrUnknownAggregate)
	}
	if len(topics) != 1 || topics[0] == "" {
		return "", validation("aggregateType", fmt.Sprintf("%q maps to %d topics", aggregateType, len(topics)), ErrAmbiguousAggregate)
	}
	return topics[0], nil
}

// AggregatesForTopic returns a sorted defensive copy of the aggregate types
// admitted by topic. Unknown topics and malformed/ambiguous family entries
// fail closed.
func AggregatesForTopic(topic string) ([]string, error) {
	return aggregatesForTopic(topicAggregates, topic)
}

// aggregatesForTopic validates one topic family and returns a sorted
// defensive copy. Keeping the family map injectable makes malformed
// allowlists testable without mutating the package's production registry.
func aggregatesForTopic(families map[string][]string, topic string) ([]string, error) {
	aggregates, ok := families[topic]
	if !ok {
		return nil, validation("topic", fmt.Sprintf("%q is not allowlisted", topic), ErrUnknownTopic)
	}
	if len(aggregates) == 0 {
		return nil, validation("topic", fmt.Sprintf("%q has no allowlisted aggregate types", topic), ErrAmbiguousAggregate)
	}
	result := append([]string(nil), aggregates...)
	sort.Strings(result)
	for _, aggregate := range result {
		canonical, err := TopicForAggregate(aggregate)
		if err != nil {
			return nil, err
		}
		if canonical != topic {
			return nil, validation("aggregateType", fmt.Sprintf("%q is admitted by %q, not %q", aggregate, canonical, topic), ErrAmbiguousAggregate)
		}
	}
	return result, nil
}
