package authoring

import "github.com/flidai/leapview/internal/dashboard"

func (d *Dashboard) PageOrDefault(pageID string) (dashboard.Page, bool) {
	if len(d.Pages) == 0 {
		return dashboard.Page{}, false
	}
	if pageID != "" {
		for _, page := range d.Pages {
			if page.ID == pageID {
				return page, true
			}
		}
	}
	return d.Pages[0], true
}
