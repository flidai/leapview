// Package usage records authenticated dashboard viewer-days and ranks popular
// dashboards across a LeapView instance.
package usage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	PopularityWindow = 30 * 24 * time.Hour
	RetentionWindow  = 90 * 24 * time.Hour
	MinimumViewers   = 3
)

type Key struct {
	WorkspaceID string
	DashboardID string
}

func (key Key) CatalogID() string {
	return key.WorkspaceID + "." + key.DashboardID
}

type View struct {
	WorkspaceID string
	DashboardID string
	PageID      string
	PrincipalID string
	ViewedAt    time.Time
}

func (view View) Validate() error {
	for label, value := range map[string]string{
		"workspace": view.WorkspaceID, "dashboard": view.DashboardID,
		"page": view.PageID, "principal": view.PrincipalID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("dashboard view %s is required", label)
		}
	}
	if view.ViewedAt.IsZero() {
		return fmt.Errorf("dashboard view timestamp is required")
	}
	return nil
}

type Summary struct {
	Key
	ViewerCount  int64
	ViewerDays   int64
	LastViewedAt time.Time
}

type Level string

const (
	LevelLow    Level = "low"
	LevelMedium Level = "medium"
	LevelHigh   Level = "high"
)

type RankedPopularity struct {
	Key
	Level Level
}

type Recorder interface {
	RecordView(context.Context, View) error
}

type Reader interface {
	ListSummaries(context.Context, time.Time) ([]Summary, error)
}

// RankPopularity returns popularity levels for the top thirty percent of
// configured dashboards, provided each result reached the minimum
// distinct-viewer floor. Ranking is instance-wide: viewer breadth wins,
// followed by viewer-days and recency. The top ten percent are high, the next
// ten percent medium, and the final ten percent low.
func RankPopularity(summaries []Summary, dashboardCount int) []RankedPopularity {
	if dashboardCount <= 0 || len(summaries) == 0 {
		return nil
	}
	eligible := slices.DeleteFunc(append([]Summary(nil), summaries...), func(summary Summary) bool {
		return summary.ViewerCount < MinimumViewers
	})
	slices.SortStableFunc(eligible, func(left, right Summary) int {
		if left.ViewerCount != right.ViewerCount {
			return compareInt64(right.ViewerCount, left.ViewerCount)
		}
		if left.ViewerDays != right.ViewerDays {
			return compareInt64(right.ViewerDays, left.ViewerDays)
		}
		if !left.LastViewedAt.Equal(right.LastViewedAt) {
			if left.LastViewedAt.After(right.LastViewedAt) {
				return -1
			}
			return 1
		}
		if left.WorkspaceID != right.WorkspaceID {
			return strings.Compare(left.WorkspaceID, right.WorkspaceID)
		}
		return strings.Compare(left.DashboardID, right.DashboardID)
	})
	highLimit := percentageCeiling(dashboardCount, 10)
	mediumLimit := percentageCeiling(dashboardCount, 20)
	lowLimit := min(percentageCeiling(dashboardCount, 30), len(eligible))
	ranked := make([]RankedPopularity, lowLimit)
	for index := range lowLimit {
		level := LevelLow
		switch {
		case index < highLimit:
			level = LevelHigh
		case index < mediumLimit:
			level = LevelMedium
		}
		ranked[index] = RankedPopularity{Key: eligible[index].Key, Level: level}
	}
	return ranked
}

func percentageCeiling(total, percentage int) int {
	return (total*percentage + 99) / 100
}

func compareInt64(left, right int64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
