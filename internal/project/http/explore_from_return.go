package http

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/authoring"
)

// ExploreReturnSurface is a closed set of browser surfaces to which an
// explorer handoff may return. Raw request URLs are intentionally not
// accepted, preventing an authenticated handoff from becoming an open
// redirector.
type ExploreReturnSurface string

const (
	ExploreReturnExplorer  ExploreReturnSurface = "explore"
	ExploreReturnDashboard ExploreReturnSurface = "dashboard"
)

type ExploreReturnContext struct {
	Surface     ExploreReturnSurface
	DashboardID authoring.DashboardID
	PageID      string
}

// ExploreReturnContextFromValues decodes only the separate return parameters
// emitted by an exploration handoff. It never accepts a path/URL parameter,
// so a canonical exploration link cannot become an open redirector.
func ExploreReturnContextFromValues(values url.Values) (ExploreReturnContext, bool, error) {
	surface, present := singletonExploreReturnValue(values, "returnSurface")
	if !present {
		return ExploreReturnContext{}, false, nil
	}
	dashboard, dashboardPresent := singletonExploreReturnValue(values, "returnDashboard")
	page, pagePresent := singletonExploreReturnValue(values, "returnPage")
	if dashboardPresent && dashboard == "" || pagePresent && page == "" {
		return ExploreReturnContext{}, false, errors.New("exploration return parameters must not be empty")
	}
	value := ExploreReturnContext{Surface: ExploreReturnSurface(surface)}
	if dashboardPresent {
		value.DashboardID = authoring.DashboardID(dashboard)
	}
	if pagePresent {
		value.PageID = page
	}
	if _, err := value.Path(); err != nil {
		return ExploreReturnContext{}, false, err
	}
	return value, true, nil
}

func singletonExploreReturnValue(values url.Values, key string) (string, bool) {
	items, present := values[key]
	if !present {
		return "", false
	}
	if len(items) != 1 {
		return "", true
	}
	return strings.TrimSpace(items[0]), true
}

func (c ExploreReturnContext) Path() (string, error) {
	switch c.Surface {
	case ExploreReturnExplorer:
		if c.DashboardID != "" || strings.TrimSpace(c.PageID) != "" {
			return "", errors.New("explore return context must not include dashboard state")
		}
		return "/explore", nil
	case ExploreReturnDashboard:
		if err := authoring.ValidateDashboardID(c.DashboardID); err != nil {
			return "", fmt.Errorf("return dashboard: %w", err)
		}
		page := strings.TrimSpace(c.PageID)
		if page == "" {
			return "/dashboards/" + url.PathEscape(c.DashboardID.String()), nil
		}
		if !validDashboardRouteID(page) {
			return "", errors.New("return dashboard page id is invalid")
		}
		return "/dashboards/" + url.PathEscape(c.DashboardID.String()) + "/pages/" + url.PathEscape(page), nil
	default:
		return "", fmt.Errorf("unsupported explore return surface %q", c.Surface)
	}
}
