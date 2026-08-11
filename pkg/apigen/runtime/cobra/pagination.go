package cobra

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// FetchAllPages follows next_page_token until the resource is exhausted.
func FetchAllPages(client *Client, method, path string, baseQuery url.Values) ([]interface{}, error) {
	var items []interface{}
	pageToken := ""

	for {
		query := url.Values{}
		for key, values := range baseQuery {
			query[key] = append([]string(nil), values...)
		}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}

		resp, err := client.Do(method, path, query, nil, "", "")
		if err != nil {
			return nil, err
		}
		if err := CheckError(resp); err != nil {
			return nil, err
		}

		body, err := ReadBody(resp)
		if err != nil {
			return nil, err
		}

		var page PaginatedResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}

		items = append(items, page.Data...)
		if page.NextPageToken == "" {
			if len(items) == 0 {
				return nil, nil
			}
			return items, nil
		}
		pageToken = page.NextPageToken
	}
}
