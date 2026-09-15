package document

import (
	"encoding/json"

	"github.com/flidai/leapview/internal/platform/authoringlists"
)

type indexedDashboardSpec DashboardSpec

var visualCollection = []authoringlists.Collection{{Path: "visuals", Identity: "id"}}

func (value DashboardSpec) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(indexedDashboardSpec(value))
	if err != nil {
		return nil, err
	}
	return authoringlists.Rewrite(raw, visualCollection, true)
}
func (value *DashboardSpec) UnmarshalJSON(raw []byte) error {
	indexed, err := authoringlists.Rewrite(raw, visualCollection, false)
	if err != nil {
		return err
	}
	return json.Unmarshal(indexed, (*indexedDashboardSpec)(value))
}
